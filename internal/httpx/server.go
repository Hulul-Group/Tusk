package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// NewServer returns an http.Server with production timeouts.
// WriteTimeout is generous enough for stats queries; redirects finish in µs.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// SignalContext returns a context cancelled on SIGINT/SIGTERM.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// Serve runs the servers until ctx is cancelled, then shuts them all down
// within drain. Returns the first listen error (other than ErrServerClosed).
func Serve(ctx context.Context, log *slog.Logger, drain time.Duration, servers ...*http.Server) error {
	errc := make(chan error, len(servers))
	for _, s := range servers {
		ln, err := net.Listen("tcp", s.Addr)
		if err != nil {
			return err
		}
		log.Info("listening", "addr", ln.Addr().String())
		go func(s *http.Server, ln net.Listener) {
			if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}(s, ln)
	}
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(sctx); err != nil {
			log.Warn("shutdown", "addr", s.Addr, "err", err)
		}
	}
	return runErr
}
