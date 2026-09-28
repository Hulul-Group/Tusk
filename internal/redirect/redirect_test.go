package redirect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/httpx"
	"github.com/Hulul-Group/Tusk/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// ---------- fakes ----------

type key struct {
	addr string
	dom  int64
}

type fakeLinks struct {
	m     map[key]*domain.RedirectTarget
	calls int
}

func (f *fakeLinks) Resolve(_ context.Context, a string, d *int64) (*domain.RedirectTarget, error) {
	f.calls++
	k := key{addr: a}
	if d != nil {
		k.dom = *d
	}
	if t, ok := f.m[k]; ok {
		return t, nil
	}
	return nil, domain.ErrNotFound
}
func (f *fakeLinks) Invalidate(context.Context, string, *int64) error { return nil }

type fakeDomains map[string]*domain.Domain

func (f fakeDomains) ResolveHost(_ context.Context, h string) (*domain.Domain, error) {
	if d, ok := f[h]; ok {
		return d, nil
	}
	return nil, domain.ErrNotFound
}

type fakeFinder map[string]*domain.Link // by uuid

func (f fakeFinder) Find(_ context.Context, fl store.LinkFilter) (*domain.Link, error) {
	for _, l := range f {
		if fl.UUID != nil && l.UUID == *fl.UUID {
			return l, nil
		}
		if fl.ID != nil && l.ID == *fl.ID {
			return l, nil
		}
	}
	return nil, domain.ErrNotFound
}

type fakePub struct {
	mu  sync.Mutex
	evs []domain.VisitEvent
}

func (p *fakePub) Publish(ev domain.VisitEvent) {
	p.mu.Lock()
	p.evs = append(p.evs, ev)
	p.mu.Unlock()
}
func (p *fakePub) Close(context.Context) error { return nil }

type fakeGeo struct{}

func (fakeGeo) Country(h domain.HeaderGetter, ip string) string {
	if c := h.Get("CF-IPCountry"); c != "" {
		return c
	}
	if ip == "8.8.8.8" {
		return "US"
	}
	return ""
}
func (fakeGeo) Name() string { return "fake" }

type fakeIPs struct{}

func (fakeIPs) IP(r *http.Request) string                      { return httpx.RemoteIP{TrustProxy: true}.IP(r) }
func (fakeIPs) GeoHeaders(r *http.Request) domain.HeaderGetter { return r.Header }

func ptr[T any](v T) *T { return &v }

const (
	uuidPlain     = "11111111-1111-1111-1111-111111111111"
	uuidProtected = "22222222-2222-2222-2222-222222222222"
)

var fixedNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func setup(t testing.TB) (*Handler, *fakePub, *http.ServeMux) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), 4)
	links := &fakeLinks{m: map[key]*domain.RedirectTarget{
		{addr: "abc"}:          {LinkID: 1, UUID: uuidPlain, UserID: ptr[int64](7), Target: "https://example.com/path?q=1"},
		{addr: "anon"}:         {LinkID: 2, UUID: "u2", Target: "https://anon.example/"},
		{addr: "bad"}:          {LinkID: 3, UUID: "u3", UserID: ptr[int64](7), Target: "https://evil.example/", Banned: true},
		{addr: "old"}:          {LinkID: 4, UUID: "u4", UserID: ptr[int64](7), Target: "https://old.example/", ExpireIn: ptr(fixedNow.Add(-time.Minute))},
		{addr: "fresh"}:        {LinkID: 5, UUID: "u5", UserID: ptr[int64](7), Target: "https://fresh.example/", ExpireIn: ptr(fixedNow.Add(time.Hour))},
		{addr: "pw"}:           {LinkID: 6, UUID: uuidProtected, UserID: ptr[int64](7), Target: "https://secret.example/", HasPassword: true},
		{addr: "uni"}:          {LinkID: 8, UUID: "u8", Target: "https://example.com/ü path"},
		{addr: "abc", dom: 10}: {LinkID: 9, UUID: "u9", UserID: ptr[int64](8), Target: "https://custom-target.example/"},
	}}
	doms := fakeDomains{
		"go.custom.io":   {ID: 10, Address: "go.custom.io", Homepage: ptr("https://custom.io/")},
		"bare.custom.io": {ID: 11, Address: "bare.custom.io"},
	}
	finder := fakeFinder{
		uuidPlain:     {ID: 1, UUID: uuidPlain, Target: "https://example.com/path?q=1", UserID: ptr[int64](7)},
		uuidProtected: {ID: 6, UUID: uuidProtected, Target: "https://secret.example/", UserID: ptr[int64](7), Password: ptr(string(hash))},
	}
	pub := &fakePub{}
	h := New(Options{
		DefaultDomain: "kutt.test",
		Links:         links,
		Domains:       doms,
		Finder:        finder,
		Publisher:     pub,
		Geo:           fakeGeo{},
		IPs:           fakeIPs{},
		IsBot:         func(ua string) bool { return strings.Contains(strings.ToLower(ua), "bot") },
		ParseUA: func(ua string) (string, string) {
			if strings.Contains(ua, "Firefox") {
				return "firefox", "linux"
			}
			return "other", "other"
		},
		Metrics: httpx.NewMetrics("test"),
		Now:     func() time.Time { return fixedNow },
	})
	mux := http.NewServeMux()
	h.Register(mux, true)
	return h, pub, mux
}

func do(t testing.TB, hnd http.Handler, method, host, path string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	hnd.ServeHTTP(rr, req)
	return rr
}

const firefoxUA = "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0"

func TestRedirectSemantics(t *testing.T) {
	h, pub, mux := setup(t)
	root := h.CustomDomainHomepage(mux)
	cases := []struct {
		name, host, path string
		hdr              map[string]string
		status           int
		location         string
		bodyHas          string
	}{
		{"plain 302", "kutt.test", "/abc", nil, 302, "https://example.com/path?q=1", ""},
		{"www stripped", "www.kutt.test", "/abc", nil, 302, "https://example.com/path?q=1", ""},
		{"unknown -> /404", "kutt.test", "/nope", nil, 302, "/404", ""},
		{"banned -> /banned", "kutt.test", "/bad", nil, 302, "/banned", ""},
		{"expired -> /404", "kutt.test", "/old", nil, 302, "/404", ""},
		{"not yet expired", "kutt.test", "/fresh", nil, 302, "https://fresh.example/", ""},
		{"protected page", "kutt.test", "/pw", map[string]string{"Accept": "text/html"}, 200, "", uuidProtected},
		{"protected basic ok", "kutt.test", "/pw", map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("x:secret"))}, 302, "https://secret.example/", ""},
		{"protected basic bad", "kutt.test", "/pw", map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("x:nope"))}, 200, "", "Protected link"},
		{"info json", "kutt.test", "/abc+", nil, 200, "", `{"target":"https://example.com/path?q=1"}`},
		{"info html", "kutt.test", "/abc+", map[string]string{"Accept": "text/html"}, 200, "", "kutt.test/abc"},
		{"info on protected -> page", "kutt.test", "/pw+", nil, 200, "", "Protected link"},
		{"preserved -> 404 page", "kutt.test", "/login", nil, 404, "", "404"},
		{"unicode encoded", "kutt.test", "/uni", nil, 302, "https://example.com/%C3%BC%20path", ""},
		{"custom domain link", "go.custom.io", "/abc", nil, 302, "https://custom-target.example/", ""},
		{"custom domain miss -> homepage", "go.custom.io", "/zzz", nil, 302, "https://custom.io/", ""},
		{"custom domain root -> homepage", "go.custom.io", "/", nil, 302, "https://custom.io/", ""},
		{"custom domain preserved -> homepage", "go.custom.io", "/login", nil, 302, "https://custom.io/", ""},
		{"custom domain w/o homepage miss", "bare.custom.io", "/zzz", nil, 302, "/404", ""},
		{"unknown host treated as default-less", "other.io", "/abc", nil, 302, "https://example.com/path?q=1", ""},
		{"GET /404 page", "kutt.test", "/404", nil, 404, "", "Link could not be found"},
		{"GET /banned page", "kutt.test", "/banned", nil, 200, "", "banned"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := do(t, root, "GET", c.host, c.path, c.hdr, "")
			if rr.Code != c.status {
				t.Fatalf("status %d want %d body=%s", rr.Code, c.status, rr.Body.String())
			}
			if c.location != "" && rr.Header().Get("Location") != c.location {
				t.Fatalf("Location %q want %q", rr.Header().Get("Location"), c.location)
			}
			if c.bodyHas != "" && !strings.Contains(rr.Body.String(), c.bodyHas) {
				t.Fatalf("body missing %q: %s", c.bodyHas, rr.Body.String())
			}
		})
	}
	_ = pub
}

func TestUnknownHostIsNotCustomDomain(t *testing.T) {
	// Kutt: domain = query.domain.find({address: host}) -> undefined -> domain_id null.
	_, _, mux := setup(t)
	rr := do(t, mux, "GET", "unknown.example", "/abc", nil, "")
	if rr.Code != 302 || rr.Header().Get("Location") != "https://example.com/path?q=1" {
		t.Fatalf("%d %s", rr.Code, rr.Header().Get("Location"))
	}
}

func TestVisitPublishing(t *testing.T) {
	_, pub, mux := setup(t)
	// bot: no event
	do(t, mux, "GET", "kutt.test", "/abc", map[string]string{"User-Agent": "Googlebot/2.1"}, "")
	// anonymous link: no event
	do(t, mux, "GET", "kutt.test", "/anon", map[string]string{"User-Agent": firefoxUA}, "")
	// banned / protected / missing: no event
	do(t, mux, "GET", "kutt.test", "/bad", map[string]string{"User-Agent": firefoxUA}, "")
	do(t, mux, "GET", "kutt.test", "/pw", map[string]string{"User-Agent": firefoxUA}, "")
	do(t, mux, "GET", "kutt.test", "/abc+", map[string]string{"User-Agent": firefoxUA}, "")
	if len(pub.evs) != 0 {
		t.Fatalf("unexpected events: %+v", pub.evs)
	}
	// human on owned link: one event with parsed fields
	do(t, mux, "GET", "kutt.test", "/abc", map[string]string{
		"User-Agent": firefoxUA, "Referer": "https://www.News.example.com:8443/a?b", "CF-IPCountry": "DE",
	}, "")
	do(t, mux, "GET", "kutt.test", "/abc", map[string]string{"User-Agent": firefoxUA, "X-Forwarded-For": "8.8.8.8, 10.0.0.1"}, "")
	if len(pub.evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(pub.evs))
	}
	ev := pub.evs[0]
	if ev.V != 1 || ev.LinkID != 1 || ev.UserID == nil || *ev.UserID != 7 || ev.Browser != "firefox" || ev.OS != "linux" ||
		ev.Country != "DE" || ev.Referrer != "news.example.com" || !ev.Timestamp.Equal(fixedNow) {
		t.Fatalf("bad event %+v", ev)
	}
	if pub.evs[1].Country != "US" || pub.evs[1].Referrer != "" {
		t.Fatalf("bad event 2 %+v", pub.evs[1])
	}
}

func TestProtectedEndpoint(t *testing.T) {
	_, pub, mux := setup(t)
	j := map[string]string{"Content-Type": "application/json"}
	cases := []struct {
		name, path, body string
		hdr              map[string]string
		status           int
		want             string
	}{
		{"ok v2", "/api/v2/links/" + uuidProtected + "/protected", `{"password":"secret"}`, j, 200, `{"target":"https://secret.example/"}`},
		{"ok v1", "/api/links/" + uuidProtected + "/protected", `{"password":"secret"}`, j, 200, `{"target":"https://secret.example/"}`},
		{"wrong", "/api/v2/links/" + uuidProtected + "/protected", `{"password":"nope"}`, j, 401, `{"error":"Password is not correct."}`},
		{"missing", "/api/v2/links/" + uuidProtected + "/protected", `{}`, j, 400, `{"error":"Password is invalid."}`},
		{"short", "/api/v2/links/" + uuidProtected + "/protected", `{"password":"ab"}`, j, 400, `{"error":"Password length must be between 3 and 64."}`},
		{"bad id", "/api/v2/links/short/protected", `{"password":"secret"}`, j, 400, `{"error":"ID is invalid."}`},
		{"unprotected link", "/api/v2/links/" + uuidPlain + "/protected", `{"password":"secret"}`, j, 400, `{"error":"Couldn't find the link."}`},
		{"unknown link", "/api/v2/links/33333333-3333-3333-3333-333333333333/protected", `{"password":"secret"}`, j, 400, `{"error":"Couldn't find the link."}`},
		{"form html ok -> 302", "/api/v2/links/" + uuidProtected + "/protected", `password=secret`, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Accept": "text/html"}, 302, ""},
		{"form html wrong -> page", "/api/v2/links/" + uuidProtected + "/protected", `password=nope`, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Accept": "text/html"}, 401, "Password is not correct."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := do(t, mux, "POST", "kutt.test", c.path, c.hdr, c.body)
			if rr.Code != c.status {
				t.Fatalf("status %d want %d: %s", rr.Code, c.status, rr.Body.String())
			}
			if c.want != "" && !strings.Contains(rr.Body.String(), c.want) {
				t.Fatalf("body %q want %q", rr.Body.String(), c.want)
			}
		})
	}
	// three successful unlocks publish (no isbot check in redirectProtected)
	if len(pub.evs) != 3 {
		t.Fatalf("want 3 events, got %d", len(pub.evs))
	}
}

func TestIsHTML(t *testing.T) {
	for accept, want := range map[string]bool{
		"":                 false,
		"*/*":              false,
		"application/json": false,
		"text/html":        true,
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8": true,
		"application/json, text/html":                                     false,
		"text/html;q=0.5, application/json":                               false,
		"text/*":                                                          true,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if accept != "" {
			r.Header.Set("Accept", accept)
		}
		if got := IsHTML(r); got != want {
			t.Errorf("IsHTML(%q)=%v want %v", accept, got, want)
		}
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "",
		"android-app://com.x":        "com.x",
		"https://www.google.com/x":   "google.com",
		"http://User@Host.COM:80/":   "host.com",
		"not a url":                  "",
		"https://[::1]:8080/":        "::1",
		"https://sub.www.example.io": "sub.example.io",
	} {
		if got := ReferrerHost(in); got != want {
			t.Errorf("ReferrerHost(%q)=%q want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"https://a.b/c?d=e#f":   "https://a.b/c?d=e#f",
		"https://a.b/ä":         "https://a.b/%C3%A4",
		"https://a.b/%20%zz":    "https://a.b/%20%25zz",
		"https://a.b/<x> \"y\"": "https://a.b/%3Cx%3E%20%22y%22",
	} {
		if got := EncodeURL(in); got != want {
			t.Errorf("EncodeURL(%q)=%q want %q", in, got, want)
		}
	}
	if RemoveWww("www.a.com") != "a.com" || RemoveWww("a.www.com") != "a.com" {
		t.Error("RemoveWww")
	}
}

func TestInfoJSONShape(t *testing.T) {
	_, _, mux := setup(t)
	rr := do(t, mux, "GET", "kutt.test", "/abc+", map[string]string{"Accept": "application/json"}, "")
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil || len(m) != 1 {
		t.Fatalf("%v %v", err, m)
	}
}

// BenchmarkRedirectHit measures the cache-hit hot path (fake resolver, human
// visitor on an owned link, event published).
func BenchmarkRedirectHit(b *testing.B) {
	h, pub, _ := setup(b)
	h.o.Metrics = nil
	h.o.Publisher = discardPub{}
	_ = pub
	req := httptest.NewRequest("GET", "/abc", nil)
	req.Host = "kutt.test"
	req.Header.Set("User-Agent", firefoxUA)
	req.SetPathValue("id", "abc")
	w := &nopWriter{h: http.Header{}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clear(w.h)
		h.ServeRedirect(w, req)
	}
}

// BenchmarkRedirectHitBot: bot traffic (no publish).
func BenchmarkRedirectHitBot(b *testing.B) {
	h, _, _ := setup(b)
	h.o.Metrics = nil
	req := httptest.NewRequest("GET", "/abc", nil)
	req.Host = "kutt.test"
	req.Header.Set("User-Agent", "Googlebot")
	req.SetPathValue("id", "abc")
	w := &nopWriter{h: http.Header{}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		clear(w.h)
		h.ServeRedirect(w, req)
	}
}

type discardPub struct{}

func (discardPub) Publish(domain.VisitEvent)   {}
func (discardPub) Close(context.Context) error { return nil }

type nopWriter struct {
	h    http.Header
	code int
}

func (w *nopWriter) Header() http.Header         { return w.h }
func (w *nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nopWriter) WriteHeader(c int)           { w.code = c }
