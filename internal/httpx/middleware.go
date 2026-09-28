// Package httpx holds the HTTP plumbing shared by every Tusk service:
// request ids, access logging, panic recovery, helmet-equivalent security
// headers, gzip (API only), real client IP, Prometheus metrics and a
// graceful server helper.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
)

// Middleware is the standard func(http.Handler) http.Handler decorator.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so that the first one listed is the outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ---------- client IP ----------

// IPResolver extracts the trusted client IP. internal/geo's ClientIP
// implements it (Cloudflare-aware); RemoteIP is the fallback.
type IPResolver interface {
	IP(r *http.Request) string
}

// RemoteIP is a minimal IPResolver. With TrustProxy it mirrors Express
// "trust proxy = true": the left-most X-Forwarded-For entry wins.
type RemoteIP struct{ TrustProxy bool }

func (p RemoteIP) IP(r *http.Request) string {
	if p.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			if s := strings.TrimSpace(xff); s != "" {
				return s
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------- request id ----------

type ctxKey int

const requestIDKey ctxKey = 1

// RequestIDHeader is echoed back and propagated.
const RequestIDHeader = "X-Request-Id"

var ridFallback atomic.Uint64

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		n := ridFallback.Add(1)
		for i := 0; i < 8; i++ {
			b[i] = byte(n >> (8 * i))
		}
	}
	return hex.EncodeToString(b[:])
}

// RequestID reuses an inbound X-Request-Id (if sane) or generates one, sets it
// on the response and stores it in the context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" || len(id) > 128 {
			id = newRequestID()
		}
		w.Header()[RequestIDHeader] = []string{id}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// RequestIDFrom returns the request id stored by RequestID ("" if none).
func RequestIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey).(string)
	return s
}

// ---------- recover ----------

// Recover turns panics into a Kutt-shaped 500 {"error":"An error occurred."}.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					log.Error("panic", "err", v, "path", r.URL.Path, "stack", string(debug.Stack()))
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"An error occurred."}`))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- security headers ----------

// Header values set by helmet 7.1.0 with contentSecurityPolicy disabled
// (Kutt server.js:39). Captured from the real module:
// node -e 'helmet({contentSecurityPolicy:false})(...)'.
// Slices are shared read-only; net/http never mutates them.
var securityHeaders = [...]struct {
	k string
	v []string
}{
	{"Cross-Origin-Opener-Policy", []string{"same-origin"}},
	{"Cross-Origin-Resource-Policy", []string{"same-origin"}},
	{"Origin-Agent-Cluster", []string{"?1"}},
	{"Referrer-Policy", []string{"no-referrer"}},
	{"Strict-Transport-Security", []string{"max-age=15552000; includeSubDomains"}},
	{"X-Content-Type-Options", []string{"nosniff"}},
	{"X-Dns-Prefetch-Control", []string{"off"}},
	{"X-Download-Options", []string{"noopen"}},
	{"X-Frame-Options", []string{"SAMEORIGIN"}},
	{"X-Permitted-Cross-Domain-Policies", []string{"none"}},
	{"X-Xss-Protection", []string{"0"}},
}

// SetSecurityHeaders writes the helmet defaults onto h. Exposed so the
// redirect hot path can apply them without an extra handler layer.
func SetSecurityHeaders(h http.Header) {
	for i := range securityHeaders {
		h[securityHeaders[i].k] = securityHeaders[i].v
	}
}

// SecurityHeaders is helmet({contentSecurityPolicy:false}) as middleware.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetSecurityHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}

// ---------- status recorder ----------

// Recorder captures status and bytes written.
type Recorder struct {
	http.ResponseWriter
	Status int
	Bytes  int64
}

func (r *Recorder) WriteHeader(code int) {
	if r.Status == 0 {
		r.Status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *Recorder) Write(b []byte) (int, error) {
	if r.Status == 0 {
		r.Status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.Bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *Recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *Recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *Recorder) code() int {
	if r.Status == 0 {
		return http.StatusOK
	}
	return r.Status
}

// ---------- access log + metrics ----------

// Observe records latency (Prometheus, per ServeMux route pattern) and writes a
// slog JSON access-log line. Redirect responses (route "GET /{id}" with 3xx)
// are sampled 1-in-sampleEvery to keep log volume sane on the hot path; errors
// (>=500) are always logged. m and log may be nil.
func Observe(m *Metrics, log *slog.Logger, ips IPResolver, sampleEvery uint64) Middleware {
	var n atomic.Uint64
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &Recorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			d := time.Since(start)
			code := rec.code()
			// r.Pattern is populated in-place by ServeMux (Go 1.23+).
			pattern := r.Pattern
			if pattern == "" {
				pattern = "unmatched"
			}
			if m != nil {
				m.observe(pattern, r.Method, code, d)
			}
			if log == nil {
				return
			}
			if code < 500 && code >= 300 && code < 400 && sampleEvery > 1 && isRedirectPattern(pattern) {
				if n.Add(1)%sampleEvery != 0 {
					return
				}
			}
			ip := ""
			if ips != nil {
				ip = ips.IP(r)
			}
			log.LogAttrs(r.Context(), slog.LevelInfo, "http",
				slog.String("rid", RequestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", pattern),
				slog.Int("status", code),
				slog.Int64("bytes", rec.Bytes),
				slog.Float64("ms", float64(d.Microseconds())/1000),
				slog.String("ip", ip),
				slog.String("host", r.Host),
			)
		})
	}
}

func isRedirectPattern(p string) bool {
	return strings.HasSuffix(p, "/{id}") && !strings.Contains(p, "/api")
}
