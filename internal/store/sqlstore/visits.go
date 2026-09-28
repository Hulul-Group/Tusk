package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Hulul-Group/Tusk/internal/store"
)

type visitStore struct{ s *DB }

// Kutt column buckets (models/visit.model.js).
var (
	browserKeys = []string{"chrome", "edge", "firefox", "ie", "opera", "other", "safari"}
	osKeys      = []string{"android", "ios", "linux", "macos", "other", "windows"}
)

func bucketKey(k string, allowed []string) string {
	k = strings.ToLower(k)
	if slices.Contains(allowed, k) {
		return k
	}
	return "other"
}

// ApplyDeltas implements visit.queries.js add:7-68 for pre-aggregated buckets.
//
// Per delta, ONE transaction:
//  1. UPDATE links SET visit_count = visit_count + Total (queues/visit.js incrementVisit).
//     Done first: the row lock on the link serializes concurrent workers for the same link,
//     so two replicas can never both miss the hour row and insert duplicates.
//     If the link no longer exists the delta is dropped.
//  2. SELECT the visits row for (link_id, hour) FOR UPDATE (pg/mysql; sqlite uses an
//     IMMEDIATE transaction) — the same lock Kutt takes, so Node Kutt can run alongside.
//  3. UPDATE it (increment total/br_*/os_*, merge countries/referrers JSON, updated_at)
//     or INSERT a new bucket.
//
// Transient deadlock/serialization/busy errors are retried.
func (vs *visitStore) ApplyDeltas(ctx context.Context, deltas []store.VisitDelta) error {
	var errs []error
	for i := range deltas {
		d := &deltas[i]
		if d.Total <= 0 {
			continue
		}
		var err error
		for attempt := 0; attempt < 8; attempt++ {
			err = vs.applyOne(ctx, d)
			if !retryable(err) {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 15 * time.Millisecond):
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("visit delta link=%d hour=%s: %w", d.LinkID, d.Hour.Format(time.RFC3339), err))
		}
	}
	return errors.Join(errs...)
}

// hourMatch returns the predicate selecting the bucket for hour h, following
// utils/knex.js truncatedCreatedAtHour (commit 279b491).
func (s *DB) hourMatch(h time.Time) (string, []any) {
	h = h.UTC().Truncate(time.Hour)
	switch s.d {
	case dSQLite:
		// strftime('%Y-%m-%d %H:00:00', created_at) = 'YYYY-MM-DD HH:00:00' exactly like Kutt;
		// robust to any created_at text format sqlite understands.
		return "strftime('%Y-%m-%d %H:00:00', created_at) = ?", []any{h.Format("2006-01-02 15:04:05")}
	default:
		// pg: date_trunc('hour', created_at at time zone 'Z'); mysql: DATE_FORMAT(created_at,
		// '%Y-%m-%d %H:00:00'). A half-open range is equivalent and index/sargable.
		return "created_at >= ? AND created_at < ?", []any{s.ts(h), s.ts(h.Add(time.Hour))}
	}
}

func (vs *visitStore) applyOne(ctx context.Context, d *store.VisitDelta) error {
	s := vs.s
	now := time.Now().UTC()
	hour := d.Hour.UTC().Truncate(time.Hour)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.q("UPDATE links SET visit_count = visit_count + ? WHERE id = ?"), d.Total, d.LinkID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // link deleted since the visit; nothing to attribute it to
		}

		pred, pargs := s.hourMatch(hour)
		var (
			id         int64
			cRaw, rRaw []byte
		)
		err = tx.QueryRowContext(ctx, s.q("SELECT id, countries, referrers FROM visits WHERE link_id = ? AND "+pred+
			" ORDER BY id LIMIT 1"+s.forUpdate()), append([]any{d.LinkID}, pargs...)...).Scan(&id, &cRaw, &rRaw)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return vs.insertBucket(ctx, tx, d, hour, now)
		case err != nil:
			return err
		}

		countries, err := parseCounts(cRaw)
		if err != nil {
			return err
		}
		referrers, err := parseCounts(rRaw)
		if err != nil {
			return err
		}
		mergeCounts(&countries, d.Countries)
		mergeCounts(&referrers, d.Referrers)

		set := []string{"total = total + ?"}
		args := []any{d.Total}
		for _, col := range sumByColumn("br_", d.Browser, browserKeys) {
			set = append(set, col.name+" = "+col.name+" + ?")
			args = append(args, col.n)
		}
		for _, col := range sumByColumn("os_", d.OS, osKeys) {
			set = append(set, col.name+" = "+col.name+" + ?")
			args = append(args, col.n)
		}
		set = append(set, "countries = "+s.jsonParam(), "referrers = "+s.jsonParam(), "updated_at = ?")
		args = append(args, countries.json(), referrers.json(), s.ts(now), id)
		_, err = tx.ExecContext(ctx, s.q("UPDATE visits SET "+strings.Join(set, ", ")+" WHERE id = ?"), args...)
		return err
	})
}

func (s *DB) jsonParam() string {
	if s.d == dPostgres {
		return "CAST(? AS jsonb)"
	}
	return "?"
}

type colCount struct {
	name string
	n    int64
}

// sumByColumn folds a key->count map into column increments (unknown keys -> *_other),
// in a deterministic order.
func sumByColumn(prefix string, m map[string]int64, allowed []string) []colCount {
	if len(m) == 0 {
		return nil
	}
	sums := make(map[string]int64, len(m))
	for k, v := range m {
		if v != 0 {
			sums[bucketKey(k, allowed)] += v
		}
	}
	out := make([]colCount, 0, len(sums))
	for _, k := range allowed {
		if v := sums[k]; v != 0 {
			out = append(out, colCount{prefix + k, v})
		}
	}
	return out
}

func (vs *visitStore) insertBucket(ctx context.Context, tx *sql.Tx, d *store.VisitDelta, hour, now time.Time) error {
	s := vs.s
	// Kutt inserts with created_at = DB now() at processing time. Use now when it falls in
	// the bucket's hour (normal case, worker flushes within seconds); for late/replayed
	// events use the hour start so the row is found again by the hour predicate.
	created := now
	if created.Before(hour) || !created.Before(hour.Add(time.Hour)) {
		created = hour
	}
	var countries, referrers orderedCounts
	mergeCounts(&countries, d.Countries)
	mergeCounts(&referrers, d.Referrers)
	cols := []string{"link_id", "user_id", "total", "countries", "referrers", "created_at", "updated_at"}
	vals := []string{"?", "?", "?", s.jsonParam(), s.jsonParam(), "?", "?"}
	args := []any{d.LinkID, i64Ptr(d.UserID), d.Total, countries.json(), referrers.json(), s.ts(created), s.ts(now)}
	for _, c := range sumByColumn("br_", d.Browser, browserKeys) {
		cols, vals, args = append(cols, c.name), append(vals, "?"), append(args, c.n)
	}
	for _, c := range sumByColumn("os_", d.OS, osKeys) {
		cols, vals, args = append(cols, c.name), append(vals, "?"), append(args, c.n)
	}
	_, err := tx.ExecContext(ctx, s.q("INSERT INTO visits ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(vals, ", ")+")"), args...)
	return err
}

// ---------- ordered JSON counts (JS object semantics) ----------

// orderedCounts is a string->int64 map that iterates like a JS object: array-index keys
// first in ascending numeric order, then string keys in insertion order. Needed to
// reproduce statsObjectToArray's stable sort tie order.
type orderedCounts struct {
	keys []string
	vals map[string]int64
}

func (o *orderedCounts) add(k string, v int64) {
	if o.vals == nil {
		o.vals = make(map[string]int64)
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] += v
}

// ordered returns keys in JS property order.
func (o *orderedCounts) ordered() []string {
	var idx, str []string
	for _, k := range o.keys {
		if isArrayIndex(k) {
			idx = append(idx, k)
		} else {
			str = append(str, k)
		}
	}
	if len(idx) == 0 {
		return str
	}
	slices.SortFunc(idx, func(a, b string) int {
		x, _ := strconv.ParseUint(a, 10, 64)
		y, _ := strconv.ParseUint(b, 10, 64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	return append(idx, str...)
}

func isArrayIndex(k string) bool {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 1<<32-1
}

func (o *orderedCounts) json() string {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.ordered() {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(o.vals[k], 10))
	}
	b.WriteByte('}')
	return b.String()
}

// mergeCounts adds a delta map in sorted key order (deterministic output).
func mergeCounts(o *orderedCounts, m map[string]int64) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		o.add(k, m[k])
	}
}

// parseCounts reads a JSON object preserving key order. NULL/empty -> empty.
// Values are numbers (possibly float from JS); they are truncated to int64.
func parseCounts(raw []byte) (orderedCounts, error) {
	var o orderedCounts
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return o, nil
	}
	// A JSON string containing JSON (double-encoded by some drivers): unwrap once.
	if raw[0] == '"' {
		var inner string
		if err := json.Unmarshal(raw, &inner); err != nil {
			return o, err
		}
		raw = []byte(inner)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return o, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return o, fmt.Errorf("sqlstore: visits json is not an object: %q", raw)
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return o, err
		}
		k, _ := kt.(string)
		vt, err := dec.Token()
		if err != nil {
			return o, err
		}
		var v int64
		switch x := vt.(type) {
		case json.Number:
			if n, err := x.Int64(); err == nil {
				v = n
			} else if f, err := x.Float64(); err == nil {
				v = int64(f)
			}
		case string:
			v, _ = strconv.ParseInt(x, 10, 64)
		}
		o.add(k, v)
	}
	return o, nil
}

// ---------- Stats (visit.queries.js find:70-189) ----------

type periodAcc struct {
	views   []int64
	total   int64
	browser [7]int64
	os      [6]int64
	country orderedCounts
	ref     orderedCounts
}

// Stats replicates visit.queries.js find exactly, except that only rows newer than
// subMonths(now, 12) are fetched (older rows can't fall in any period).
func (vs *visitStore) Stats(ctx context.Context, linkID int64, now time.Time) (*store.Stats, error) {
	s := vs.s
	now = now.UTC()
	type period struct {
		from time.Time
		diff func(l, r time.Time) int
		acc  *periodAcc
	}
	// utils.js getStatsPeriods:149-156 + getDifferenceFunction:112-118.
	periods := []period{
		{now.Add(-24 * time.Hour), differenceInHours, &periodAcc{views: make([]int64, 24)}},
		{jsDate(now.Year(), now.Month(), now.Day()-7, now.Hour(), now.Minute(), now.Second(), now.Nanosecond()), differenceInDays, &periodAcc{views: make([]int64, 7)}},
		{jsDate(now.Year(), now.Month(), now.Day()-30, now.Hour(), now.Minute(), now.Second(), now.Nanosecond()), differenceInDays, &periodAcc{views: make([]int64, 30)}},
		{subMonths(now, 12), differenceInMonths, &periodAcc{views: make([]int64, 12)}},
	}
	// SQL pre-filter with a 1-day margin; the exact isAfter test runs in Go.
	cutoff := periods[3].from.Add(-24 * time.Hour)
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT created_at, total, countries, referrers,
 br_chrome, br_edge, br_firefox, br_ie, br_opera, br_other, br_safari,
 os_android, os_ios, os_linux, os_macos, os_other, os_windows
 FROM visits WHERE link_id = ? AND created_at > ? ORDER BY id`), linkID, s.ts(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			created    nullTime
			total      int64
			cRaw, rRaw []byte
			br         [7]int64
			os         [6]int64
		)
		if err := rows.Scan(&created, &total, &cRaw, &rRaw, &br[0], &br[1], &br[2], &br[3], &br[4], &br[5], &br[6],
			&os[0], &os[1], &os[2], &os[3], &os[4], &os[5]); err != nil {
			return nil, err
		}
		if !created.Valid {
			continue
		}
		countries, err := parseCounts(cRaw)
		if err != nil {
			return nil, err
		}
		referrers, err := parseCounts(rRaw)
		if err != nil {
			return nil, err
		}
		for _, p := range periods {
			if !created.T.After(p.from) { // isAfter(created_at, fromDate)
				continue
			}
			a := p.acc
			for i := range br {
				a.browser[i] += br[i]
			}
			for i := range os {
				a.os[i] += os[i]
			}
			for _, k := range countries.ordered() {
				a.country.add(k, countries.vals[k])
			}
			for _, k := range referrers.ordered() {
				a.ref.add(k, referrers.vals[k])
			}
			idx := len(a.views) - p.diff(now, created.T) - 1
			if idx >= 0 && idx < len(a.views) {
				a.views[idx] += total
			}
			a.total += total
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	conv := func(a *periodAcc) store.PeriodStats {
		return store.PeriodStats{
			Stats: store.StatBuckets{
				Browser:  toArray(browserKeys, a.browser[:]),
				OS:       toArray(osKeys, a.os[:]),
				Country:  countsToArray(&a.country),
				Referrer: countsToArray(&a.ref),
			},
			Views: a.views,
			Total: a.total,
		}
	}
	return &store.Stats{
		LastDay:   conv(periods[0].acc),
		LastWeek:  conv(periods[1].acc),
		LastMonth: conv(periods[2].acc),
		LastYear:  conv(periods[3].acc),
		UpdatedAt: now,
	}, nil
}

// statsObjectToArray (utils.js:95-110): Object.keys order, stable sort by value desc.
func toArray(keys []string, vals []int64) []store.NameValue {
	out := make([]store.NameValue, len(keys))
	for i, k := range keys {
		out[i] = store.NameValue{Name: k, Value: vals[i]}
	}
	slices.SortStableFunc(out, func(a, b store.NameValue) int { return cmpDesc(a.Value, b.Value) })
	return out
}

func countsToArray(o *orderedCounts) []store.NameValue {
	keys := o.ordered()
	out := make([]store.NameValue, len(keys))
	for i, k := range keys {
		out[i] = store.NameValue{Name: k, Value: o.vals[k]}
	}
	slices.SortStableFunc(out, func(a, b store.NameValue) int { return cmpDesc(a.Value, b.Value) })
	return out
}

func cmpDesc(a, b int64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}
