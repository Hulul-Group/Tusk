package cache

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

type fakeLinks struct {
	store.LinkStore // unused methods panic
	mu              sync.Mutex
	data            map[linkKey]*domain.RedirectTarget
	calls           atomic.Int64
	delay           time.Duration
}

func (f *fakeLinks) Resolve(_ context.Context, a string, d *int64) (*domain.RedirectTarget, error) {
	f.calls.Add(1)
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.data[linkKey{a, domID(d)}]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *t
	return &c, nil
}

func (f *fakeLinks) set(a string, d int64, target string) {
	f.mu.Lock()
	f.data[linkKey{a, d}] = &domain.RedirectTarget{LinkID: 1, Target: target}
	f.mu.Unlock()
}

type fakeDomains struct {
	store.DomainStore
	calls atomic.Int64
}

func (f *fakeDomains) Find(_ context.Context, fl store.DomainFilter) (*domain.Domain, error) {
	f.calls.Add(1)
	if *fl.Address == "custom.io" {
		return &domain.Domain{ID: 7, Address: "custom.io"}, nil
	}
	return nil, domain.ErrNotFound
}

func testCfg() *config.Config {
	return &config.Config{LocalCacheSize: 1000, LocalCacheTTL: time.Minute, RedisCacheTTL: time.Minute, DefaultDomain: "kutt.local:3000"}
}

func TestResolveLocal(t *testing.T) {
	fl := &fakeLinks{data: map[linkKey]*domain.RedirectTarget{}, delay: 20 * time.Millisecond}
	fl.set("abc", 0, "https://a")
	r := NewLinkResolver(fl, nil, testCfg())
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if t2, err := r.Resolve(ctx, "abc", nil); err != nil || t2.Target != "https://a" {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := fl.calls.Load(); n != 1 {
		t.Fatalf("singleflight: %d DB calls", n)
	}
	// custom domain is a different key
	if _, err := r.Resolve(ctx, "abc", ptr(int64(3))); err != domain.ErrNotFound {
		t.Fatal(err)
	}
	// negative cache
	before := fl.calls.Load()
	r.Resolve(ctx, "abc", ptr(int64(3)))
	if fl.calls.Load() != before {
		t.Fatal("negative not cached")
	}
	// invalidate
	fl.set("abc", 0, "https://b")
	r.Invalidate(ctx, "abc", nil)
	if t2, _ := r.Resolve(ctx, "abc", nil); t2.Target != "https://b" {
		t.Fatal("stale after invalidate")
	}
}

func TestNegativeExpiry(t *testing.T) {
	fl := &fakeLinks{data: map[linkKey]*domain.RedirectTarget{}}
	r := NewLinkResolver(fl, nil, testCfg())
	now := time.Now()
	r.local.now = func() time.Time { return now }
	r.Resolve(context.Background(), "x", nil)
	fl.set("x", 0, "https://x")
	now = now.Add(NegativeTTL + time.Millisecond)
	if t2, err := r.Resolve(context.Background(), "x", nil); err != nil || t2.Target != "https://x" {
		t.Fatal("negative entry did not expire", err)
	}
}

func TestLRUEviction(t *testing.T) {
	c := newLRU[string, int](4, 1, hashString)
	for i, k := range []string{"a", "b", "c", "d"} {
		c.Add(k, i, time.Minute)
	}
	c.Get("a")
	c.Add("e", 5, time.Minute)
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should be evicted")
	}
	if _, ok := c.Get("a"); !ok || c.Len() != 4 {
		t.Fatal("a should survive")
	}
}

func TestDomainResolver(t *testing.T) {
	fd := &fakeDomains{}
	d := NewDomainResolver(fd, nil, testCfg())
	ctx := context.Background()
	for _, h := range []string{"kutt.local:3000", "www.KUTT.local:3000", ""} {
		if dom, err := d.ResolveHost(ctx, h); dom != nil || err != nil {
			t.Fatalf("default %q", h)
		}
	}
	dom, _ := d.ResolveHost(ctx, "WWW.Custom.io")
	if dom == nil || dom.ID != 7 {
		t.Fatal("custom")
	}
	d.ResolveHost(ctx, "custom.io")
	if dom, _ := d.ResolveHost(ctx, "unknown.io"); dom != nil {
		t.Fatal("unknown host -> nil (Kutt uses default links)")
	}
	d.ResolveHost(ctx, "unknown.io")
	if fd.calls.Load() != 2 {
		t.Fatal("domain caching", fd.calls.Load())
	}
	if NormalizeHost("awww.x.com") != "ax.com" {
		t.Fatal("Kutt removeWww replaces first occurrence anywhere")
	}
}

func redisClient(t testing.TB) *redis.Client {
	addr := os.Getenv("TUSK_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TUSK_TEST_REDIS_ADDR not set")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return rdb
}

func TestRedisTwoNodes(t *testing.T) {
	rdb := redisClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rdb.Del(ctx, KeyPrefix+"l:0:rk")
	fl := &fakeLinks{data: map[linkKey]*domain.RedirectTarget{}}
	fl.set("rk", 0, "https://1")
	a := NewLinkResolver(fl, rdb, testCfg())
	b := NewLinkResolver(fl, rdb, testCfg())
	go a.Subscribe(ctx)
	go b.Subscribe(ctx)
	time.Sleep(200 * time.Millisecond)
	a.Resolve(ctx, "rk", nil)
	b.Resolve(ctx, "rk", nil) // served from Redis
	if fl.calls.Load() != 1 {
		t.Fatalf("redis tier not used: %d", fl.calls.Load())
	}
	if v, _ := rdb.Get(ctx, KeyPrefix+"l:0:rk").Result(); v == "" {
		t.Fatal("redis key missing")
	}
	fl.set("rk", 0, "https://2")
	if err := a.Invalidate(ctx, "rk", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if t2, _ := b.Resolve(ctx, "rk", nil); t2.Target == "https://2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("peer did not drop its local entry")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func ptr[T any](v T) *T { return &v }

func BenchmarkResolveHit(b *testing.B) {
	fl := &fakeLinks{data: map[linkKey]*domain.RedirectTarget{}}
	fl.set("abc123", 0, "https://example.com")
	r := NewLinkResolver(fl, nil, testCfg())
	ctx := context.Background()
	r.Resolve(ctx, "abc123", nil)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := r.Resolve(ctx, "abc123", nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestResolveHitNoAlloc(t *testing.T) {
	fl := &fakeLinks{data: map[linkKey]*domain.RedirectTarget{}}
	fl.set("abc123", 0, "https://example.com")
	r := NewLinkResolver(fl, nil, testCfg())
	ctx := context.Background()
	id := int64(0)
	r.Resolve(ctx, "abc123", nil)
	if n := testing.AllocsPerRun(1000, func() { r.Resolve(ctx, "abc123", nil) }); n != 0 {
		t.Fatalf("hit allocates %v", n)
	}
	_ = id
}
