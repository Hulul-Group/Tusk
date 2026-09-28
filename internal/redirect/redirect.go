// Package redirect is the Tusk hot path: GET /{id} (Kutt links.handler.js
// redirect, lines 459-545), the custom-domain homepage redirect
// (redirectCustomDomainHomepage, 587-608), the password-protected unlock
// endpoint (redirectProtected, 547-585) and the server-rendered protected,
// info, banned and 404 pages.
//
// The handler performs NO database writes and never blocks on analytics: visit
// events are handed to a non-blocking domain.VisitPublisher.
package redirect

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/httpx"
	"github.com/Hulul-Group/Tusk/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// ClientIP is satisfied by *geo.ClientIP.
type ClientIP interface {
	IP(r *http.Request) string
	// GeoHeaders returns the headers when the geo header is trusted for r,
	// or an empty getter otherwise (GEO_TRUST_CLOUDFLARE_ONLY).
	GeoHeaders(r *http.Request) domain.HeaderGetter
}

// LinkFinder is the subset of store.LinkStore needed to read password hashes
// (never cached in RedirectTarget).
type LinkFinder interface {
	Find(ctx context.Context, f store.LinkFilter) (*domain.Link, error)
}

// Options configures a Handler. Every field except DefaultDomain, Links and
// Finder is optional.
type Options struct {
	DefaultDomain string
	Links         domain.LinkResolver
	Domains       domain.DomainResolver // nil: custom domains disabled
	Finder        LinkFinder
	Publisher     domain.VisitPublisher // nil: stats disabled
	Geo           domain.GeoResolver    // nil: country unknown
	IPs           ClientIP              // nil: RemoteAddr
	IsBot         func(ua string) bool  // uaparse.IsBot
	ParseUA       func(ua string) (browser, os string)
	Metrics       *httpx.Metrics
	// ShortURL renders the short link for the info page (utils.getShortURL().link).
	SiteName string
	Now      func() time.Time
}

// Handler serves the redirect routes.
type Handler struct {
	o     Options
	pages *pages
}

// New builds a Handler.
func New(o Options) *Handler {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.IsBot == nil {
		o.IsBot = func(string) bool { return false }
	}
	if o.ParseUA == nil {
		o.ParseUA = func(string) (string, string) { return "other", "other" }
	}
	if o.SiteName == "" {
		o.SiteName = "Kutt"
	}
	return &Handler{o: o, pages: newPages(o.SiteName)}
}

// PreservedURLs is utils.preservedURLs (utils.js:158-199).
var PreservedURLs = []string{
	"login", "logout", "create-admin", "404", "settings", "admin", "stats", "signup", "banned", "report",
	"reset-password", "resetpassword", "verify-email", "verifyemail", "verify", "terms",
	"confirm-link-delete", "confirm-link-ban", "confirm-user-delete", "confirm-user-ban", "create-user",
	"confirm-domain-delete-admin", "confirm-domain-ban", "add-domain-form", "confirm-domain-delete",
	"get-report-email", "get-support-email", "link", "admin", "url-password", "url-info", "api",
	"static", "images", "privacy", "protected", "css", "fonts", "libs", "pricing",
}

var preserved = func() map[string]struct{} {
	m := make(map[string]struct{}, len(PreservedURLs))
	for _, p := range PreservedURLs {
		m[p] = struct{}{}
	}
	return m
}()

// IsPreserved reports whether s is a reserved first path segment.
func IsPreserved(s string) bool { _, ok := preserved[s]; return ok }

// RemoveWww ports utils.removeWww: String.replace("www.", "") replaces the
// FIRST occurrence anywhere in the string, not just a prefix.
func RemoveWww(host string) string {
	i := strings.Index(host, "www.")
	if i < 0 {
		return host
	}
	return host[:i] + host[i+4:]
}

// Register mounts GET /{id} and POST /api{,/v2}/links/{id}/protected on mux.
// withPages also mounts the server-rendered GET /404 and GET /banned (the
// standalone redirector; in the API/all-in-one process the SPA owns them).
func (h *Handler) Register(mux *http.ServeMux, withPages bool) {
	mux.HandleFunc("GET /{id}", h.ServeRedirect)
	mux.HandleFunc("POST /api/v2/links/{id}/protected", h.ServeProtected)
	mux.HandleFunc("POST /api/links/{id}/protected", h.ServeProtected)
	if withPages {
		mux.HandleFunc("GET /404", h.ServeNotFound)
		mux.HandleFunc("GET /banned", h.ServeBanned)
	}
}

func (h *Handler) count(outcome string) {
	if h.o.Metrics != nil {
		h.o.Metrics.Redirects.WithLabelValues(outcome).Inc()
	}
}

func (h *Handler) lookup(result string) {
	if h.o.Metrics != nil {
		h.o.Metrics.RedirectCache.WithLabelValues(result).Inc()
	}
}

// resolveDomain returns the custom domain for the Host header (nil for the
// default domain or an unknown host, exactly like Kutt's query.domain.find).
func (h *Handler) resolveDomain(ctx context.Context, rawHost string) (*domain.Domain, error) {
	host := RemoveWww(rawHost)
	if host == h.o.DefaultDomain || h.o.Domains == nil {
		return nil, nil
	}
	d, err := h.o.Domains.ResolveHost(ctx, host)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return d, err
}

// CustomDomainHomepage wraps next with redirectCustomDomainHomepage: on a
// custom domain, "/" and any path whose first segment is preserved redirect
// (302) to the domain's homepage when it has one.
func (h *Handler) CustomDomainHomepage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.o.Domains == nil || RemoveWww(r.Host) == h.o.DefaultDomain {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		seg := strings.TrimPrefix(p, "/")
		if i := strings.IndexByte(seg, '/'); i >= 0 {
			seg = seg[:i]
		}
		if p == "/" || IsPreserved(seg) {
			d, err := h.resolveDomain(r.Context(), r.Host)
			if err == nil && d != nil && d.Homepage != nil && *d.Homepage != "" {
				h.count("homepage")
				redirect302(w, *d.Homepage)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ServeRedirect is GET /{id}.
func (h *Handler) ServeRedirect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Kutt: req.path.replace("/", "") in preservedURLs -> next() -> 404 page.
	if IsPreserved(id) {
		h.ServeNotFound(w, r)
		return
	}
	ctx := r.Context()
	d, err := h.resolveDomain(ctx, r.Host)
	if err != nil {
		h.serverError(w, r)
		return
	}
	var domainID *int64
	if d != nil {
		domainID = &d.ID
	}
	// req.params.id.replace("+", "") removes the first "+" only.
	address := id
	if i := strings.IndexByte(id, '+'); i >= 0 {
		address = id[:i] + id[i+1:]
	}
	t, err := h.o.Links.Resolve(ctx, address, domainID)
	switch {
	case errors.Is(err, domain.ErrNotFound) || (err == nil && t == nil):
		h.lookup("miss")
		t = nil
	case err != nil:
		h.lookup("error")
		h.serverError(w, r)
		return
	default:
		h.lookup("hit")
	}
	// Expired but not yet swept (the sweeper runs every 30s, cron.js): behave
	// as if already deleted. Kutt itself does not check expiry here.
	if t != nil && t.ExpireIn != nil && !t.ExpireIn.After(h.o.Now()) {
		t = nil
		h.count("expired")
	}
	if t == nil {
		if d != nil && d.Homepage != nil && *d.Homepage != "" {
			h.count("homepage")
			redirect302(w, *d.Homepage)
			return
		}
		h.count("notfound")
		redirect302(w, "/404")
		return
	}
	if t.Banned {
		h.count("banned")
		redirect302(w, "/banned")
		return
	}
	// /.*\+$/ : link info when the id ends with "+" and the link is not protected.
	if strings.HasSuffix(id, "+") && !t.HasPassword {
		h.count("info")
		if IsHTML(r) {
			h.pages.info(w, t.Target, h.shortLink(address, d))
			return
		}
		writeJSONTarget(w, http.StatusOK, t.Target)
		return
	}
	if t.HasPassword {
		// HTTP Basic auth unlock (links.handler.js:507-523); no visit recorded.
		if pw, ok := basicPassword(r); ok {
			if l, err := h.o.Finder.Find(ctx, store.LinkFilter{ID: &t.LinkID}); err == nil && l != nil && l.Password != nil &&
				bcrypt.CompareHashAndPassword([]byte(*l.Password), trunc72(pw)) == nil {
				h.count("target")
				redirect302(w, t.Target)
				return
			}
		}
		h.count("protected")
		h.pages.protected(w, http.StatusOK, t.UUID, "")
		return
	}
	if t.UserID != nil {
		ua := r.Header.Get("User-Agent")
		if !h.o.IsBot(ua) {
			h.publish(r, t.LinkID, t.UserID, ua)
		}
	}
	h.count("target")
	redirect302(w, t.Target)
}

func (h *Handler) publish(r *http.Request, linkID int64, userID *int64, ua string) {
	if h.o.Publisher == nil {
		return
	}
	ev := domain.VisitEvent{V: 1, LinkID: linkID, UserID: userID, Timestamp: h.o.Now().UTC()}
	ev.Browser, ev.OS = h.o.ParseUA(ua)
	if h.o.Geo != nil {
		var hg domain.HeaderGetter = r.Header
		ip := ""
		if h.o.IPs != nil {
			hg = h.o.IPs.GeoHeaders(r)
			ip = h.o.IPs.IP(r)
		} else {
			ip = remoteHost(r.RemoteAddr)
		}
		ev.Country = h.o.Geo.Country(hg, ip)
	}
	ev.Referrer = ReferrerHost(r.Header.Get("Referer"))
	h.o.Publisher.Publish(ev)
	if h.o.Metrics != nil {
		h.o.Metrics.VisitsPublished.Inc()
	}
}

func (h *Handler) shortLink(address string, d *domain.Domain) string {
	if d != nil {
		return d.Address + "/" + address
	}
	return h.o.DefaultDomain + "/" + address
}

// ServeNotFound renders the 404 page with status 404 (renders.notFound).
func (h *Handler) ServeNotFound(w http.ResponseWriter, r *http.Request) {
	h.pages.notFound(w)
}

// ServeBanned renders the banned page (renders.banned, status 200).
func (h *Handler) ServeBanned(w http.ResponseWriter, r *http.Request) {
	h.pages.banned(w)
}

func (h *Handler) serverError(w http.ResponseWriter, r *http.Request) {
	if IsHTML(r) {
		h.pages.errorPage(w)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"error":"An error occurred."}`))
}

// ---------- helpers ----------

var locationKey = "Location"

// redirect302 mirrors Express res.redirect(url): 302 with an encodeurl-encoded
// Location. The body is omitted (Express sends a tiny text body; clients ignore it).
func redirect302(w http.ResponseWriter, target string) {
	h := w.Header()
	h[locationKey] = []string{EncodeURL(target)}
	w.WriteHeader(http.StatusFound)
}

func trunc72(pw string) []byte {
	b := []byte(pw)
	if len(b) > 72 {
		b = b[:72]
	}
	return b
}

// basicPassword parses "Authorization: Basic base64(user:pass)" like
// links.handler.js:507-523 (split on the first space and first colon).
func basicPassword(r *http.Request) (string, bool) {
	a := r.Header.Get("Authorization")
	i := strings.IndexByte(a, ' ')
	if i < 0 || a[:i] != "Basic" {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(a[i+1:]))
	if err != nil {
		// Buffer.from(x, "base64") is lenient; accept unpadded too.
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(a[i+1:]), "="))
		if err != nil {
			return "", false
		}
	}
	s := string(raw)
	c := strings.IndexByte(s, ':')
	if c < 0 {
		return "", false
	}
	return s[c+1:], true
}

func remoteHost(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		addr = addr[:i]
	}
	return strings.Trim(addr, "[]")
}

// ReferrerHost ports `removeWww(URL.parse(referrer).hostname)`: the
// lower-cased hostname of an absolute URL, "" (Direct) otherwise.
func ReferrerHost(ref string) string {
	if ref == "" {
		return ""
	}
	i := strings.Index(ref, "://")
	if i <= 0 {
		return ""
	}
	for j := 0; j < i; j++ {
		c := ref[j]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
			return ""
		}
	}
	rest := ref[i+3:]
	end := len(rest)
	for k := 0; k < len(rest); k++ {
		switch rest[k] {
		case '/', '?', '#', '\\':
			end = k
			k = len(rest)
		}
	}
	host := rest[:end]
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if strings.HasPrefix(host, "[") {
		if j := strings.IndexByte(host, ']'); j > 0 {
			host = host[1:j]
		}
	} else if c := strings.LastIndexByte(host, ':'); c >= 0 {
		host = host[:c]
	}
	return RemoveWww(strings.ToLower(host))
}
