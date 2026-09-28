package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Hulul-Group/Tusk/internal/domain"
)

const (
	// StreamKey is the Redis stream holding visit events.
	StreamKey = "tusk:visits"
	// Group is the consumer group shared by all worker replicas.
	Group = "workers"
	// StreamMaxLen caps the stream (approximate trimming).
	StreamMaxLen = 1_000_000
	// ClaimIdle: pending entries idle longer than this are re-claimed from dead consumers.
	ClaimIdle = 60 * time.Second
	field     = "e"
)

// StreamOptions configures the Redis Streams transport.
type StreamOptions struct {
	BufferSize    int           // local publish buffer (VISIT_BUFFER_SIZE)
	BatchSize     int           // XREADGROUP COUNT and publisher pipeline size (VISIT_BATCH_SIZE)
	FlushInterval time.Duration // XREADGROUP BLOCK and publisher flush period (VISIT_FLUSH_INTERVAL)
	Consumer      string        // consumer name; default hostname-pid-random
	Stream        string        // default StreamKey (tests)
}

// Stream is both publisher and consumer.
type Stream struct {
	rdb  *redis.Client
	opt  StreamOptions
	ch   chan domain.VisitEvent
	done chan struct{}
	stop chan struct{}
	once sync.Once

	closed          atomic.Bool
	dropped, pubd   atomic.Uint64
	publishFailures atomic.Uint64
}

// NewStream starts the background publisher flusher.
func NewStream(rdb *redis.Client, o StreamOptions) *Stream {
	if o.BufferSize <= 0 {
		o.BufferSize = 100_000
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 500
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = time.Second
	}
	if o.Stream == "" {
		o.Stream = StreamKey
	}
	if o.Consumer == "" {
		h, _ := os.Hostname()
		o.Consumer = fmt.Sprintf("%s-%d-%s", h, os.Getpid(), uuid.NewString()[:8])
	}
	s := &Stream{rdb: rdb, opt: o, ch: make(chan domain.VisitEvent, o.BufferSize), done: make(chan struct{}), stop: make(chan struct{})}
	go s.flusher()
	return s
}

// Publish enqueues locally without blocking; a background goroutine pipelines XADDs.
func (s *Stream) Publish(ev domain.VisitEvent) {
	if s.closed.Load() {
		s.dropped.Add(1)
		return
	}
	select {
	case s.ch <- ev:
		s.pubd.Add(1)
	default:
		s.dropped.Add(1)
	}
}

func (s *Stream) Dropped() uint64   { return s.dropped.Load() }
func (s *Stream) Published() uint64 { return s.pubd.Load() }

// Close stops publishing and flushes what's buffered (bounded by ctx).
func (s *Stream) Close(ctx context.Context) error {
	s.closed.Store(true)
	s.once.Do(func() { close(s.stop) })
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// flusher: collect up to BatchSize events (or whatever arrived within ~5ms) and send them
// in one pipeline. Short linger keeps visit latency low while amortizing round trips.
func (s *Stream) flusher() {
	defer close(s.done)
	batch := make([]domain.VisitEvent, 0, s.opt.BatchSize)
	linger := time.NewTimer(time.Hour)
	linger.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.send(batch)
		batch = batch[:0]
	}
	for {
		select {
		case <-s.stop:
			for {
				select {
				case ev := <-s.ch:
					batch = append(batch, ev)
					if len(batch) >= s.opt.BatchSize {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		case ev := <-s.ch:
			batch = append(batch, ev)
			if len(batch) == 1 {
				linger.Reset(5 * time.Millisecond)
			}
			if len(batch) >= s.opt.BatchSize {
				linger.Stop()
				flush()
			}
		case <-linger.C:
			flush()
		}
	}
}

func (s *Stream) send(batch []domain.VisitEvent) {
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pipe := s.rdb.Pipeline()
		for i := range batch {
			b, _ := json.Marshal(&batch[i])
			pipe.XAdd(ctx, &redis.XAddArgs{Stream: s.opt.Stream, MaxLen: StreamMaxLen, Approx: true, Values: []any{field, b}})
		}
		_, err := pipe.Exec(ctx)
		cancel()
		if err == nil {
			return
		}
		slog.Warn("events: XADD pipeline failed", "events", len(batch), "attempt", attempt+1, "err", err)
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	s.publishFailures.Add(uint64(len(batch)))
	s.dropped.Add(uint64(len(batch)))
}

// ensureGroup creates the stream + group (idempotent).
func (s *Stream) ensureGroup(ctx context.Context) error {
	err := s.rdb.XGroupCreateMkStream(ctx, s.opt.Stream, Group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// Run consumes batches from the group until ctx is done. Messages are XACKed only after
// handle returns nil; failed batches stay pending and are re-delivered via XAUTOCLAIM
// after ClaimIdle (on this or another replica). At startup this consumer first re-reads
// its own pending entries (ID "0").
func (s *Stream) Run(ctx context.Context, handle func(context.Context, []domain.VisitEvent) error) error {
	for {
		if err := s.ensureGroup(ctx); err == nil {
			break
		} else if ctx.Err() != nil {
			return nil
		} else {
			slog.Warn("events: XGROUP CREATE failed; retrying", "err", err)
			sleepCtx(ctx, time.Second)
		}
	}
	// 1. Own pending entries from a previous run with the same consumer name.
	s.readLoop(ctx, "0", handle, true)

	lastClaim := time.Time{}
	for ctx.Err() == nil {
		if time.Since(lastClaim) > ClaimIdle/2 {
			lastClaim = time.Now()
			s.autoclaim(ctx, handle)
		}
		if err := s.readOnce(ctx, ">", handle); err != nil && ctx.Err() == nil {
			if errors.Is(err, errNoGroup) {
				s.ensureGroup(ctx)
				continue
			}
			slog.Warn("events: XREADGROUP failed", "err", err)
			sleepCtx(ctx, time.Second)
		}
	}
	return nil
}

var errNoGroup = errors.New("NOGROUP")

func (s *Stream) readLoop(ctx context.Context, id string, handle func(context.Context, []domain.VisitEvent) error, pending bool) {
	for ctx.Err() == nil {
		res, err := s.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: Group, Consumer: s.opt.Consumer, Streams: []string{s.opt.Stream, id}, Count: int64(s.opt.BatchSize),
		}).Result()
		if err != nil || len(res) == 0 || len(res[0].Messages) == 0 {
			return
		}
		msgs := res[0].Messages
		if err := s.process(ctx, msgs, handle); err != nil {
			return // leave pending; autoclaim will retry later
		}
		id = msgs[len(msgs)-1].ID
	}
}

func (s *Stream) readOnce(ctx context.Context, id string, handle func(context.Context, []domain.VisitEvent) error) error {
	res, err := s.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: Group, Consumer: s.opt.Consumer, Streams: []string{s.opt.Stream, id},
		Count: int64(s.opt.BatchSize), Block: s.opt.FlushInterval,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		if strings.Contains(err.Error(), "NOGROUP") {
			return errNoGroup
		}
		return err
	}
	for _, st := range res {
		if len(st.Messages) > 0 {
			s.process(ctx, st.Messages, handle)
		}
	}
	return nil
}

func (s *Stream) autoclaim(ctx context.Context, handle func(context.Context, []domain.VisitEvent) error) {
	start := "0-0"
	for i := 0; i < 100 && ctx.Err() == nil; i++ {
		msgs, next, err := s.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream: s.opt.Stream, Group: Group, Consumer: s.opt.Consumer,
			MinIdle: ClaimIdle, Start: start, Count: int64(s.opt.BatchSize),
		}).Result()
		if err != nil {
			return
		}
		if len(msgs) > 0 {
			if s.process(ctx, msgs, handle) != nil {
				return
			}
		}
		if next == "0-0" || next == "" {
			return
		}
		start = next
	}
}

// process decodes, handles, and acks. Undecodable messages are acked and dropped.
func (s *Stream) process(ctx context.Context, msgs []redis.XMessage, handle func(context.Context, []domain.VisitEvent) error) error {
	evs := make([]domain.VisitEvent, 0, len(msgs))
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
		raw, _ := m.Values[field].(string)
		var ev domain.VisitEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.LinkID == 0 {
			slog.Warn("events: dropping malformed stream entry", "id", m.ID)
			continue
		}
		evs = append(evs, ev)
	}
	if len(evs) > 0 {
		// Even when the consumer ctx is cancelled mid-batch, finish this batch.
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := handle(hctx, evs)
		cancel()
		if err != nil {
			slog.Warn("events: handler failed; batch stays pending", "events", len(evs), "err", err)
			return err
		}
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.rdb.XAck(actx, s.opt.Stream, Group, ids...).Err()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
