// Package geo resolves a visitor's country (GEO-001..005, docs/srs/00-overview.md item 4)
// and extracts the trusted client IP.
//
// Kutt: country = (req.get("cf-ipcountry") || geoip.lookup(ip)?.country)?.slice(0,2).toUpperCase()
// || "Unknown" (links.handler.js:537, queues/visit.js:20). Tusk makes the source configurable.
//
// Wiring (redirect handler):
//
//	cip := geo.NewClientIP(cfg); go cip.Run(ctx)
//	res, err := geo.New(cfg)                 // startup error if provider=geoip and the file is bad
//	ip := cip.IP(r)
//	country := res.Country(cip.GeoHeaders(r), ip)   // "" = unknown
package geo

import (
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"

	"github.com/oschwald/geoip2-golang/v2"

	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
)

// New builds the resolver for cfg.GeoProvider.
func New(cfg *config.Config) (domain.GeoResolver, error) {
	switch cfg.GeoProvider {
	case config.GeoNone:
		return None{}, nil
	case config.GeoCloudflare:
		return NewHeader(cfg.GeoHeader), nil
	case config.GeoIPFile:
		if cfg.GeoIPDBPath == "" {
			return nil, fmt.Errorf("geo: GEO_PROVIDER=geoip requires GEOIP_DB_PATH")
		}
		// Missing/corrupt file is a startup error only for provider=geoip.
		m, err := OpenMMDB(cfg.GeoIPDBPath)
		if err != nil {
			return nil, err
		}
		return m, nil
	case config.GeoAuto, "":
		chain := Chain{NewHeader(cfg.GeoHeader)}
		if cfg.GeoIPDBPath != "" {
			// auto: lazily opened; a missing file only disables the fallback.
			chain = append(chain, LazyMMDB(cfg.GeoIPDBPath))
		}
		return chain, nil
	}
	return nil, fmt.Errorf("geo: unknown GEO_PROVIDER %q", cfg.GeoProvider)
}

// normalize applies Kutt's `.slice(0, 2).toUpperCase()` and GEO-002's unknown values.
func normalize(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 2 {
		v = v[:2]
	}
	if len(v) != 2 {
		return ""
	}
	a, b := upper(v[0]), upper(v[1])
	if a < 'A' || a > 'Z' {
		return "" // GEO: only letters; "T1" (Tor) and junk -> unknown
	}
	if b < 'A' || b > 'Z' {
		return ""
	}
	if a == 'X' && b == 'X' {
		return ""
	}
	if v[0] == a && v[1] == b {
		return v
	}
	return string([]byte{a, b})
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

// None: country tracking disabled.
type None struct{}

func (None) Country(domain.HeaderGetter, string) string { return "" }
func (None) Name() string                               { return "none" }

// Header reads a CDN country header (default CF-IPCountry).
type Header struct{ name string }

func NewHeader(name string) Header {
	if name == "" {
		name = "CF-IPCountry"
	}
	return Header{name: http.CanonicalHeaderKey(name)}
}

func (h Header) Country(hg domain.HeaderGetter, _ string) string {
	if hg == nil {
		return ""
	}
	return normalize(hg.Get(h.name))
}
func (h Header) Name() string { return "cloudflare" }

// Chain returns the first non-empty answer (GEO-004 auto).
type Chain []domain.GeoResolver

func (c Chain) Country(hg domain.HeaderGetter, ip string) string {
	for _, r := range c {
		if v := r.Country(hg, ip); v != "" {
			return v
		}
	}
	return ""
}
func (Chain) Name() string { return "auto" }

// MMDB resolves via a MaxMind GeoLite2-Country / DB-IP country-lite .mmdb file.
type MMDB struct{ r *geoip2.Reader }

// OpenMMDB opens the database eagerly.
func OpenMMDB(path string) (*MMDB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("geo: GEOIP_DB_PATH: %w", err)
	}
	r, err := geoip2.Open(path)
	if err != nil {
		return nil, fmt.Errorf("geo: open %s: %w", path, err)
	}
	return &MMDB{r: r}, nil
}

func (m *MMDB) Country(_ domain.HeaderGetter, ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	rec, err := m.r.Country(a.Unmap())
	if err != nil || rec == nil {
		return ""
	}
	return normalize(rec.Country.ISOCode)
}
func (*MMDB) Name() string   { return "geoip" }
func (m *MMDB) Close() error { return m.r.Close() }

// lazyMMDB opens the file on first use; if it cannot be opened it stays disabled.
type lazyMMDB struct {
	path string
	once sync.Once
	m    *MMDB
}

// LazyMMDB returns a resolver that opens path on first lookup.
func LazyMMDB(path string) domain.GeoResolver { return &lazyMMDB{path: path} }

func (l *lazyMMDB) Country(hg domain.HeaderGetter, ip string) string {
	l.once.Do(func() {
		m, err := OpenMMDB(l.path)
		if err == nil {
			l.m = m
		}
	})
	if l.m == nil {
		return ""
	}
	return l.m.Country(hg, ip)
}
func (*lazyMMDB) Name() string { return "geoip" }

type emptyHeaders struct{}

func (emptyHeaders) Get(string) string { return "" }

// GeoHeaders returns r.Header when the geo header may be trusted for this request
// (GEO_TRUST_CLOUDFLARE_ONLY=false, or the request came through a Cloudflare edge),
// otherwise an empty getter so spoofed CF-IPCountry values are ignored.
func (c *ClientIP) GeoHeaders(r *http.Request) domain.HeaderGetter {
	if c.TrustGeoHeader(r) {
		return r.Header
	}
	return emptyHeaders{}
}
