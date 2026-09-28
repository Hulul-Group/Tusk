// Package api implements Kutt's JSON API (server/routes/*.routes.js) under
// both /api/v2 and /api (server.js:78-79), plus the small set of Tusk
// extensions the React SPA needs (docs/API-EXTENSIONS.md).
package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Hulul-Group/Tusk/internal/auth"
	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/httpx"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// Mailer is implemented by internal/mail. Every method is a no-op error when
// mail is disabled; callers check Enabled first like Kutt's featureAccess.
type Mailer interface {
	Enabled() bool
	SendVerification(ctx context.Context, to, token string) error
	SendChangeEmail(ctx context.Context, to, token string) error
	SendResetPassword(ctx context.Context, to, token string) error
	SendReport(ctx context.Context, link string) error
}

// DomainInvalidator is implemented by *cache.DomainResolver so domain edits
// reach every redirector (docs/CONTRACTS.md, Agent A note).
type DomainInvalidator interface {
	Invalidate(ctx context.Context, address string) error
}

// Options wires the API.
type Options struct {
	Config   *config.Config
	Store    store.Store
	Links    domain.LinkResolver // Invalidate on every link mutation
	Domains  domain.DomainResolver
	Auth     *auth.Service
	OIDC     *auth.OIDC // nil when OIDC is disabled
	Mailer   Mailer
	Redis    *redis.Client // optional: rate limits + stats cache shared with Kutt
	IPs      httpx.IPResolver
	Metrics  *httpx.Metrics
	Log      *slog.Logger
	Mode     auth.Mode
	LookupIP func(ctx context.Context, host string) (string, error) // dns.lookup; default net.DefaultResolver
	Now      func() time.Time
	// Protected, when set, is mounted at POST /api{,/v2}/links/{id}/protected
	// (the redirect package's handler).
	Protected http.HandlerFunc
	// NeedsAdmin caches "no user exists" for /config (optional).
}

// Server is the API handler set.
type Server struct {
	o          Options
	cfg        *config.Config
	st         store.Store
	log        *slog.Logger
	rl         *rateLimiter
	stats      *statsCache
	alphabetRe *regexp.Regexp
}

// New builds the API server.
func New(o Options) *Server {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.IPs == nil {
		o.IPs = httpx.RemoteIP{TrustProxy: o.Config.TrustProxy}
	}
	if o.LookupIP == nil {
		o.LookupIP = func(ctx context.Context, host string) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil || len(ips) == 0 {
				return "", errors.New("dns lookup failed")
			}
			// dns.lookup returns the first address (IPv4 preferred by default ordering).
			for _, ip := range ips {
				if v4 := ip.IP.To4(); v4 != nil {
					return v4.String(), nil
				}
			}
			return ips[0].IP.String(), nil
		}
	}
	s := &Server{o: o, cfg: o.Config, st: o.Store, log: o.Log,
		alphabetRe: newCustomAlphabetRe(o.Config.LinkAlphabet)}
	s.rl = newRateLimiter(o.Config.EnableRateLimit, o.Redis, o.Now)
	s.stats = newStatsCache(o.Redis, o.Now)
	return s
}

// Register mounts every route under /api/v2 and /api.
func (s *Server) Register(mux *http.ServeMux) {
	for _, p := range []string{"/api/v2", "/api"} {
		s.register(mux, p)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Not found.")
	})
	// Non-/api Kutt routes that are not SPA pages.
	mux.HandleFunc("GET /login/oidc", s.h(s.oidcLogin))
	mux.HandleFunc("GET /logout", func(w http.ResponseWriter, r *http.Request) {
		s.o.Auth.ClearTokenCookie(w)
		http.Redirect(w, r, "/", http.StatusFound)
	})
}

func (s *Server) register(mux *http.ServeMux, p string) {
	hf := func(pattern string, fn handlerFunc) {
		i := strings.IndexByte(pattern, ' ')
		mux.HandleFunc(pattern[:i+1]+p+pattern[i+1:], s.h(fn))
	}
	// health.routes.js
	mux.HandleFunc("GET "+p+"/health", Health)
	// auth.routes.js
	hf("POST /auth/login", s.login)
	hf("POST /auth/signup", s.signup)
	hf("POST /auth/create-admin", s.createAdmin)
	hf("POST /auth/change-password", s.changePassword)
	hf("POST /auth/change-email", s.changeEmailRequest)
	hf("POST /auth/apikey", s.generateAPIKey)
	hf("POST /auth/reset-password", s.resetPassword)
	hf("POST /auth/new-password", s.newPassword)
	// Tusk extensions (docs/API-EXTENSIONS.md)
	hf("POST /auth/logout", s.logout)
	hf("POST /auth/verify/{token}", s.verifyEmail)
	hf("POST /auth/verify-email/{token}", s.verifyChangeEmail)
	hf("GET /config", s.publicConfig)
	// link.routes.js
	hf("GET /links", s.getLinks)
	hf("GET /links/admin", s.getLinksAdmin)
	hf("POST /links", s.createLink)
	hf("PATCH /links/{id}", s.editLink)
	hf("PATCH /links/admin/{id}", s.editLinkAdmin)
	hf("DELETE /links/{id}", s.removeLink)
	hf("POST /links/admin/ban/{id}", s.banLink)
	hf("GET /links/{id}/stats", s.linkStats)
	hf("POST /links/report", s.reportLink)
	if s.o.Protected != nil {
		mux.HandleFunc("POST "+p+"/links/{id}/protected", s.o.Protected)
	}
	// user.routes.js
	hf("GET /users", s.getUser)
	hf("GET /users/admin", s.getUsersAdmin)
	hf("POST /users/admin", s.createUser)
	hf("POST /users/delete", s.deleteUser)
	hf("DELETE /users/admin/{id}", s.deleteUserAdmin)
	hf("POST /users/admin/ban/{id}", s.banUser)
	// domain.routes.js
	hf("GET /domains/admin", s.getDomainsAdmin)
	hf("POST /domains", s.addDomain)
	hf("POST /domains/admin", s.addDomainAdmin)
	hf("DELETE /domains/{id}", s.removeDomain)
	hf("DELETE /domains/admin/{id}", s.removeDomainAdmin)
	hf("POST /domains/admin/ban/{id}", s.banDomain)
}

// Health is GET /api/v2/health: res.send("OK").
func Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("OK"))
}

// ---------- plumbing ----------

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// errBody is Kutt's CustomError; status defaults to 500 like utils.js:21.
func errBody(status int, msg string) error { return domain.NewError(status, msg) }

// kuttErr: `new CustomError(msg)` without a status -> 500.
func kuttErr(msg string) error { return domain.NewError(http.StatusInternalServerError, msg) }

func (s *Server) h(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			s.renderError(w, r, err)
		}
	}
}

// renderError is helpers.handler.js error(): CustomError message+status,
// anything else "An error occurred." 500.
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *domain.AppError
	if errors.As(err, &ae) {
		st := ae.Status
		if st == 0 {
			st = http.StatusInternalServerError
		}
		writeError(w, st, ae.Message)
		return
	}
	var be *bodyError
	if errors.As(err, &be) {
		writeError(w, be.status, "An error occurred.")
		return
	}
	s.log.Error("api error", "path", r.URL.Path, "err", err, "rid", httpx.RequestIDFrom(r.Context()))
	writeError(w, http.StatusInternalServerError, "An error occurred.")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, obj{{"error", msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"error":"An error occurred."}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func message(w http.ResponseWriter, status int, msg string) error {
	writeJSON(w, status, obj{{"message", msg}})
	return nil
}

// ---------- body parsing (express.json + express.urlencoded) ----------

type bodyError struct{ status int }

func (e *bodyError) Error() string { return "bad body" }

const maxBody = 100 << 10 // body-parser default limit "100kb"

// body is the parsed request body (req.body).
type body map[string]any

func (b body) get(k string) any { return b[k] }

func (b body) str(k string) string {
	if v, ok := b[k].(string); ok {
		return v
	}
	return ""
}

func readBody(r *http.Request) (body, error) {
	b := body{}
	if r.Body == nil {
		return b, nil
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return b, &bodyError{status: http.StatusBadRequest}
	}
	if len(raw) > maxBody {
		return b, &bodyError{status: http.StatusRequestEntityTooLarge}
	}
	switch {
	case ct == "application/json" || strings.HasSuffix(ct, "+json"):
		if len(strings.TrimSpace(string(raw))) == 0 {
			return b, nil
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return b, &bodyError{status: http.StatusBadRequest}
		}
		switch x := v.(type) {
		case map[string]any:
			return body(x), nil
		case []any:
			return b, nil
		default: // body-parser strict mode: only objects/arrays
			return b, &bodyError{status: http.StatusBadRequest}
		}
	case ct == "application/x-www-form-urlencoded":
		vals, err := url.ParseQuery(string(raw))
		if err != nil {
			return b, &bodyError{status: http.StatusBadRequest}
		}
		for k, vs := range vals {
			if len(vs) == 1 {
				b[k] = vs[0]
			} else {
				arr := make([]any, len(vs))
				for i, v := range vs {
					arr[i] = v
				}
				b[k] = arr
			}
		}
	}
	return b, nil
}

// ---------- auth helpers ----------

// authn runs auth.apikey (+ optional) then auth.jwt / auth.jwtLoose.
func (s *Server) authn(r *http.Request, b body, apiKey, strict bool) (*auth.Principal, error) {
	key := ""
	if apiKey {
		key = b.str("apikey")
		if key == "" {
			key = auth.APIKeyFrom(r, "")
		}
	}
	return s.o.Auth.AuthenticateWith(r.Context(), r, key, apiKey, strict)
}

func (s *Server) admin(p *auth.Principal) error {
	if p == nil || !p.Admin {
		return errBody(http.StatusUnauthorized, auth.MsgAdminOnly)
	}
	return nil
}

// featureAccess: auth.handler.js:363-376.
func featureAccess(flags ...bool) error {
	for _, f := range flags {
		if !f {
			return errBody(http.StatusBadRequest, "Request is not allowed.")
		}
	}
	return nil
}

// ---------- parseQuery (helpers.handler.js:54-86) ----------

type pageQuery struct {
	limit, skip int
}

func parseQuery(w http.ResponseWriter, r *http.Request) (pageQuery, bool) {
	q := r.URL.Query()
	for _, k := range []string{"limit", "skip", "search"} {
		if len(q[k]) > 1 {
			writeJSON(w, http.StatusBadRequest, obj{{"error", k + " query is not valid."}})
			return pageQuery{}, false
		}
	}
	limit := 10
	if n, ok := parseIntJS(q.Get("limit")); ok && n != 0 {
		limit = int(n)
	}
	if limit > 50 {
		limit = 50
	}
	if limit < 0 {
		limit = 10 // Kutt would pass a negative LIMIT to the DB (500)
	}
	skip := 0
	if n, ok := parseIntJS(q.Get("skip")); ok && n > 0 {
		skip = int(n)
	}
	return pageQuery{limit: limit, skip: skip}, true
}

// parseBooleanQuery ports utils.parseBooleanQuery.
func parseBooleanQuery(v string) *bool {
	switch v {
	case "true":
		t := true
		return &t
	case "false":
		f := false
		return &f
	}
	return nil
}

// ---------- short URLs ----------

// shortURL ports utils.getShortURL(address, domain).url.
func (s *Server) shortURL(address string, dom *string) string {
	d := s.cfg.DefaultDomain
	hasDomain := dom != nil && *dom != ""
	if hasDomain {
		d = *dom
	}
	proto := "http://"
	if (s.cfg.CustomDomainUseHTTPS || !hasDomain) && !s.o.Mode.Dev {
		proto = "https://"
	}
	return proto + d + "/" + address
}

// ---------- nanoid ----------

const nanoidURLAlphabet = "useandom-26T198340PX75pxJACKVERYMINDBUSHWOLF_GQZbfghjklqvwyzrict"

// nanoid ports nanoid's customAlphabet (mask + rejection sampling).
func nanoid(alphabet string, size int) string {
	a := []rune(alphabet)
	if len(a) == 0 {
		a = []rune(nanoidURLAlphabet)
	}
	mask := 1
	for mask < len(a)-1 {
		mask = mask<<1 | 1
	}
	if len(a) == 1 {
		mask = 1
	}
	step := (int)(1.6*float64(mask*size)/float64(len(a))) + 1
	out := make([]rune, 0, size)
	buf := make([]byte, step)
	for {
		_, _ = rand.Read(buf)
		for _, c := range buf {
			i := int(c) & mask
			if i < len(a) {
				out = append(out, a[i])
				if len(out) == size {
					return string(out)
				}
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

func newUUID() string { return uuid.NewString() }
