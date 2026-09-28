package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Hulul-Group/Tusk/internal/config"
)

const testIndex = `<!doctype html><html><head><title>Kutt</title><!--tusk:custom-css--></head><body><div id="root"></div></body></html>`

var bigJS = strings.Repeat("console.log('tusk');\n", 200)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

// testHandler builds a handler over an in-memory dist and an on-disk custom dir.
func testHandler(t *testing.T, withCustom bool) (*handler, string) {
	t.Helper()
	dist := fstest.MapFS{
		"index.html":                {Data: []byte(testIndex)},
		"assets/index-abc123.js":    {Data: []byte(bigJS)},
		"assets/index-abc123.js.gz": {Data: gz(t, bigJS)},
		"assets/index-abc123.js.br": {Data: []byte("BROTLI")}, // fake but smaller: we only check selection
		"assets/app-def456.css":     {Data: []byte("body{color:red}")},
		"images/logo.png":           {Data: []byte("\x89PNG embedded")},
		"robots.txt":                {Data: []byte("User-agent: *\nDisallow: /api/\n")},
	}
	cfg := &config.Config{SiteName: "Shorty"}
	custom := ""
	if withCustom {
		custom = t.TempDir()
		must(t, os.MkdirAll(filepath.Join(custom, "images"), 0o755))
		must(t, os.MkdirAll(filepath.Join(custom, "css"), 0o755))
		must(t, os.WriteFile(filepath.Join(custom, "images", "logo.png"), []byte("\x89PNG custom"), 0o644))
		must(t, os.WriteFile(filepath.Join(custom, "images", "extra.svg"), []byte("<svg/>"), 0o644))
		must(t, os.WriteFile(filepath.Join(custom, "css", "theme.css"), []byte(":root{--primary:crimson}"), 0o644))
		must(t, os.WriteFile(filepath.Join(custom, "css", "b.css"), []byte("a{}"), 0o644))
		must(t, os.WriteFile(filepath.Join(custom, "secret.txt"), []byte("nope"), 0o644))
		cfg.StaticDir = custom
	}
	return newHandler(dist, cfg), custom
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func get(h http.Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCustomOverridesEmbedded(t *testing.T) {
	h, _ := testHandler(t, true)

	rec := get(h, "/images/logo.png", nil)
	if rec.Code != 200 || rec.Body.String() != "\x89PNG custom" {
		t.Fatalf("custom image should win: %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != cacheCustom {
		t.Errorf("custom cache-control = %q", cc)
	}

	// custom-only file
	if rec := get(h, "/images/extra.svg", nil); rec.Code != 200 || rec.Body.String() != "<svg/>" {
		t.Fatalf("custom-only image: %d %q", rec.Code, rec.Body.String())
	}

	// Kutt's express.static("custom/css", {extensions: ["css"]}): /css/theme → theme.css
	for _, p := range []string{"/css/theme.css", "/css/theme"} {
		rec := get(h, p, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "crimson") {
			t.Fatalf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
			t.Errorf("%s content-type %q", p, ct)
		}
	}

	// Files outside custom/images and custom/css are never exposed, and traversal fails.
	for _, p := range []string{"/secret.txt", "/images/../secret.txt", "/css/..%2fsecret.txt", "/images/..%2f..%2fetc%2fpasswd"} {
		rec := get(h, p, nil)
		if strings.Contains(rec.Body.String(), "nope") || strings.Contains(rec.Body.String(), "root:") {
			t.Fatalf("%s leaked a file outside the custom dirs", p)
		}
	}
}

func TestEmbeddedWhenNoCustom(t *testing.T) {
	h, _ := testHandler(t, false)
	rec := get(h, "/images/logo.png", nil)
	if rec.Code != 200 || rec.Body.String() != "\x89PNG embedded" {
		t.Fatalf("embedded image: %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != cacheDefault {
		t.Errorf("non-hashed file cache-control = %q", cc)
	}
}

func TestIndexInjection(t *testing.T) {
	h, _ := testHandler(t, true)
	body := get(h, "/", nil).Body.String()
	if !strings.Contains(body, "<title>Shorty</title>") {
		t.Errorf("site name not injected: %s", body)
	}
	// sorted, both custom stylesheets linked (Kutt custom_styles)
	want := `<link rel="stylesheet" href="/css/b.css"><link rel="stylesheet" href="/css/theme.css">`
	if !strings.Contains(body, want) {
		t.Errorf("custom css links missing: %s", body)
	}
	if strings.Contains(body, customCSSMarker) {
		t.Error("marker left in output")
	}
}

func TestCacheHeaders(t *testing.T) {
	h, _ := testHandler(t, false)
	cases := map[string]string{
		"/assets/index-abc123.js": cacheImmutable,
		"/assets/app-def456.css":  cacheImmutable,
		"/":                       cacheIndex,
		"/index.html":             cacheIndex,
		"/settings":               cacheIndex,
		"/robots.txt":             cacheDefault,
	}
	for p, want := range cases {
		rec := get(h, p, nil)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", p, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", p, got, want)
		}
		if rec.Header().Get("ETag") == "" {
			t.Errorf("%s: missing ETag", p)
		}
	}
}

func TestETagNotModified(t *testing.T) {
	h, _ := testHandler(t, false)
	etag := get(h, "/assets/index-abc123.js", nil).Header().Get("ETag")
	rec := get(h, "/assets/index-abc123.js", map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("want 304, got %d", rec.Code)
	}
}

func TestSPAFallback(t *testing.T) {
	h, _ := testHandler(t, false)
	for _, p := range []string{"/", "/login", "/settings", "/admin", "/stats?id=abc", "/verify/some-token", "/reset-password/tok", "/deep/client/route"} {
		rec := get(h, p, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Errorf("%s: want index.html, got %d", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: content-type %q", p, ct)
		}
	}
	// Missing hashed assets and asset-looking paths must 404, not return HTML
	// (a stale index referencing an old chunk should fail loudly).
	for _, p := range []string{"/assets/missing-000.js", "/assets/foo", "/nope.js", "/api/v2/unknown"} {
		if rec := get(h, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}
	// A browser navigating to a dotted client path still gets the shell.
	if rec := get(h, "/some.page", map[string]string{"Accept": "text/html"}); rec.Code != 200 {
		t.Errorf("html navigation to dotted path: %d", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h, _ := testHandler(t, false)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodHead, "/assets/index-abc123.js", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d len=%d", rec.Code, rec.Body.Len())
	}
}

func TestPrecompressed(t *testing.T) {
	h, _ := testHandler(t, false)
	const js = "/assets/index-abc123.js"

	// brotli preferred when allowed
	rec := get(h, js, map[string]string{"Accept-Encoding": "gzip, deflate, br"})
	if rec.Header().Get("Content-Encoding") != "br" || rec.Body.String() != "BROTLI" {
		t.Fatalf("br: enc=%q body=%q", rec.Header().Get("Content-Encoding"), rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Error("missing Vary: Accept-Encoding")
	}

	// gzip when br is refused
	rec = get(h, js, map[string]string{"Accept-Encoding": "gzip, br;q=0"})
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip: enc=%q", rec.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(rec.Body)
	must(t, err)
	plain, _ := io.ReadAll(zr)
	if string(plain) != bigJS {
		t.Fatal("gzip body does not decode to the original")
	}

	// identity when nothing acceptable
	for _, ae := range []string{"", "identity", "deflate", "gzip;q=0, br;q=0"} {
		rec = get(h, js, map[string]string{"Accept-Encoding": ae})
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != bigJS {
			t.Fatalf("AE %q: enc=%q", ae, rec.Header().Get("Content-Encoding"))
		}
	}

	// wildcard
	rec = get(h, js, map[string]string{"Accept-Encoding": "*"})
	if rec.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("*: enc=%q", rec.Header().Get("Content-Encoding"))
	}

	// index.html is gzipped at startup (it is rewritten, so build-time .gz can't be reused)
	h2 := newHandler(fstest.MapFS{"index.html": {Data: []byte(strings.Repeat(testIndex, 20))}}, nil)
	rec = get(h2, "/", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("index gzip: enc=%q", rec.Header().Get("Content-Encoding"))
	}
}

func TestPlaceholderWithoutBuild(t *testing.T) {
	h := newHandler(fstest.MapFS{}, nil)
	rec := get(h, "/", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "web UI was not built") {
		t.Fatalf("placeholder: %d %q", rec.Code, rec.Body.String())
	}
}

// TestEmbeddedHandler exercises the real embed.FS (placeholder or real build).
func TestEmbeddedHandler(t *testing.T) {
	h := Handler(&config.Config{SiteName: "Kutt"})
	rec := get(h, "/", nil)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("embedded index: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
