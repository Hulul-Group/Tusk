package events

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
)

func ev(id int64) domain.VisitEvent {
	return domain.VisitEvent{V: 1, LinkID: id, Browser: "chrome", OS: "linux", Timestamp: time.Now().UTC()}
}

func TestChannelBatchingAndDrain(t *testing.T) {
	c := NewChannel(100, 10, 50*time.Millisecond)
	var mu sync.Mutex
	var batches [][]domain.VisitEvent
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx, func(_ context.Context, b []domain.VisitEvent) error {
			mu.Lock()
			batches = append(batches, append([]domain.VisitEvent(nil), b...))
			mu.Unlock()
			return nil
		})
		close(done)
	}()
	for i := 1; i <= 25; i++ {
		c.Publish(ev(int64(i)))
	}
	time.Sleep(150 * time.Millisecond)
	for i := 26; i <= 30; i++ {
		c.Publish(ev(int64(i)))
	}
	c.Close(context.Background())
	c.Publish(ev(99)) // dropped after close
	cancel()
	<-done
	n := 0
	for _, b := range batches {
		if len(b) > 10 {
			t.Fatal("batch too big")
		}
		n += len(b)
	}
	if n != 30 || c.Dropped() != 1 {
		t.Fatalf("delivered %d dropped %d", n, c.Dropped())
	}
}

func TestChannelNonBlocking(t *testing.T) {
	c := NewChannel(2, 10, time.Second)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		c.Publish(ev(1))
	}
	if time.Since(start) > 50*time.Millisecond || c.Dropped() != 998 {
		t.Fatalf("dropped=%d", c.Dropped())
	}
}

func TestFactory(t *testing.T) {
	p, cns := New(&config.Config{}, nil)
	if _, ok := p.(*Channel); !ok || p != cns.(domain.VisitPublisher) {
		t.Fatal("expected channel transport")
	}
}

func rdb(t testing.TB) *redis.Client {
	addr := os.Getenv("TUSK_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TUSK_TEST_REDIS_ADDR not set")
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStreamEndToEnd(t *testing.T) {
	r := rdb(t)
	key := "tusk:test:visits:" + time.Now().Format("150405.000")
	defer r.Del(context.Background(), key)
	pub := NewStream(r, StreamOptions{BatchSize: 50, FlushInterval: 100 * time.Millisecond, Stream: key, Consumer: "p"})
	const n = 1000
	for i := 1; i <= n; i++ {
		pub.Publish(ev(int64(i)))
	}
	if err := pub.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Two competing consumers; the first fails its first batch (left pending).
	var got sync.Map
	var total atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var failedOnce atomic.Bool
	mk := func(name string, fail bool) {
		c := NewStream(r, StreamOptions{BatchSize: 50, FlushInterval: 100 * time.Millisecond, Stream: key, Consumer: name})
		go c.Run(ctx, func(_ context.Context, b []domain.VisitEvent) error {
			if fail && failedOnce.CompareAndSwap(false, true) {
				return errors.New("boom")
			}
			for _, e := range b {
				if _, dup := got.LoadOrStore(e.LinkID, true); !dup {
					total.Add(1)
				}
			}
			return nil
		})
	}
	mk("c1", true)
	mk("c2", false)
	deadline := time.Now().Add(5 * time.Second)
	for total.Load() < n-50 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if total.Load() != n-50 {
		t.Fatalf("got %d, want %d (one batch pending)", total.Load(), n-50)
	}
	pend, err := r.XPending(ctx, key, Group).Result()
	if err != nil || pend.Count != 50 {
		t.Fatalf("pending %+v %v", pend, err)
	}
	// Simulate idle > ClaimIdle by claiming with min-idle 0 through a fresh consumer.
	c3 := NewStream(r, StreamOptions{BatchSize: 50, Stream: key, Consumer: "c3"})
	msgs, _, err := r.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: key, Group: Group, Consumer: "c3", MinIdle: 0, Start: "0-0", Count: 100}).Result()
	if err != nil || len(msgs) != 50 {
		t.Fatal("claim", err, len(msgs))
	}
	if err := c3.process(ctx, msgs, func(_ context.Context, b []domain.VisitEvent) error {
		for _, e := range b {
			if _, dup := got.LoadOrStore(e.LinkID, true); !dup {
				total.Add(1)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if total.Load() != n {
		t.Fatal("after reclaim", total.Load())
	}
	if pend, _ := r.XPending(ctx, key, Group).Result(); pend.Count != 0 {
		t.Fatal("still pending", pend.Count)
	}
}

func BenchmarkChannelPublish(b *testing.B) {
	c := NewChannel(1<<20, 500, time.Second)
	e := ev(1)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Publish(e)
			select {
			case <-c.ch:
			default:
			}
		}
	})
}
