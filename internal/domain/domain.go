// Package domain holds the entities and cross-service contracts shared by every
// Tusk service. It has NO dependencies outside the standard library so that the
// redirector, API and worker binaries can all import it.
//
// Entities mirror the Kutt v3.2.6 schema exactly (see docs/srs/part-A-data.md §2).
// The database is shared with a running Kutt instance during migration, so
// column names and semantics MUST NOT change.
package domain

import (
	"context"
	"errors"
	"time"
)

// ---------- Entities (1:1 with Kutt tables) ----------

type Role string

const (
	RoleUser  Role = "USER"
	RoleAdmin Role = "ADMIN"
)

type User struct {
	ID                   int64
	Email                string
	Password             string // bcrypt $2a$/$2b$, cost 12
	APIKey               *string
	Role                 Role
	Verified             bool
	Banned               bool
	BannedByID           *int64
	VerificationToken    *string
	VerificationExpires  *time.Time
	ResetPasswordToken   *string
	ResetPasswordExpires *time.Time
	ChangeEmailToken     *string
	ChangeEmailExpires   *time.Time
	ChangeEmailAddress   *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

type Domain struct {
	ID         int64
	UUID       string
	Address    string
	Homepage   *string
	UserID     *int64
	Banned     bool
	BannedByID *int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Link struct {
	ID          int64
	UUID        string
	Address     string
	Target      string // max 2040
	Description *string
	Password    *string // bcrypt hash; nil = not protected
	ExpireIn    *time.Time
	UserID      *int64
	DomainID    *int64
	Banned      bool
	BannedByID  *int64
	VisitCount  int64
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Joined, not stored on links.
	DomainAddress *string
}

// Host is a banned-destination-hostname record (Kutt "hosts" table).
type Host struct {
	ID         int64
	Address    string
	Banned     bool
	BannedByID *int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// IP is a banned IP record (Kutt "ips" table).
type IP struct {
	ID        int64
	IP        string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ---------- Redirect hot path ----------

// RedirectTarget is the minimal, cacheable projection the redirector needs.
// It is what goes into the local LRU and Redis; keep it small.
type RedirectTarget struct {
	LinkID      int64      `json:"id"`
	UUID        string     `json:"uuid"`
	UserID      *int64     `json:"user_id,omitempty"`
	Target      string     `json:"target"`
	Banned      bool       `json:"banned"`
	HasPassword bool       `json:"has_password"`
	ExpireIn    *time.Time `json:"expire_in,omitempty"`
}

// LinkResolver resolves (address, domain) to a redirect target. domainID nil = default domain.
// Implementations layer: local LRU -> Redis -> DB, with singleflight on misses.
// Returns ErrNotFound when absent.
type LinkResolver interface {
	Resolve(ctx context.Context, address string, domainID *int64) (*RedirectTarget, error)
	// Invalidate drops the entry everywhere (local, Redis, and peers via pub/sub).
	Invalidate(ctx context.Context, address string, domainID *int64) error
}

// DomainResolver maps a request Host header to a custom domain (nil for the default domain).
type DomainResolver interface {
	ResolveHost(ctx context.Context, host string) (*Domain, error)
}

// ---------- Visit events (redirector -> worker) ----------

// VisitEvent is emitted by the redirector for every successful human redirect.
// It is the ONLY coupling between redirector and worker. Wire format is JSON,
// field names are stable (version with V if they ever change).
type VisitEvent struct {
	V         int       `json:"v"` // schema version, currently 1
	LinkID    int64     `json:"link_id"`
	UserID    *int64    `json:"user_id,omitempty"`
	Country   string    `json:"country"`  // ISO-3166 alpha-2 upper-case, or "" if unknown (worker writes "unknown")
	Browser   string    `json:"browser"`  // chrome|edge|firefox|ie|opera|safari|other (Kutt buckets)
	OS        string    `json:"os"`       // android|ios|linux|macos|windows|other
	Referrer  string    `json:"referrer"` // hostname without www, or "" for Direct
	Timestamp time.Time `json:"ts"`       // UTC
}

// VisitPublisher is non-blocking: a redirect must never wait on analytics.
// When the buffer is full the event is dropped and a counter is incremented.
type VisitPublisher interface {
	Publish(ev VisitEvent)
	Close(ctx context.Context) error
}

// VisitConsumer delivers batches to the aggregator. Implementations:
// in-process channel (single binary) or Redis Streams consumer group (split mode).
type VisitConsumer interface {
	Run(ctx context.Context, handle func(ctx context.Context, batch []VisitEvent) error) error
}

// ---------- Geo ----------

// GeoResolver returns an upper-case alpha-2 country, or "" when unknown/disabled.
// Implementations: cloudflare header, GeoIP mmdb, chain (auto), noop (none).
// clientIP is already the trusted real client IP.
type GeoResolver interface {
	Country(headers HeaderGetter, clientIP string) string
	Name() string
}

// HeaderGetter is satisfied by http.Header; keeps this package stdlib-only and http-free.
type HeaderGetter interface {
	Get(key string) string
}

// ---------- Errors ----------

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
	ErrBanned       = errors.New("banned")
	ErrExpired      = errors.New("expired")
)

// AppError carries an HTTP status and user-facing message, like Kutt's CustomError.
type AppError struct {
	Status  int
	Message string
	Field   string // optional form field for htmx/form errors
}

func (e *AppError) Error() string { return e.Message }

func NewError(status int, msg string) *AppError { return &AppError{Status: status, Message: msg} }
