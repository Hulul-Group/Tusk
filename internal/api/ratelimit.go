package api

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Hulul-Group/Tusk/internal/store"
)

// ---------- rate limiting (helpers.handler.js:88-124) ----------

// rateLimiter is a fixed-window counter like express-rate-limit's MemoryStore
// / rate-limit-redis. Keys match Kutt ("rl:" + method + baseUrl + path + ":" + ip);
// in Redis they get rate-limit-redis's default "rl:" prefix, so a Kutt and a
// Tusk instance behind the same Redis share counters during cut-over.
type rateLimiter struct {
	enabled bool
	rdb     *redis.Client
	now     func() time.Time

	mu      sync.Mutex
	windows map[string]*rlWindow
	lastGC  time.Time
}

type rlWindow struct {
	count int
	reset time.Time
}

func newRateLimiter(enabled bool, rdb *redis.Client, now func() time.Time) *rateLimiter {
	return &rateLimiter{enabled: enabled, rdb: rdb, now: now, windows: map[string]*rlWindow{}}
}

// incrScript: rate-limit-redis 4.x increment script (INCR, set PEXPIRE on the
// first hit or if the key lost its TTL; return count and ttl).
var incrScript = redis.NewScript(`
local totalHits = redis.call("INCR", KEYS[1])
local timeToExpire = redis.call("PTTL", KEYS[1])
if timeToExpire <= 0 or ARGV[1] == "1" then
  redis.call("PEXPIRE", KEYS[1], tonumber(ARGV[2]))
  timeToExpire = tonumber(ARGV[2])
end
return { totalHits, timeToExpire }`)

// allow increments the counter; returns (allowed, remaining, resetAt).
func (l *rateLimiter) allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, time.Time) {
	now := l.now()
	if l.rdb != nil {
		res, err := incrScript.Run(ctx, l.rdb, []string{"rl:" + key}, "0", strconv.FormatInt(window.Milliseconds(), 10)).Int64Slice()
		if err == nil && len(res) == 2 {
			hits := int(res[0])
			reset := now.Add(time.Duration(res[1]) * time.Millisecond)
			return hits <= limit, max(limit-hits, 0), reset
		}
		// Redis unavailable: fall back to the local window (fail open-ish, still bounded).
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastGC) > time.Minute {
		for k, w := range l.windows {
			if now.After(w.reset) {
				delete(l.windows, k)
			}
		}
		l.lastGC = now
	}
	w := l.windows[key]
	if w == nil || !now.Before(w.reset) {
		w = &rlWindow{reset: now.Add(window)}
		l.windows[key] = w
	}
	w.count++
	return w.count <= limit, max(limit-w.count, 0), w.reset
}

// check applies a Kutt rateLimit({window, limit}) at the point in the chain
// where Kutt mounts it (after validation). routeKey is baseUrl+path, e.g.
// "/api/v2/auth/login".
func (s *Server) rateLimit(w http.ResponseWriter, r *http.Request, windowSec, limit int) error {
	if !s.rl.enabled {
		return nil
	}
	key := r.Method + r.URL.Path + ":" + s.o.IPs.IP(r)
	ok, remaining, reset := s.rl.allow(r.Context(), key, limit, time.Duration(windowSec)*time.Second)
	h := w.Header()
	// express-rate-limit 7 legacyHeaders (default on).
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt((reset.UnixMilli()+999)/1000, 10))
	if !ok {
		secs := int(time.Until(reset).Seconds() + 0.999)
		if secs < 0 {
			secs = 0
		}
		h.Set("Retry-After", strconv.Itoa(secs))
		if s.o.Metrics != nil {
			s.o.Metrics.RateLimited.WithLabelValues(r.Pattern).Inc()
		}
		return errBody(http.StatusTooManyRequests, "Too many requests, please try again later.")
	}
	return nil
}

// ---------- stats cache (visit.queries.js find: Redis "s:<link_id>", 60s) ----------

type statsCache struct {
	rdb *redis.Client
	now func() time.Time
	mu  sync.Mutex
	m   map[int64]statsEntry
}

type statsEntry struct {
	b   []byte
	exp time.Time
}

func newStatsCache(rdb *redis.Client, now func() time.Time) *statsCache {
	return &statsCache{rdb: rdb, now: now, m: map[int64]statsEntry{}}
}

const statsTTL = 60 * time.Second

func (c *statsCache) get(ctx context.Context, linkID int64) []byte {
	if c.rdb != nil {
		b, err := c.rdb.Get(ctx, "s:"+strconv.FormatInt(linkID, 10)).Bytes()
		if err == nil && len(b) > 2 {
			return b
		}
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[linkID]
	if ok && c.now().Before(e.exp) {
		return e.b
	}
	return nil
}

func (c *statsCache) put(ctx context.Context, linkID int64, b []byte) {
	if c.rdb != nil {
		c.rdb.Set(ctx, "s:"+strconv.FormatInt(linkID, 10), b, statsTTL)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 10000 {
		now := c.now()
		for k, e := range c.m {
			if now.After(e.exp) {
				delete(c.m, k)
			}
		}
	}
	c.m[linkID] = statsEntry{b: b, exp: c.now().Add(statsTTL)}
}

// statsJSON renders store.Stats in Kutt's key order
// (lastYear, lastDay, lastMonth, lastWeek, updatedAt: visit.queries.js:164-187).
func statsJSON(st *store.Stats) ([]byte, error) {
	period := func(p store.PeriodStats) obj {
		conv := func(a []store.NameValue) []obj {
			out := make([]obj, 0, len(a))
			for _, nv := range a {
				out = append(out, obj{{"name", nv.Name}, {"value", nv.Value}})
			}
			return out
		}
		views := p.Views
		if views == nil {
			views = []int64{}
		}
		return obj{
			{"stats", obj{
				{"browser", conv(p.Stats.Browser)},
				{"os", conv(p.Stats.OS)},
				{"country", conv(p.Stats.Country)},
				{"referrer", conv(p.Stats.Referrer)},
			}},
			{"views", views},
			{"total", p.Total},
		}
	}
	return marshal(obj{
		{"lastYear", period(st.LastYear)},
		{"lastDay", period(st.LastDay)},
		{"lastMonth", period(st.LastMonth)},
		{"lastWeek", period(st.LastWeek)},
		{"updatedAt", jsDate(st.UpdatedAt)},
	})
}
