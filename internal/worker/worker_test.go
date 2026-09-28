package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/events"
	"github.com/Hulul-Group/Tusk/internal/store"
	"github.com/Hulul-Group/Tusk/internal/store/sqlstore"
)

func uid(v int64) *int64 { return &v }

func TestFold(t *testing.T) {
	h := time.Date(2026, 9, 28, 10, 15, 0, 0, time.UTC)
	batch := []domain.VisitEvent{
		{LinkID: 1, UserID: uid(9), Browser: "chrome", OS: "windows", Country: "US", Referrer: "google.com", Timestamp: h},
		{LinkID: 1, UserID: uid(9), Browser: "Firefox", OS: "linux", Country: "", Referrer: "", Timestamp: h.Add(40 * time.Minute)},
		{LinkID: 1, UserID: uid(9), Browser: "weird", OS: "beos", Country: "de", Referrer: "News.YCombinator.com", Timestamp: h.Add(50 * time.Minute)},
		{LinkID: 2, UserID: uid(9), Browser: "safari", OS: "ios", Timestamp: h},
		{LinkID: 3, UserID: nil, Browser: "chrome", Timestamp: h}, // anonymous: Kutt doesn't track
	}
	d := Fold(batch)
	if len(d) != 3 {
		t.Fatalf("%d deltas", len(d))
	}
	a := d[0]
	if a.LinkID != 1 || a.Total != 2 || !a.Hour.Equal(h.Truncate(time.Hour)) || a.Browser["chrome"] != 1 || a.Browser["firefox"] != 1 ||
		a.Countries["us"] != 1 || a.Countries["unknown"] != 1 || a.Referrers["google[dot]com"] != 1 || a.Referrers["direct"] != 1 {
		t.Fatalf("%+v", a)
	}
	b := d[1]
	if b.Browser["other"] != 1 || b.OS["other"] != 1 || b.Referrers["news[dot]ycombinator[dot]com"] != 1 || b.Countries["de"] != 1 {
		t.Fatalf("%+v", b)
	}
	if d[2].LinkID != 2 || d[2].Browser["safari"] != 1 {
		t.Fatalf("%+v", d[2])
	}
}

type fakeInval struct {
	mu   sync.Mutex
	keys []string
}

func (f *fakeInval) Invalidate(_ context.Context, a string, d *int64) error {
	f.mu.Lock()
	f.keys = append(f.keys, fmt.Sprint(a, d != nil))
	f.mu.Unlock()
	return nil
}

func TestWorkerEndToEndSQLite(t *testing.T) {
	ctx := context.Background()
	db, err := sqlstore.OpenDSN(ctx, "sqlite", filepath.Join(t.TempDir(), "w.sqlite"), 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, _ := db.Users().Create(ctx, store.CreateUser{Email: "w@x", Password: "x"})
	l, _ := db.Links().Create(ctx, store.CreateLink{Address: "w", Target: "https://t", UserID: &u.ID})
	past := time.Now().Add(-time.Hour)
	db.Links().Create(ctx, store.CreateLink{Address: "old", Target: "https://t", UserID: &u.ID, ExpireIn: &past})

	ch := events.NewChannel(1000, 100, 20*time.Millisecond)
	inv := &fakeInval{}
	w := New(db, ch, inv, &config.Config{ExpiredLinkSweep: 50 * time.Millisecond})
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error)
	go func() { done <- w.Run(rctx) }()
	for i := 0; i < 250; i++ {
		ch.Publish(domain.VisitEvent{V: 1, LinkID: l.ID, UserID: &u.ID, Browser: "chrome", OS: "macos", Country: "FR", Timestamp: time.Now().UTC()})
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _ := db.Links().Find(ctx, store.LinkFilter{ID: &l.ID})
	if got.VisitCount != 250 {
		t.Fatalf("visit_count %d", got.VisitCount)
	}
	st, _ := db.Visits().Stats(ctx, l.ID, time.Now())
	if st.LastDay.Total != 250 || st.LastDay.Stats.Country[0].Name != "fr" || st.LastDay.Stats.OS[0].Name != "macos" {
		t.Fatalf("%+v", st.LastDay)
	}
	if w.Swept.Load() != 1 || len(inv.keys) != 1 || inv.keys[0] != "oldfalse" {
		t.Fatalf("sweep %d %v", w.Swept.Load(), inv.keys)
	}
}

func BenchmarkFold(b *testing.B) {
	batch := make([]domain.VisitEvent, 500)
	now := time.Now().UTC()
	br := []string{"chrome", "firefox", "safari", "edge"}
	for i := range batch {
		batch[i] = domain.VisitEvent{V: 1, LinkID: int64(i % 50), UserID: uid(1), Browser: br[i%4], OS: "windows",
			Country: "US", Referrer: "google.com", Timestamp: now}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Fold(batch)
	}
}
