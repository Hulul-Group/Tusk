// Package cache implements the redirect hot-path caches: a two-tier (in-process LRU +
// Redis) LinkResolver and DomainResolver with singleflight, negative caching and
// cross-node invalidation over Redis pub/sub.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"hash/maphash"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

const (
	// KeyPrefix keeps Tusk's keys apart from Kutt's (Kutt uses bare "l:", "d:" … keys).
	KeyPrefix = "tusk:"
	// InvalidationChannel carries "l:<domainID>:<address>" / "d:<address>" messages.
	InvalidationChannel = "tusk:inval"
	// NegativeTTL bounds how long a "not found" answer is cached locally.
	NegativeTTL = 5 * time.Second
)

// NewRedis returns a client for cfg, or nil,nil when REDIS_ENABLED=false.
func NewRedis(cfg *config.Config) (*redis.Client, error) {
	if !cfg.RedisEnabled {
		return nil, nil
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr(),
		Password:     cfg.RedisPassword,
		DB:           cfg.RedisDB,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolSize:     64,
		MinIdleConns: 4,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, err
	}
	return rdb, nil
}

type linkKey struct {
	address  string
	domainID int64 // 0 = default domain (Kutt ids start at 1)
}

func hashLinkKey(seed maphash.Seed, k linkKey) uint64 {
	return maphash.String(seed, k.address) ^ (uint64(k.domainID) * 0x9E3779B97F4A7C15)
}

func hashString(seed maphash.Seed, s string) uint64 { return maphash.String(seed, s) }

func domID(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func linkRedisKey(k linkKey) string {
	return KeyPrefix + "l:" + strconv.FormatInt(k.domainID, 10) + ":" + k.address
}

// LinkResolver implements domain.LinkResolver. Returned *RedirectTarget values are shared
// between callers and MUST be treated as read-only.
type LinkResolver struct {
	links    store.LinkStore
	rdb      *redis.Client
	local    *lru[linkKey, *domain.RedirectTarget] // nil value = cached "not found"
	localTTL time.Duration
	redisTTL time.Duration
	sf       singleflight.Group
	gen      atomic.Uint64 // bumped on every invalidation; stale loads aren't cached

	Hits, Misses atomic.Uint64
}

var _ domain.LinkResolver = (*LinkResolver)(nil)

// NewLinkResolver builds the resolver; rdb may be nil (local LRU only).
func NewLinkResolver(links store.LinkStore, rdb *redis.Client, cfg *config.Config) *LinkResolver {
	size := cfg.LocalCacheSize
	if size <= 0 {
		size = 100_000
	}
	return &LinkResolver{
		links:    links,
		rdb:      rdb,
		local:    newLRU[linkKey, *domain.RedirectTarget](size, 64, hashLinkKey),
		localTTL: nonZero(cfg.LocalCacheTTL, 30*time.Second),
		redisTTL: nonZero(cfg.RedisCacheTTL, 15*time.Minute),
	}
}

func nonZero(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Resolve: local LRU -> Redis -> DB (singleflight). A local hit does not allocate.
func (r *LinkResolver) Resolve(ctx context.Context, address string, domainID *int64) (*domain.RedirectTarget, error) {
	k := linkKey{address, domID(domainID)}
	if t, ok := r.local.Get(k); ok {
		r.Hits.Add(1)
		if t == nil {
			return nil, domain.ErrNotFound
		}
		return t, nil
	}
	r.Misses.Add(1)
	v, err, _ := r.sf.Do(linkRedisKey(k), func() (any, error) { return r.load(ctx, k, domainID) })
	if err != nil {
		return nil, err
	}
	t := v.(*domain.RedirectTarget)
	if t == nil {
		return nil, domain.ErrNotFound
	}
	return t, nil
}

func (r *LinkResolver) load(ctx context.Context, k linkKey, domainID *int64) (*domain.RedirectTarget, error) {
	gen := r.gen.Load()
	rkey := linkRedisKey(k)
	if r.rdb != nil {
		b, err := r.rdb.Get(ctx, rkey).Bytes()
		if err == nil {
			var t domain.RedirectTarget
			if json.Unmarshal(b, &t) == nil {
				r.storeLocal(gen, k, &t, r.localTTL)
				return &t, nil
			}
		} else if !errors.Is(err, redis.Nil) {
			slog.Warn("cache: redis get failed; falling back to DB", "err", err)
		}
	}
	t, err := r.links.Resolve(ctx, k.address, domainID)
	if errors.Is(err, domain.ErrNotFound) {
		r.storeLocal(gen, k, nil, NegativeTTL)
		return (*domain.RedirectTarget)(nil), nil
	}
	if err != nil {
		return nil, err
	}
	r.storeLocal(gen, k, t, r.localTTL)
	if r.rdb != nil && r.gen.Load() == gen {
		if b, err := json.Marshal(t); err == nil {
			if err := r.rdb.Set(ctx, rkey, b, r.redisTTL).Err(); err != nil {
				slog.Warn("cache: redis set failed", "err", err)
			}
		}
	}
	return t, nil
}

func (r *LinkResolver) storeLocal(gen uint64, k linkKey, t *domain.RedirectTarget, ttl time.Duration) {
	if r.gen.Load() != gen {
		return // an invalidation raced with this load; don't cache a possibly stale row
	}
	r.local.Add(k, t, ttl)
}

// Invalidate drops the entry locally, in Redis, and on peers (PUBLISH tusk:inval).
func (r *LinkResolver) Invalidate(ctx context.Context, address string, domainID *int64) error {
	k := linkKey{address, domID(domainID)}
	r.gen.Add(1)
	r.local.Remove(k)
	r.sf.Forget(linkRedisKey(k))
	if r.rdb == nil {
		return nil
	}
	rkey := linkRedisKey(k)
	pipe := r.rdb.Pipeline()
	pipe.Del(ctx, rkey)
	pipe.Publish(ctx, InvalidationChannel, strings.TrimPrefix(rkey, KeyPrefix))
	_, err := pipe.Exec(ctx)
	return err
}

// dropLocal handles a peer message "l:<dom>:<address>".
func (r *LinkResolver) dropLocal(msg string) bool {
	rest, ok := strings.CutPrefix(msg, "l:")
	if !ok {
		return false
	}
	ds, addr, ok := strings.Cut(rest, ":")
	if !ok {
		return false
	}
	id, err := strconv.ParseInt(ds, 10, 64)
	if err != nil {
		return false
	}
	r.gen.Add(1)
	r.local.Remove(linkKey{addr, id})
	return true
}

// ---------- domains ----------

// DomainResolver implements domain.DomainResolver (links.handler.js redirect:467-471):
// host = removeWww(Host); host == DEFAULT_DOMAIN -> nil; else domain.find({address: host}).
// An unknown host also yields nil (Kutt then looks the link up with domain_id NULL).
type DomainResolver struct {
	domains       store.DomainStore
	rdb           *redis.Client
	defaultDomain string
	local         *lru[string, *domain.Domain]
	localTTL      time.Duration
	redisTTL      time.Duration
	sf            singleflight.Group
	gen           atomic.Uint64
}

var _ domain.DomainResolver = (*DomainResolver)(nil)

func NewDomainResolver(domains store.DomainStore, rdb *redis.Client, cfg *config.Config) *DomainResolver {
	return &DomainResolver{
		domains:       domains,
		rdb:           rdb,
		defaultDomain: strings.ToLower(removeWww(cfg.DefaultDomain)),
		local:         newLRU[string, *domain.Domain](10_000, 16, hashString),
		localTTL:      nonZero(cfg.LocalCacheTTL, 30*time.Second),
		redisTTL:      nonZero(cfg.RedisCacheTTL, 15*time.Minute),
	}
}

// removeWww reproduces utils.js:352 `host?.replace("www.", "")` — the FIRST occurrence
// anywhere in the string, not only a prefix.
func removeWww(host string) string { return strings.Replace(host, "www.", "", 1) }

// NormalizeHost lower-cases and applies Kutt's removeWww.
func NormalizeHost(host string) string { return removeWww(asciiLower(host)) }

func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}

// IsDefault reports whether host is the default domain.
func (d *DomainResolver) IsDefault(host string) bool { return NormalizeHost(host) == d.defaultDomain }

func (d *DomainResolver) ResolveHost(ctx context.Context, host string) (*domain.Domain, error) {
	h := NormalizeHost(host)
	if h == "" || h == d.defaultDomain {
		return nil, nil
	}
	if dom, ok := d.local.Get(h); ok {
		return dom, nil
	}
	v, err, _ := d.sf.Do(h, func() (any, error) { return d.load(ctx, h) })
	if err != nil {
		return nil, err
	}
	return v.(*domain.Domain), nil
}

func (d *DomainResolver) load(ctx context.Context, h string) (*domain.Domain, error) {
	gen := d.gen.Load()
	rkey := KeyPrefix + "d:" + h
	if d.rdb != nil {
		if b, err := d.rdb.Get(ctx, rkey).Bytes(); err == nil {
			var dom domain.Domain
			if json.Unmarshal(b, &dom) == nil {
				if d.gen.Load() == gen {
					d.local.Add(h, &dom, d.localTTL)
				}
				return &dom, nil
			}
		}
	}
	dom, err := d.domains.Find(ctx, store.DomainFilter{Address: &h})
	if errors.Is(err, domain.ErrNotFound) {
		if d.gen.Load() == gen {
			d.local.Add(h, nil, NegativeTTL)
		}
		return (*domain.Domain)(nil), nil
	}
	if err != nil {
		return nil, err
	}
	if d.gen.Load() == gen {
		d.local.Add(h, dom, d.localTTL)
		if d.rdb != nil {
			if b, err := json.Marshal(dom); err == nil {
				d.rdb.Set(ctx, rkey, b, d.redisTTL)
			}
		}
	}
	return dom, nil
}

// Invalidate drops a domain everywhere (call after domain add/update/remove/ban).
func (d *DomainResolver) Invalidate(ctx context.Context, address string) error {
	h := NormalizeHost(address)
	d.gen.Add(1)
	d.local.Remove(h)
	d.sf.Forget(h)
	if d.rdb == nil {
		return nil
	}
	pipe := d.rdb.Pipeline()
	pipe.Del(ctx, KeyPrefix+"d:"+h)
	pipe.Publish(ctx, InvalidationChannel, "d:"+h)
	_, err := pipe.Exec(ctx)
	return err
}

func (d *DomainResolver) dropLocal(msg string) bool {
	h, ok := strings.CutPrefix(msg, "d:")
	if !ok {
		return false
	}
	d.gen.Add(1)
	d.local.Remove(h)
	return true
}

// ---------- pub/sub ----------

// Subscribe listens on tusk:inval and drops local entries named by peers until ctx is
// done. Either resolver may be nil. Returns immediately when rdb is nil. On reconnect the
// local caches are purged (messages may have been missed).
func Subscribe(ctx context.Context, rdb *redis.Client, links *LinkResolver, domains *DomainResolver) {
	if rdb == nil {
		return
	}
	for ctx.Err() == nil {
		sub := rdb.Subscribe(ctx, InvalidationChannel)
		if _, err := sub.Receive(ctx); err != nil {
			sub.Close()
			if ctx.Err() != nil {
				return
			}
			slog.Warn("cache: subscribe failed; retrying", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		// Anything cached before (re)subscribing could have missed an invalidation.
		if links != nil {
			links.gen.Add(1)
			links.local.Purge()
		}
		if domains != nil {
			domains.gen.Add(1)
			domains.local.Purge()
		}
		ch := sub.Channel(redis.WithChannelHealthCheckInterval(30 * time.Second))
		for loop := true; loop; {
			select {
			case <-ctx.Done():
				sub.Close()
				return
			case m, ok := <-ch:
				if !ok {
					loop = false
					break
				}
				if links != nil && links.dropLocal(m.Payload) {
					continue
				}
				if domains != nil {
					domains.dropLocal(m.Payload)
				}
			}
		}
		sub.Close()
	}
}

// Subscribe is a convenience wrapper for the link resolver alone.
func (r *LinkResolver) Subscribe(ctx context.Context) { Subscribe(ctx, r.rdb, r, nil) }
