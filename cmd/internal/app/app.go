// Package app wires Tusk's services from config. cmd/tusk, cmd/api,
// cmd/redirector and cmd/worker are thin mains over Run.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Hulul-Group/Tusk/internal/api"
	"github.com/Hulul-Group/Tusk/internal/auth"
	"github.com/Hulul-Group/Tusk/internal/cache"
	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/events"
	"github.com/Hulul-Group/Tusk/internal/geo"
	"github.com/Hulul-Group/Tusk/internal/httpx"
	"github.com/Hulul-Group/Tusk/internal/mail"
	"github.com/Hulul-Group/Tusk/internal/redirect"
	"github.com/Hulul-Group/Tusk/internal/store/sqlstore"
	"github.com/Hulul-Group/Tusk/internal/uaparse"
	"github.com/Hulul-Group/Tusk/internal/webui"
	"github.com/Hulul-Group/Tusk/internal/worker"
)

// Drain is the graceful-shutdown budget (HTTP drain + visit publisher flush).
const Drain = 30 * time.Second

// Main loads config, forces roles (nil = SERVICE_ROLES), runs until SIGINT/SIGTERM.
func Main(service string, roles []string) {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})).With("service", service)
	slog.SetDefault(log)
	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(2)
	}
	if roles != nil {
		cfg.ServiceRoles = roles
	}
	ctx, stop := httpx.SignalContext(context.Background())
	defer stop()
	if err := Run(ctx, cfg, service, log); err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

func logLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(os.Getenv("LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return l
}

// Run starts every role in cfg.ServiceRoles and blocks until ctx is done.
func Run(ctx context.Context, cfg *config.Config, service string, log *slog.Logger) error {
	runAPI, runRedir, runWorker := cfg.HasRole("api"), cfg.HasRole("redirector"), cfg.HasRole("worker")
	if !runAPI && !runRedir && !runWorker {
		return fmt.Errorf("SERVICE_ROLES %v: nothing to run (use all|api|redirector|worker)", cfg.ServiceRoles)
	}
	mode := auth.ModeFromEnv()

	st, err := sqlstore.Open(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer st.Close()
	if runAPI { // Kutt runs knex migrations on start (server.js); only API/all migrate.
		if err := st.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		log.Info("migrations applied")
	}
	rdb, err := cache.NewRedis(cfg)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	if rdb != nil {
		defer rdb.Close()
	}

	links := cache.NewLinkResolver(st.Links(), rdb, cfg)
	domains := cache.NewDomainResolver(st.Domains(), rdb, cfg)
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()
	go cache.Subscribe(subCtx, rdb, links, domains)

	pub, consumer := events.New(cfg, rdb)

	metrics := httpx.NewMetrics(service)
	metrics.Register(prometheus.NewCounterFunc(prometheus.CounterOpts{Namespace: "tusk", Name: "link_cache_hits_total",
		Help: "Link resolver local-cache hits (cache.LinkResolver.Hits)."}, func() float64 { return float64(links.Hits.Load()) }))
	metrics.Register(prometheus.NewCounterFunc(prometheus.CounterOpts{Namespace: "tusk", Name: "link_cache_misses_total",
		Help: "Link resolver local-cache misses (cache.LinkResolver.Misses)."}, func() float64 { return float64(links.Misses.Load()) }))
	if d, ok := pub.(interface{ Dropped() uint64 }); ok {
		metrics.Register(prometheus.NewCounterFunc(prometheus.CounterOpts{Namespace: "tusk", Name: "visit_publisher_dropped_total",
			Help: "Visit events dropped by the publisher (buffer full / Redis down)."}, func() float64 { return float64(d.Dropped()) }))
	}

	cip := geo.NewClientIP(cfg)
	go cip.Run(subCtx) // refresh Cloudflare ranges
	geoRes, err := geo.New(cfg)
	if err != nil {
		return fmt.Errorf("geo: %w", err)
	}

	var servers []*http.Server
	rd := redirect.New(redirect.Options{
		DefaultDomain: cfg.DefaultDomain, Links: links, Domains: domains, Finder: st.Links(),
		Publisher: pub, Geo: geoRes, IPs: cip, IsBot: uaparse.IsBot, ParseUA: uaparse.Parse,
		Metrics: metrics, SiteName: cfg.SiteName,
	})
	base := func(h http.Handler, sample uint64) http.Handler {
		return httpx.Chain(h, httpx.RequestID, httpx.Observe(metrics, log, cip, sample), httpx.Recover(log), httpx.SecurityHeaders)
	}

	if runAPI {
		mailer, err := mail.New(cfg, log)
		if err != nil {
			return fmt.Errorf("mail: %w", err)
		}
		authSvc := auth.New(st.Users(), cfg.JWTSecret, mode)
		var oidc *auth.OIDC
		if cfg.OIDCEnabled {
			oidc, err = auth.NewOIDC(auth.OIDCConfig{
				Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret,
				Scope: cfg.OIDCScope, Prompt: cfg.OIDCPrompt, EmailClaim: cfg.OIDCEmailClaim,
				RedirectURL: mode.SiteScheme() + cfg.DefaultDomain + "/login/oidc", CookieSecret: cfg.JWTSecret, Secure: mode.Prod,
			}, st.Users())
			if err != nil {
				return fmt.Errorf("oidc: %w", err)
			}
		}
		apiSrv := api.New(api.Options{
			Config: cfg, Store: st, Links: links, Domains: domains, Auth: authSvc, OIDC: oidc, Mailer: mailer,
			Redis: rdb, IPs: cip, Metrics: metrics, Log: log, Mode: mode, Protected: rd.ServeProtected,
		})
		mux := http.NewServeMux()
		apiSrv.Register(mux)
		// The SPA owns /, /login, /404, /banned ... and static assets; /{id} is the redirect.
		ui := webui.Handler(cfg)
		for _, p := range webui.SPARoutes {
			mux.Handle("GET "+p, ui)
		}
		for _, p := range webui.AssetPrefixes {
			mux.Handle("GET "+p, ui)
		}
		mux.HandleFunc("GET /{id}", rd.ServeRedirect)
		mux.Handle("/", ui) // SPA 404 page for everything else (server.js: app.get("*", notFound))
		// API responses are gzip'd; redirects/static are not re-compressed.
		gz := httpx.Gzip(mux)
		h := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				gz.ServeHTTP(w, r)
				return
			}
			mux.ServeHTTP(w, r)
		}))
		h = homepage(rd, h)
		servers = append(servers, httpx.NewServer(":"+strconv.Itoa(cfg.Port), base(h, 100)))
	}
	if runRedir && !runAPI {
		// Standalone redirector: GET /{id}, protected unlock, server-rendered 404/banned, health.
		mux := http.NewServeMux()
		rd.Register(mux, true)
		for _, p := range []string{"/api/v2/health", "/api/health"} {
			mux.HandleFunc("GET "+p, api.Health)
		}
		mux.HandleFunc("/", rd.ServeNotFound)
		port := cfg.RedirectorPort
		if !cfg.SplitMode() {
			port = cfg.Port
		}
		servers = append(servers, httpx.NewServer(":"+strconv.Itoa(port), base(homepage(rd, mux), 100)))
	}
	if !runAPI && !runRedir {
		// Worker-only: health endpoint on PORT so orchestrators can probe it.
		mux := http.NewServeMux()
		for _, p := range []string{"/api/v2/health", "/api/health"} {
			mux.HandleFunc("GET "+p, api.Health)
		}
		servers = append(servers, httpx.NewServer(":"+strconv.Itoa(cfg.Port), mux))
	}
	if cfg.MetricsPort > 0 {
		mm := http.NewServeMux()
		mm.Handle("GET /metrics", metrics.Handler())
		mm.HandleFunc("GET /api/v2/health", api.Health)
		servers = append(servers, httpx.NewServer(":"+strconv.Itoa(cfg.MetricsPort), mm))
	}

	var wg sync.WaitGroup
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	var workerErr error
	if runWorker {
		w := worker.New(st, consumer, links, cfg)
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerErr = w.Run(workerCtx)
		}()
	}

	serveErr := httpx.Serve(ctx, log, Drain, servers...)
	log.Info("shutting down: flushing visit publisher")
	fctx, cancel := context.WithTimeout(context.Background(), Drain)
	defer cancel()
	if err := pub.Close(fctx); err != nil {
		log.Warn("publisher close", "err", err)
	}
	// Stop the worker after the publisher flushed so in-process (channel) events are applied.
	cancelWorker()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-fctx.Done():
		log.Warn("worker did not stop within drain budget")
	}
	return errors.Join(serveErr, workerErr)
}

// homepage applies redirectCustomDomainHomepage (server.js:72) to everything
// except static assets, which Kutt serves before that middleware (server.js:54-56).
func homepage(rd *redirect.Handler, next http.Handler) http.Handler {
	wrapped := rd.CustomDomainHomepage(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range webui.AssetPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}
		wrapped.ServeHTTP(w, r)
	})
}
