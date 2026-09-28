// Package worker aggregates VisitEvents into per-(link, hour) deltas and applies them,
// and sweeps expired links (Kutt cron.js).
package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// Invalidator is the part of domain.LinkResolver the sweeper needs.
type Invalidator interface {
	Invalidate(ctx context.Context, address string, domainID *int64) error
}

// Worker consumes visit batches and runs the expired-link sweeper.
type Worker struct {
	st       store.Store
	consumer domain.VisitConsumer
	inval    Invalidator
	sweep    time.Duration
	now      func() time.Time

	Applied, Skipped, Failed atomic.Uint64
	Swept                    atomic.Uint64
}

// New builds a worker. linkResolver may be nil (no cache invalidation on sweep).
func New(st store.Store, consumer domain.VisitConsumer, linkResolver Invalidator, cfg *config.Config) *Worker {
	sweep := cfg.ExpiredLinkSweep
	if sweep <= 0 {
		sweep = 30 * time.Second // cron.js setInterval(…, 30_000)
	}
	return &Worker{st: st, consumer: consumer, inval: linkResolver, sweep: sweep, now: time.Now}
}

// Run blocks until ctx is done. The consumer drains/flushes its buffered events on
// cancellation (Channel: drains the buffer; Streams: finishes the in-flight batch, the rest
// stays in Redis), and Run returns once it has.
func (w *Worker) Run(ctx context.Context) error {
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		w.sweepLoop(ctx)
	}()
	err := w.consumer.Run(ctx, w.Handle)
	<-sweepDone
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return err
}

// Handle folds a batch and applies it. Returning an error makes the transport retry
// (Streams: the batch stays pending); ApplyDeltas is per-delta transactional, so a
// partial failure can double count the deltas that succeeded on retry. To bound that,
// only the failed deltas are retried here before giving up.
func (w *Worker) Handle(ctx context.Context, batch []domain.VisitEvent) error {
	deltas := Fold(batch)
	if len(deltas) == 0 {
		return nil
	}
	var err error
	pending := deltas
	for attempt := 0; attempt < 3 && len(pending) > 0; attempt++ {
		var failed []store.VisitDelta
		for i := range pending {
			if e := w.st.Visits().ApplyDeltas(ctx, pending[i:i+1]); e != nil {
				failed = append(failed, pending[i])
				err = e
			} else {
				w.Applied.Add(uint64(pending[i].Total))
			}
		}
		pending = failed
		if len(pending) > 0 && ctx.Err() == nil {
			time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
		}
	}
	if len(pending) > 0 {
		var n int64
		for _, d := range pending {
			n += d.Total
		}
		w.Failed.Add(uint64(n))
		slog.Error("worker: dropping visit deltas after retries", "deltas", len(pending), "visits", n, "err", err)
	}
	// Never return an error for partially-applied batches: re-delivery would double count.
	return nil
}

type foldKey struct {
	linkID int64
	hour   int64 // unix seconds of the UTC hour
}

// Fold aggregates events into deltas keyed by (link_id, UTC hour), applying Kutt's
// normalization (queues/visit.js + visit.queries.js add):
//   - country: upper alpha-2 -> lower-case; "" -> "unknown" (Kutt default "Unknown".toLowerCase())
//   - referrer: hostname without www (removeWww), "." -> "[dot]", lower-cased; "" -> "direct"
//   - browser/os: Kutt bucket names; anything else -> "other"
//
// Events for anonymous links (UserID nil) are skipped: Kutt only queues a visit when
// `link.user_id && !isBot` (links.handler.js:529), so neither visits nor visit_count change.
// Output order follows first appearance, so it is deterministic for a given batch.
func Fold(batch []domain.VisitEvent) []store.VisitDelta {
	idx := make(map[foldKey]int, 16)
	out := make([]store.VisitDelta, 0, 16)
	refMemo := make(map[string]string, 16) // raw referrer -> normalized key (avoid per-event allocs)
	for i := range batch {
		ev := &batch[i]
		if ev.LinkID == 0 || ev.UserID == nil {
			continue
		}
		ts := ev.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		hour := ts.UTC().Truncate(time.Hour)
		k := foldKey{ev.LinkID, hour.Unix()}
		j, ok := idx[k]
		if !ok {
			j = len(out)
			idx[k] = j
			uid := *ev.UserID
			out = append(out, store.VisitDelta{
				LinkID: ev.LinkID, UserID: &uid, Hour: hour,
				Browser:   make(map[string]int64, 2),
				OS:        make(map[string]int64, 2),
				Countries: make(map[string]int64, 2),
				Referrers: make(map[string]int64, 2),
			})
		}
		d := &out[j]
		d.Total++
		d.Browser[bucket(ev.Browser, browsers)]++
		d.OS[bucket(ev.OS, oses)]++
		ck, ok := refMemo["\x00"+ev.Country] // separate namespace in the same memo
		if !ok {
			ck = CountryKey(ev.Country)
			refMemo["\x00"+ev.Country] = ck
		}
		d.Countries[ck]++
		rk, ok := refMemo[ev.Referrer]
		if !ok {
			rk = ReferrerKey(ev.Referrer)
			refMemo[ev.Referrer] = rk
		}
		d.Referrers[rk]++
	}
	return out
}

var (
	browsers = []string{"chrome", "edge", "firefox", "ie", "opera", "other", "safari"}
	oses     = []string{"android", "ios", "linux", "macos", "other", "windows"}
)

func bucket(v string, set []string) string {
	for _, s := range set {
		if strings.EqualFold(v, s) {
			return s
		}
	}
	return "other"
}

// CountryKey: (country || "Unknown").toLowerCase().
func CountryKey(c string) string {
	if c == "" {
		return "unknown"
	}
	if len(c) > 2 {
		c = c[:2]
	}
	return lower(c)
}

// ReferrerKey: (referrer && referrer.replace(/\./gi, "[dot]") || "Direct").toLowerCase().
func ReferrerKey(r string) string {
	if r == "" {
		return "direct"
	}
	return strings.ReplaceAll(lower(r), ".", "[dot]")
}

func lower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}

func (w *Worker) sweepLoop(ctx context.Context) {
	t := time.NewTicker(w.sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.SweepOnce(ctx)
		}
	}
}

// SweepOnce removes expired links (cron.js) and invalidates their cache entries.
func (w *Worker) SweepOnce(ctx context.Context) {
	keys, err := w.st.Links().RemoveExpired(ctx, w.now())
	if err != nil && ctx.Err() == nil {
		slog.Warn("worker: expired-link sweep failed", "err", err)
	}
	for _, k := range keys {
		w.Swept.Add(1)
		if w.inval != nil {
			if err := w.inval.Invalidate(ctx, k.Address, k.DomainID); err != nil {
				slog.Warn("worker: cache invalidation failed", "address", k.Address, "err", err)
			}
		}
	}
}
