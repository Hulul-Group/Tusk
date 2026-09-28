package httpx

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

var gzPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return w
}}

// minGzipSize: responses shorter than this are sent uncompressed.
const minGzipSize = 1024

type gzipWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	buf         []byte
	status      int
	wroteHeader bool
	decided     bool
	compress    bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.status == 0 {
		g.status = code
	}
}

func (g *gzipWriter) decide(force bool) {
	if g.decided {
		return
	}
	if !force && len(g.buf) < minGzipSize {
		return
	}
	g.decided = true
	h := g.ResponseWriter.Header()
	st := g.status
	if st == 0 {
		st = http.StatusOK
	}
	g.compress = len(g.buf) >= minGzipSize && h.Get("Content-Encoding") == "" &&
		st != http.StatusNoContent && st != http.StatusNotModified && st >= 200
	if g.compress {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		g.gz = gzPool.Get().(*gzip.Writer)
		g.gz.Reset(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(st)
	g.wroteHeader = true
	if len(g.buf) > 0 {
		if g.compress {
			_, _ = g.gz.Write(g.buf)
		} else {
			_, _ = g.ResponseWriter.Write(g.buf)
		}
		g.buf = nil
	}
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.buf = append(g.buf, b...)
		g.decide(false)
		return len(b), nil
	}
	if g.compress {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) close() {
	g.decide(true)
	if g.gz != nil {
		_ = g.gz.Close()
		gzPool.Put(g.gz)
		g.gz = nil
	}
}

// Gzip compresses responses >= 1 KiB when the client accepts gzip.
// Mount it on the API only; redirects are tiny and must stay allocation-light.
func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}
