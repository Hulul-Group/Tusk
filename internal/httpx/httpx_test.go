package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersHelmet(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	want := map[string]string{
		"Cross-Origin-Opener-Policy": "same-origin", "Cross-Origin-Resource-Policy": "same-origin",
		"Origin-Agent-Cluster": "?1", "Referrer-Policy": "no-referrer",
		"Strict-Transport-Security": "max-age=15552000; includeSubDomains", "X-Content-Type-Options": "nosniff",
		"X-DNS-Prefetch-Control": "off", "X-Download-Options": "noopen", "X-Frame-Options": "SAMEORIGIN",
		"X-Permitted-Cross-Domain-Policies": "none", "X-XSS-Protection": "0",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("%s=%q want %q", k, got, v)
		}
	}
	if rr.Header().Get("Content-Security-Policy") != "" {
		t.Error("CSP must be disabled (server.js:39)")
	}
}

func TestRequestIDAndRecover(t *testing.T) {
	var seen string
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
		panic("boom")
	}), RequestID, Recover(slog.New(slog.NewTextHandler(io.Discard, nil))))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 500 || rr.Body.String() != `{"error":"An error occurred."}` {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	if seen == "" || rr.Header().Get(RequestIDHeader) != seen {
		t.Fatalf("rid %q %q", seen, rr.Header().Get(RequestIDHeader))
	}
	// incoming id is honoured
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(RequestIDHeader, "abc-123")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if seen != "abc-123" {
		t.Fatalf("incoming rid not kept: %q", seen)
	}
}

func TestGzip(t *testing.T) {
	big := strings.Repeat("hello tusk ", 500)
	h := Gzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/small" {
			_, _ = w.Write([]byte(`{"a":1}`))
			return
		}
		_, _ = w.Write([]byte(big))
	}))
	req := httptest.NewRequest("GET", "/big", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rr.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("headers %v", rr.Header())
	}
	zr, err := gzip.NewReader(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	if string(out) != big {
		t.Fatal("roundtrip")
	}
	req = httptest.NewRequest("GET", "/small", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Header().Get("Content-Encoding") != "" || rr.Body.String() != `{"a":1}` {
		t.Fatalf("small body must not be compressed: %v %q", rr.Header(), rr.Body.String())
	}
	req = httptest.NewRequest("GET", "/big", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Header().Get("Content-Encoding") != "" || rr.Body.String() != big {
		t.Fatal("no Accept-Encoding must pass through")
	}
}

func TestObserveMetricsAndClientIP(t *testing.T) {
	m := NewMetrics("test")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(302) })
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Observe(m, log, RemoteIP{TrustProxy: true}, 1)(mux)
	req := httptest.NewRequest("GET", "/abc", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(buf.String(), `"route":"GET /{id}"`) || !strings.Contains(buf.String(), `"ip":"198.51.100.7"`) {
		t.Fatalf("log %s", buf.String())
	}
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rr.Body.String(), `route="GET /{id}"`) {
		t.Fatalf("metrics body missing route label:\n%s", rr.Body.String()[:min(2000, rr.Body.Len())])
	}
	if (RemoteIP{}).IP(req) != "192.0.2.1" {
		t.Fatalf("untrusted proxy must use RemoteAddr: %s", (RemoteIP{}).IP(req))
	}
}
