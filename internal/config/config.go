// Package config loads Tusk configuration from the environment.
//
// Every Kutt v3.2.6 variable (server/env.js) is honoured with the same name,
// type and default so an existing Kutt .env works unchanged. Each variable
// also accepts a <NAME>_FILE form (Docker secrets), matching Kutt.
//
// Tusk-only additions are grouped at the bottom (GEO_*, SERVICE_*, cache, events).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// GeoProvider selects how a visitor's country is resolved.
type GeoProvider string

const (
	// GeoAuto: trusted CDN header first, then the GeoIP file if configured, else "unknown".
	// This mirrors Kutt, which used cf-ipcountry and fell back to geoip-lite.
	GeoAuto GeoProvider = "auto"
	// GeoCloudflare: only the CDN country header (GEO_HEADER, default CF-IPCountry).
	GeoCloudflare GeoProvider = "cloudflare"
	// GeoIPFile: only a local MaxMind/DB-IP .mmdb file (GEOIP_DB_PATH).
	GeoIPFile GeoProvider = "geoip"
	// GeoNone: country tracking disabled; every visit is recorded as "unknown".
	GeoNone GeoProvider = "none"
)

// Config is the full runtime configuration. Field comments give the env var.
type Config struct {
	Port          int    // PORT (3000)
	SiteName      string // SITE_NAME (Kutt)
	DefaultDomain string // DEFAULT_DOMAIN (localhost:3000)
	LinkLength    int    // LINK_LENGTH (6)
	LinkAlphabet  string // LINK_CUSTOM_ALPHABET
	TrustProxy    bool   // TRUST_PROXY (true)

	DBClient   string // DB_CLIENT: pg|pg-native|sqlite3|better-sqlite3|mysql|mysql2 (better-sqlite3)
	DBFilename string // DB_FILENAME (db/data)
	DBHost     string // DB_HOST (localhost)
	DBPort     int    // DB_PORT (5432)
	DBName     string // DB_NAME (kutt)
	DBUser     string // DB_USER (postgres)
	DBPassword string // DB_PASSWORD
	DBSSL      bool   // DB_SSL (false)
	DBPoolMin  int    // DB_POOL_MIN (0)
	DBPoolMax  int    // DB_POOL_MAX (10)

	RedisEnabled  bool   // REDIS_ENABLED (false)
	RedisHost     string // REDIS_HOST (127.0.0.1)
	RedisPort     int    // REDIS_PORT (6379)
	RedisPassword string // REDIS_PASSWORD
	RedisDB       int    // REDIS_DB (0)

	DisallowAnonymousLinks bool   // DISALLOW_ANONYMOUS_LINKS (true)
	DisallowRegistration   bool   // DISALLOW_REGISTRATION (true)
	DisallowLoginForm      bool   // DISALLOW_LOGIN_FORM (false)
	ServerIPAddress        string // SERVER_IP_ADDRESS
	ServerCNAMEAddress     string // SERVER_CNAME_ADDRESS
	CustomDomainUseHTTPS   bool   // CUSTOM_DOMAIN_USE_HTTPS (false)
	JWTSecret              string // JWT_SECRET (required in production; "securekey" in dev)

	MailEnabled  bool   // MAIL_ENABLED
	MailHost     string // MAIL_HOST
	MailPort     int    // MAIL_PORT (587)
	MailSecure   bool   // MAIL_SECURE
	MailUser     string // MAIL_USER
	MailFrom     string // MAIL_FROM
	MailPassword string // MAIL_PASSWORD

	OIDCEnabled      bool   // OIDC_ENABLED
	OIDCIssuer       string // OIDC_ISSUER
	OIDCPrompt       string // OIDC_PROMPT
	OIDCClientID     string // OIDC_CLIENT_ID
	OIDCClientSecret string // OIDC_CLIENT_SECRET
	OIDCScope        string // OIDC_SCOPE (openid profile email)
	OIDCEmailClaim   string // OIDC_EMAIL_CLAIM (email)
	OIDCButtonText   string // OIDC_BUTTON_TEXT (Log in with OIDC)

	EnableRateLimit bool   // ENABLE_RATE_LIMIT (false)
	ReportEmail     string // REPORT_EMAIL
	ContactEmail    string // CONTACT_EMAIL
	Production      bool   // NODE_ENV=production or TUSK_ENV=production

	// ---- Tusk additions ----

	// Geo: see GeoProvider.
	GeoProvider GeoProvider // GEO_PROVIDER: auto|cloudflare|geoip|none (auto)
	GeoHeader   string      // GEO_HEADER: country header name (CF-IPCountry)
	GeoIPDBPath string      // GEOIP_DB_PATH: path to GeoLite2-Country.mmdb / dbip-country-lite.mmdb
	// GeoTrustCloudflareOnly: honour GEO_HEADER and CF-Connecting-IP only when the TCP peer
	// is inside Cloudflare's published IP ranges. Prevents header spoofing when the origin
	// is reachable directly. (true)
	GeoTrustCloudflareOnly bool // GEO_TRUST_CLOUDFLARE_ONLY
	// CloudflareIPRangesURL: refreshed every 24h; embedded list is the fallback.
	CloudflareIPRangesURLs []string // CLOUDFLARE_IP_RANGES_URLS (comma list)

	// Service role for the microservice split. "all" runs every role in one process.
	ServiceRoles []string // SERVICE_ROLES: all | api,redirector,worker (all)
	// Per-role listen ports when roles are split into separate processes.
	RedirectorPort int // REDIRECTOR_PORT (3001) — used only by cmd/redirector
	MetricsPort    int // METRICS_PORT (9090); 0 disables the separate metrics listener

	// Hot-path cache (in-process LRU in front of Redis/DB).
	LocalCacheSize int           // LOCAL_CACHE_SIZE entries (100000)
	LocalCacheTTL  time.Duration // LOCAL_CACHE_TTL (30s) — bounded staleness after edits on other nodes
	RedisCacheTTL  time.Duration // REDIS_CACHE_TTL (15m) — matches Kutt's 60*15

	// Visit event pipeline.
	VisitBatchSize     int           // VISIT_BATCH_SIZE (500) events folded per DB flush
	VisitFlushInterval time.Duration // VISIT_FLUSH_INTERVAL (1s)
	VisitBufferSize    int           // VISIT_BUFFER_SIZE (100000) in-memory channel capacity
	ExpiredLinkSweep   time.Duration // EXPIRED_LINK_SWEEP (30s) — Kutt cron.js interval

	StaticDir string // TUSK_CUSTOM_DIR (custom) — on-disk overrides for images/css
}

// Load reads configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.LookupEnv) }

// LoadFrom reads configuration using the given lookup (for tests).
func LoadFrom(lookup func(string) (string, bool)) (*Config, error) {
	e := &envReader{lookup: lookup}
	c := &Config{}

	c.Production = e.str("NODE_ENV", "") == "production" || e.str("TUSK_ENV", "") == "production"

	c.Port = e.int("PORT", 3000)
	c.SiteName = e.str("SITE_NAME", "Kutt")
	c.DefaultDomain = e.str("DEFAULT_DOMAIN", "localhost:3000")
	c.LinkLength = e.int("LINK_LENGTH", 6)
	c.LinkAlphabet = e.str("LINK_CUSTOM_ALPHABET", "abcdefghkmnpqrstuvwxyzABCDEFGHKLMNPQRSTUVWXYZ23456789")
	c.TrustProxy = e.bool("TRUST_PROXY", true)

	c.DBClient = e.str("DB_CLIENT", "better-sqlite3")
	c.DBFilename = e.str("DB_FILENAME", "db/data")
	c.DBHost = e.str("DB_HOST", "localhost")
	c.DBPort = e.int("DB_PORT", 5432)
	c.DBName = e.str("DB_NAME", "kutt")
	c.DBUser = e.str("DB_USER", "postgres")
	c.DBPassword = e.str("DB_PASSWORD", "")
	c.DBSSL = e.bool("DB_SSL", false)
	c.DBPoolMin = e.int("DB_POOL_MIN", 0)
	c.DBPoolMax = e.int("DB_POOL_MAX", 10)

	c.RedisEnabled = e.bool("REDIS_ENABLED", false)
	c.RedisHost = e.str("REDIS_HOST", "127.0.0.1")
	c.RedisPort = e.int("REDIS_PORT", 6379)
	c.RedisPassword = e.str("REDIS_PASSWORD", "")
	c.RedisDB = e.int("REDIS_DB", 0)

	c.DisallowAnonymousLinks = e.bool("DISALLOW_ANONYMOUS_LINKS", true)
	c.DisallowRegistration = e.bool("DISALLOW_REGISTRATION", true)
	c.DisallowLoginForm = e.bool("DISALLOW_LOGIN_FORM", false)
	c.ServerIPAddress = e.str("SERVER_IP_ADDRESS", "")
	c.ServerCNAMEAddress = e.str("SERVER_CNAME_ADDRESS", "")
	c.CustomDomainUseHTTPS = e.bool("CUSTOM_DOMAIN_USE_HTTPS", false)
	c.JWTSecret = e.str("JWT_SECRET", "")
	if c.JWTSecret == "" && !c.Production {
		c.JWTSecret = "securekey" // Kutt devDefault
	}

	c.MailEnabled = e.bool("MAIL_ENABLED", false)
	c.MailHost = e.str("MAIL_HOST", "")
	c.MailPort = e.int("MAIL_PORT", 587)
	c.MailSecure = e.bool("MAIL_SECURE", false)
	c.MailUser = e.str("MAIL_USER", "")
	c.MailFrom = e.str("MAIL_FROM", "")
	c.MailPassword = e.str("MAIL_PASSWORD", "")

	c.OIDCEnabled = e.bool("OIDC_ENABLED", false)
	c.OIDCIssuer = e.str("OIDC_ISSUER", "")
	c.OIDCPrompt = e.str("OIDC_PROMPT", "")
	c.OIDCClientID = e.str("OIDC_CLIENT_ID", "")
	c.OIDCClientSecret = e.str("OIDC_CLIENT_SECRET", "")
	c.OIDCScope = e.str("OIDC_SCOPE", "openid profile email")
	c.OIDCEmailClaim = e.str("OIDC_EMAIL_CLAIM", "email")
	c.OIDCButtonText = e.str("OIDC_BUTTON_TEXT", "Log in with OIDC")

	c.EnableRateLimit = e.bool("ENABLE_RATE_LIMIT", false)
	c.ReportEmail = e.str("REPORT_EMAIL", "")
	c.ContactEmail = e.str("CONTACT_EMAIL", "")

	c.GeoProvider = GeoProvider(strings.ToLower(e.str("GEO_PROVIDER", string(GeoAuto))))
	c.GeoHeader = e.str("GEO_HEADER", "CF-IPCountry")
	c.GeoIPDBPath = e.str("GEOIP_DB_PATH", "")
	c.GeoTrustCloudflareOnly = e.bool("GEO_TRUST_CLOUDFLARE_ONLY", true)
	c.CloudflareIPRangesURLs = e.list("CLOUDFLARE_IP_RANGES_URLS",
		[]string{"https://www.cloudflare.com/ips-v4", "https://www.cloudflare.com/ips-v6"})

	c.ServiceRoles = e.list("SERVICE_ROLES", []string{"all"})
	c.RedirectorPort = e.int("REDIRECTOR_PORT", 3001)
	c.MetricsPort = e.int("METRICS_PORT", 9090)

	c.LocalCacheSize = e.int("LOCAL_CACHE_SIZE", 100_000)
	c.LocalCacheTTL = e.dur("LOCAL_CACHE_TTL", 30*time.Second)
	c.RedisCacheTTL = e.dur("REDIS_CACHE_TTL", 15*time.Minute)

	c.VisitBatchSize = e.int("VISIT_BATCH_SIZE", 500)
	c.VisitFlushInterval = e.dur("VISIT_FLUSH_INTERVAL", time.Second)
	c.VisitBufferSize = e.int("VISIT_BUFFER_SIZE", 100_000)
	c.ExpiredLinkSweep = e.dur("EXPIRED_LINK_SWEEP", 30*time.Second)

	c.StaticDir = e.str("TUSK_CUSTOM_DIR", "custom")

	if len(e.errs) > 0 {
		return nil, errors.Join(e.errs...)
	}
	return c, c.Validate()
}

// Validate checks cross-field rules.
func (c *Config) Validate() error {
	var errs []error
	switch c.DBClient {
	case "pg", "pg-native", "sqlite3", "better-sqlite3", "mysql", "mysql2":
	default:
		errs = append(errs, fmt.Errorf("DB_CLIENT %q not supported", c.DBClient))
	}
	switch c.GeoProvider {
	case GeoAuto, GeoCloudflare, GeoNone:
	case GeoIPFile:
		if c.GeoIPDBPath == "" {
			errs = append(errs, errors.New("GEO_PROVIDER=geoip requires GEOIP_DB_PATH"))
		}
	default:
		errs = append(errs, fmt.Errorf("GEO_PROVIDER %q must be auto|cloudflare|geoip|none", c.GeoProvider))
	}
	if c.JWTSecret == "" {
		errs = append(errs, errors.New("JWT_SECRET is required in production"))
	}
	if c.LinkLength < 1 || c.LinkAlphabet == "" {
		errs = append(errs, errors.New("LINK_LENGTH must be >=1 and LINK_CUSTOM_ALPHABET non-empty"))
	}
	if c.SplitMode() && !c.RedisEnabled {
		errs = append(errs, errors.New("SERVICE_ROLES other than 'all' require REDIS_ENABLED=true (events and cache invalidation cross processes via Redis)"))
	}
	return errors.Join(errs...)
}

// HasRole reports whether this process should run the given role.
func (c *Config) HasRole(role string) bool {
	for _, r := range c.ServiceRoles {
		if r == "all" || r == role {
			return true
		}
	}
	return false
}

// SplitMode is true when the process does not run all roles.
func (c *Config) SplitMode() bool {
	for _, r := range c.ServiceRoles {
		if r == "all" {
			return false
		}
	}
	return true
}

// IsSQLite, IsPostgres, IsMySQL classify DB_CLIENT.
func (c *Config) IsSQLite() bool   { return c.DBClient == "sqlite3" || c.DBClient == "better-sqlite3" }
func (c *Config) IsPostgres() bool { return c.DBClient == "pg" || c.DBClient == "pg-native" }
func (c *Config) IsMySQL() bool    { return c.DBClient == "mysql" || c.DBClient == "mysql2" }

// RedisAddr returns host:port.
func (c *Config) RedisAddr() string { return fmt.Sprintf("%s:%d", c.RedisHost, c.RedisPort) }

type envReader struct {
	lookup func(string) (string, bool)
	errs   []error
}

// raw honours NAME_FILE (read + trim) like Kutt; empty values count as unset
// (Kutt deletes empty JWT_SECRET / LINK_CUSTOM_ALPHABET).
func (e *envReader) raw(key string) (string, bool) {
	if p, ok := e.lookup(key + "_FILE"); ok && p != "" {
		if b, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(b)), true
		}
	}
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

func (e *envReader) str(key, def string) string {
	if v, ok := e.raw(key); ok {
		return v
	}
	return def
}

func (e *envReader) int(key string, def int) int {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid number %q", key, v))
		return def
	}
	return n
}

// bool accepts the envalid set: true/false/1/0/yes/no/t/f/on/off.
func (e *envReader) bool(key string, def bool) bool {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "t", "y", "on":
		return true
	case "false", "0", "no", "f", "n", "off":
		return false
	}
	e.errs = append(e.errs, fmt.Errorf("%s: invalid boolean %q", key, v))
	return def
}

func (e *envReader) dur(key string, def time.Duration) time.Duration {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid duration %q", key, v))
		return def
	}
	return d
}

func (e *envReader) list(key string, def []string) []string {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
