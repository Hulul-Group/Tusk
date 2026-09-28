// Package store defines the persistence contract. Implementations live in
// internal/store/sqlstore (one implementation over database/sql, three dialects:
// postgres via pgx stdlib, mysql via go-sql-driver, sqlite via modernc.org/sqlite).
//
// Semantics follow Kutt server/queries/*.js exactly; the SRS reference for each
// method is given in its comment (docs/srs/part-A-data.md §4).
//
// OWNERSHIP: Agent A (data) implements. Agent B (api) consumes and must not
// add methods without recording them in docs/CONTRACTS.md.
package store

import (
	"context"
	"time"

	"github.com/Hulul-Group/Tusk/internal/domain"
)

// Page is Kutt's skip/limit pagination (req.context.limit/skip, limit default 10, max 50).
type Page struct {
	Skip  int
	Limit int
}

// ---------- Links ----------

// LinkFilter matches Kutt normalizeMatch keys. Nil fields are ignored.
type LinkFilter struct {
	ID       *int64
	UUID     *string
	Address  *string
	UserID   *int64
	DomainID *int64 // use DefaultDomain=true to match domain_id IS NULL
	// DefaultDomain restricts to links with domain_id IS NULL.
	DefaultDomain bool
	Banned        *bool
}

// AdminLinkFilter adds the admin-list filters (links.handler.js getAdmin).
type AdminLinkFilter struct {
	LinkFilter
	Anonymous *bool  // user_id IS NULL / IS NOT NULL
	HasDomain *bool  // domain_id IS NOT NULL / IS NULL
	Search    string // ILIKE on concat_ws(' ', description, address, target)
	User      string // numeric => links.user_id = N, else users.email ILIKE %x%
	Domain    string // domains.address ILIKE %x%
}

type CreateLink struct {
	Address     string
	Target      string
	Description *string
	Password    *string // plaintext; store hashes with bcrypt cost 12
	ExpireIn    *time.Time
	UserID      *int64
	DomainID    *int64
}

// UpdateLink: nil = unchanged. ClearX = set NULL.
type UpdateLink struct {
	Address       *string
	Target        *string
	Description   *string
	Password      *string // plaintext; hashed on write
	ClearPassword bool
	ExpireIn      *time.Time
	ClearExpireIn bool
	Banned        *bool
	BannedByID    *int64
}

type LinkStore interface {
	// Find returns one link joined with domains.address (link.queries.js find). ErrNotFound if none.
	Find(ctx context.Context, f LinkFilter) (*domain.Link, error)
	// List: user dashboard (get). Search = ILIKE concat_ws(description, address, target, domains.address). Ordered id DESC.
	List(ctx context.Context, f LinkFilter, search string, p Page) ([]domain.Link, error)
	Total(ctx context.Context, f LinkFilter, search string) (int64, error)
	ListAdmin(ctx context.Context, f AdminLinkFilter, p Page) ([]AdminLink, error)
	TotalAdmin(ctx context.Context, f AdminLinkFilter) (int64, error)
	Create(ctx context.Context, in CreateLink) (*domain.Link, error)
	Update(ctx context.Context, f LinkFilter, in UpdateLink) ([]domain.Link, error)
	// Remove deletes matching link(s) and their visits. Returns false if none.
	Remove(ctx context.Context, f LinkFilter) (bool, error)
	// RemoveExpired deletes links with expire_in < now (cron.js). Returns deleted (address, domain_id) for cache invalidation.
	RemoveExpired(ctx context.Context, now time.Time) ([]LinkKey, error)
	// Resolve is the redirect hot path: single indexed lookup by (address, domain_id).
	Resolve(ctx context.Context, address string, domainID *int64) (*domain.RedirectTarget, error)
	// AddressTaken checks custom-address uniqueness within a domain (default domain = domain_id IS NULL).
	AddressTaken(ctx context.Context, address string, domainID *int64) (bool, error)
}

type LinkKey struct {
	Address  string
	DomainID *int64
}

type AdminLink struct {
	domain.Link
	Email *string // users.email
}

// ---------- Users ----------

type UserFilter struct {
	ID                 *int64
	Email              *string
	APIKey             *string
	VerificationToken  *string
	ResetPasswordToken *string
	ChangeEmailToken   *string
	// TokenNotExpired: when set, the corresponding *_expires column must be > this time.
	TokenNotExpired *time.Time
}

type AdminUserFilter struct {
	Role     *domain.Role
	Banned   *bool
	Verified *bool
	Search   string // id or email ILIKE
	Domain   string // users owning a domain ILIKE
}

type AdminUser struct {
	domain.User
	LinksCount int64
	Domains    []string
}

// UpdateUser: nil = unchanged, Clear* = set NULL.
type UpdateUser struct {
	Email                *string
	Password             *string // already-hashed
	APIKey               *string
	Role                 *domain.Role
	Verified             *bool
	Banned               *bool
	BannedByID           *int64
	VerificationToken    *string
	VerificationExpires  *time.Time
	ResetPasswordToken   *string
	ResetPasswordExpires *time.Time
	ChangeEmailToken     *string
	ChangeEmailExpires   *time.Time
	ChangeEmailAddress   *string
	ClearVerification    bool
	ClearResetPassword   bool
	ClearChangeEmail     bool
}

type CreateUser struct {
	Email    string
	Password string // already-hashed
	Role     domain.Role
	Verified bool
	// VerificationToken/Expires set when mail verification is on.
	VerificationToken   *string
	VerificationExpires *time.Time
}

type UserStore interface {
	Find(ctx context.Context, f UserFilter) (*domain.User, error)
	Create(ctx context.Context, in CreateUser) (*domain.User, error)
	Update(ctx context.Context, f UserFilter, in UpdateUser) (*domain.User, error)
	// Remove deletes the user; links/visits cascade; domains get user_id NULL (user.queries.js remove).
	Remove(ctx context.Context, id int64) error
	// Any reports whether at least one user exists (create-admin flow).
	Any(ctx context.Context) (bool, error)
	ListAdmin(ctx context.Context, f AdminUserFilter, p Page) ([]AdminUser, error)
	TotalAdmin(ctx context.Context, f AdminUserFilter) (int64, error)
}

// ---------- Domains ----------

type DomainFilter struct {
	ID      *int64
	UUID    *string
	Address *string
	UserID  *int64
	Banned  *bool
}

type AdminDomainFilter struct {
	Banned *bool
	Owner  *bool  // user_id IS NOT NULL / IS NULL
	Search string // address ILIKE
	User   string // numeric id or email ILIKE
}

type AdminDomain struct {
	domain.Domain
	Email      *string
	LinksCount int64
}

type UpsertDomain struct {
	Address  string
	Homepage *string
	UserID   *int64
}

type UpdateDomain struct {
	Homepage    *string
	UserID      *int64
	ClearUserID bool
	Banned      *bool
	BannedByID  *int64
}

type DomainStore interface {
	Find(ctx context.Context, f DomainFilter) (*domain.Domain, error)
	List(ctx context.Context, f DomainFilter) ([]domain.Domain, error)
	// Add inserts or, if the address exists without owner, claims it (domain.queries.js add).
	Add(ctx context.Context, in UpsertDomain) (*domain.Domain, error)
	Update(ctx context.Context, f DomainFilter, in UpdateDomain) ([]domain.Domain, error)
	Remove(ctx context.Context, id int64) error
	ListAdmin(ctx context.Context, f AdminDomainFilter, p Page) ([]AdminDomain, error)
	TotalAdmin(ctx context.Context, f AdminDomainFilter) (int64, error)
}

// ---------- Hosts / IPs (ban lists) ----------

type HostStore interface {
	Find(ctx context.Context, address string) (*domain.Host, error)
	// Ban upserts the host as banned (host.queries.js add).
	Ban(ctx context.Context, address string, bannedByID int64) error
}

// ---------- Visits ----------

// VisitDelta is a pre-aggregated increment for one (link, hour) bucket.
// The worker folds many VisitEvents into one delta before touching the DB.
type VisitDelta struct {
	LinkID    int64
	UserID    *int64
	Hour      time.Time // UTC, truncated to the hour
	Total     int64
	Browser   map[string]int64 // keys: chrome edge firefox ie opera other safari
	OS        map[string]int64 // keys: android ios linux macos other windows
	Countries map[string]int64 // lower-case alpha-2 or "unknown"
	Referrers map[string]int64 // lower-case host with "." replaced by "[dot]", or "direct"
}

type VisitStore interface {
	// ApplyDeltas upserts each bucket in one transaction per delta (row lock), merges JSON maps,
	// and increments links.visit_count by Total. Must be safe to run on N worker replicas.
	ApplyDeltas(ctx context.Context, deltas []VisitDelta) error
	// Stats computes the Kutt stats payload (visit.queries.js find) for a link.
	Stats(ctx context.Context, linkID int64, now time.Time) (*Stats, error)
}

// Stats is the exact JSON shape Kutt returns for GET /api/v2/links/:id/stats (minus link fields).
type Stats struct {
	LastDay   PeriodStats `json:"lastDay"`
	LastWeek  PeriodStats `json:"lastWeek"`
	LastMonth PeriodStats `json:"lastMonth"`
	LastYear  PeriodStats `json:"lastYear"`
	UpdatedAt time.Time   `json:"updatedAt"`
}

type PeriodStats struct {
	Stats StatBuckets `json:"stats"`
	Views []int64     `json:"views"`
	Total int64       `json:"total"`
}

type StatBuckets struct {
	Browser  []NameValue `json:"browser"`
	OS       []NameValue `json:"os"`
	Country  []NameValue `json:"country"`
	Referrer []NameValue `json:"referrer"`
}

type NameValue struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// ---------- Aggregate ----------

// Store bundles all repositories plus lifecycle.
type Store interface {
	Links() LinkStore
	Users() UserStore
	Domains() DomainStore
	Hosts() HostStore
	Visits() VisitStore
	// Migrate verifies/creates schema. On an existing Kutt DB it checks knex_migrations
	// contains 20250106070444_remove_cooldown.js and refuses otherwise; on an empty DB it
	// creates the final Kutt schema AND a knex_migrations table so Node Kutt can still run on it.
	Migrate(ctx context.Context) error
	Ping(ctx context.Context) error
	Close() error
}
