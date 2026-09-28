// Package apitest provides an in-memory store.Store and resolver fakes for
// Tusk HTTP handler tests. Semantics follow Kutt's queries closely enough for
// handler tests; it is not a substitute for sqlstore's own tests.
package apitest

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// Store is a mutex-guarded in-memory store.
type Store struct {
	mu      sync.Mutex
	Now     func() time.Time
	users   []*domain.User
	links   []*domain.Link
	domains []*domain.Domain
	hosts   []*domain.Host
	Visits  map[int64]*store.Stats // canned stats per link id
	nextID  int64
	// BcryptCost for link passwords (tests use 4 for speed).
	BcryptCost int
}

// New returns an empty store with a fixed clock.
func New(now time.Time) *Store {
	return &Store{Now: func() time.Time { return now }, Visits: map[int64]*store.Stats{}, BcryptCost: 4}
}

func (s *Store) id() int64 { s.nextID++; return s.nextID }

func (s *Store) Links() store.LinkStore        { return (*links)(s) }
func (s *Store) Users() store.UserStore        { return (*users)(s) }
func (s *Store) Domains() store.DomainStore    { return (*domains)(s) }
func (s *Store) Hosts() store.HostStore        { return (*hosts)(s) }
func (s *Store) Migrate(context.Context) error { return nil }
func (s *Store) Ping(context.Context) error    { return nil }
func (s *Store) Close() error                  { return nil }

// Visits satisfies store.Store (method name clash with the field is avoided
// by the wrapper type).
type storeWithVisits struct{ *Store }

func (s storeWithVisits) Visits() store.VisitStore { return (*visits)(s.Store) }

// AsStore returns s as a store.Store.
func (s *Store) AsStore() store.Store { return storeWithVisits{s} }

func cp[T any](v *T) *T {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// ---------- users ----------

type users Store

func (u *users) match(x *domain.User, f store.UserFilter) bool {
	if f.ID != nil && x.ID != *f.ID {
		return false
	}
	if f.Email != nil && x.Email != *f.Email {
		return false
	}
	if f.APIKey != nil && (x.APIKey == nil || *x.APIKey != *f.APIKey) {
		return false
	}
	if f.VerificationToken != nil && (x.VerificationToken == nil || *x.VerificationToken != *f.VerificationToken ||
		(f.TokenNotExpired != nil && (x.VerificationExpires == nil || !x.VerificationExpires.After(*f.TokenNotExpired)))) {
		return false
	}
	if f.ResetPasswordToken != nil && (x.ResetPasswordToken == nil || *x.ResetPasswordToken != *f.ResetPasswordToken ||
		(f.TokenNotExpired != nil && (x.ResetPasswordExpires == nil || !x.ResetPasswordExpires.After(*f.TokenNotExpired)))) {
		return false
	}
	if f.ChangeEmailToken != nil && (x.ChangeEmailToken == nil || *x.ChangeEmailToken != *f.ChangeEmailToken ||
		(f.TokenNotExpired != nil && (x.ChangeEmailExpires == nil || !x.ChangeEmailExpires.After(*f.TokenNotExpired)))) {
		return false
	}
	return true
}

func (u *users) Find(_ context.Context, f store.UserFilter) (*domain.User, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, x := range u.users {
		if u.match(x, f) {
			return cp(x), nil
		}
	}
	return nil, domain.ErrNotFound
}

func (u *users) Create(_ context.Context, in store.CreateUser) (*domain.User, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, x := range u.users {
		if x.Email == in.Email {
			return nil, domain.ErrConflict
		}
	}
	s := (*Store)(u)
	now := s.Now()
	role := in.Role
	if role == "" {
		role = domain.RoleUser
	}
	x := &domain.User{ID: s.id(), Email: in.Email, Password: in.Password, Role: role, Verified: in.Verified,
		VerificationToken: in.VerificationToken, VerificationExpires: in.VerificationExpires, CreatedAt: now, UpdatedAt: now}
	u.users = append(u.users, x)
	return cp(x), nil
}

func (u *users) Update(_ context.Context, f store.UserFilter, in store.UpdateUser) (*domain.User, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, x := range u.users {
		if !u.match(x, f) {
			continue
		}
		set := func(dst **string, v *string) {
			if v != nil {
				*dst = cp(v)
			}
		}
		if in.Email != nil {
			x.Email = *in.Email
		}
		if in.Password != nil {
			x.Password = *in.Password
		}
		set(&x.APIKey, in.APIKey)
		if in.Role != nil {
			x.Role = *in.Role
		}
		if in.Verified != nil {
			x.Verified = *in.Verified
		}
		if in.Banned != nil {
			x.Banned = *in.Banned
		}
		if in.BannedByID != nil {
			x.BannedByID = cp(in.BannedByID)
		}
		set(&x.VerificationToken, in.VerificationToken)
		if in.VerificationExpires != nil {
			x.VerificationExpires = cp(in.VerificationExpires)
		}
		set(&x.ResetPasswordToken, in.ResetPasswordToken)
		if in.ResetPasswordExpires != nil {
			x.ResetPasswordExpires = cp(in.ResetPasswordExpires)
		}
		set(&x.ChangeEmailToken, in.ChangeEmailToken)
		if in.ChangeEmailExpires != nil {
			x.ChangeEmailExpires = cp(in.ChangeEmailExpires)
		}
		set(&x.ChangeEmailAddress, in.ChangeEmailAddress)
		if in.ClearVerification {
			x.VerificationToken, x.VerificationExpires = nil, nil
		}
		if in.ClearResetPassword {
			x.ResetPasswordToken, x.ResetPasswordExpires = nil, nil
		}
		if in.ClearChangeEmail {
			x.ChangeEmailToken, x.ChangeEmailExpires, x.ChangeEmailAddress = nil, nil, nil
		}
		x.UpdatedAt = (*Store)(u).Now()
		return cp(x), nil
	}
	return nil, domain.ErrNotFound
}

func (u *users) Remove(_ context.Context, id int64) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.users[:0]
	for _, x := range u.users {
		if x.ID != id {
			out = append(out, x)
		}
	}
	u.users = out
	ls := u.links[:0]
	for _, l := range u.links {
		if l.UserID == nil || *l.UserID != id {
			ls = append(ls, l)
		}
	}
	u.links = ls
	for _, d := range u.domains {
		if d.UserID != nil && *d.UserID == id {
			d.UserID = nil
		}
	}
	return nil
}

func (u *users) Any(context.Context) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.users) > 0, nil
}

func (u *users) adminList(f store.AdminUserFilter) []store.AdminUser {
	var out []store.AdminUser
	for i := len(u.users) - 1; i >= 0; i-- {
		x := u.users[i]
		if f.Role != nil && x.Role != *f.Role || f.Banned != nil && x.Banned != *f.Banned || f.Verified != nil && x.Verified != *f.Verified {
			continue
		}
		if f.Search != "" && !strings.Contains(strings.ToLower(x.Email), strings.ToLower(f.Search)) && strconv.FormatInt(x.ID, 10) != f.Search {
			continue
		}
		au := store.AdminUser{User: *x}
		for _, l := range u.links {
			if l.UserID != nil && *l.UserID == x.ID {
				au.LinksCount++
			}
		}
		for _, d := range u.domains {
			if d.UserID != nil && *d.UserID == x.ID {
				au.Domains = append(au.Domains, d.Address)
			}
		}
		out = append(out, au)
	}
	return out
}

func (u *users) ListAdmin(_ context.Context, f store.AdminUserFilter, p store.Page) ([]store.AdminUser, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return paginate(u.adminList(f), p), nil
}

func (u *users) TotalAdmin(_ context.Context, f store.AdminUserFilter) (int64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return int64(len(u.adminList(f))), nil
}

func paginate[T any](a []T, p store.Page) []T {
	if p.Skip >= len(a) {
		return []T{}
	}
	a = a[p.Skip:]
	if p.Limit > 0 && len(a) > p.Limit {
		a = a[:p.Limit]
	}
	return a
}

// ---------- links ----------

type links Store

func (l *links) domainAddr(id *int64) *string {
	if id == nil {
		return nil
	}
	for _, d := range l.domains {
		if d.ID == *id {
			return cp(&d.Address)
		}
	}
	return nil
}

func (l *links) match(x *domain.Link, f store.LinkFilter) bool {
	if f.ID != nil && x.ID != *f.ID || f.UUID != nil && x.UUID != *f.UUID || f.Address != nil && x.Address != *f.Address {
		return false
	}
	if f.UserID != nil && (x.UserID == nil || *x.UserID != *f.UserID) {
		return false
	}
	if f.DomainID != nil && (x.DomainID == nil || *x.DomainID != *f.DomainID) {
		return false
	}
	if f.DefaultDomain && x.DomainID != nil {
		return false
	}
	if f.Banned != nil && x.Banned != *f.Banned {
		return false
	}
	return true
}

func (l *links) out(x *domain.Link) *domain.Link {
	c := cp(x)
	c.DomainAddress = l.domainAddr(x.DomainID)
	return c
}

func (l *links) Find(_ context.Context, f store.LinkFilter) (*domain.Link, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, x := range l.links {
		if l.match(x, f) {
			return l.out(x), nil
		}
	}
	return nil, domain.ErrNotFound
}

func (l *links) filtered(f store.LinkFilter, search string) []domain.Link {
	var out []domain.Link
	for i := len(l.links) - 1; i >= 0; i-- {
		x := l.links[i]
		if !l.match(x, f) {
			continue
		}
		if search != "" {
			hay := strings.ToLower(x.Address + " " + x.Target)
			if x.Description != nil {
				hay += " " + strings.ToLower(*x.Description)
			}
			if !strings.Contains(hay, strings.ToLower(search)) {
				continue
			}
		}
		out = append(out, *l.out(x))
	}
	return out
}

func (l *links) List(_ context.Context, f store.LinkFilter, search string, p store.Page) ([]domain.Link, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return paginate(l.filtered(f, search), p), nil
}

func (l *links) Total(_ context.Context, f store.LinkFilter, search string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int64(len(l.filtered(f, search))), nil
}

func (l *links) adminList(f store.AdminLinkFilter) []store.AdminLink {
	var out []store.AdminLink
	for _, x := range l.filtered(f.LinkFilter, f.Search) {
		if f.Anonymous != nil && (x.UserID == nil) != *f.Anonymous {
			continue
		}
		if f.HasDomain != nil && (x.DomainID != nil) != *f.HasDomain {
			continue
		}
		al := store.AdminLink{Link: x}
		if x.UserID != nil {
			for _, u := range l.users {
				if u.ID == *x.UserID {
					al.Email = cp(&u.Email)
				}
			}
		}
		out = append(out, al)
	}
	return out
}

func (l *links) ListAdmin(_ context.Context, f store.AdminLinkFilter, p store.Page) ([]store.AdminLink, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return paginate(l.adminList(f), p), nil
}

func (l *links) TotalAdmin(_ context.Context, f store.AdminLinkFilter) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int64(len(l.adminList(f))), nil
}

func (l *links) hash(pw string) *string {
	h, _ := bcrypt.GenerateFromPassword([]byte(pw), (*Store)(l).BcryptCost)
	s := string(h)
	return &s
}

func (l *links) Create(_ context.Context, in store.CreateLink) (*domain.Link, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := (*Store)(l)
	now := s.Now()
	x := &domain.Link{ID: s.id(), UUID: uuid.NewString(), Address: in.Address, Target: in.Target,
		Description: cp(in.Description), ExpireIn: cp(in.ExpireIn), UserID: cp(in.UserID), DomainID: cp(in.DomainID),
		CreatedAt: now, UpdatedAt: now}
	if in.Password != nil {
		x.Password = l.hash(*in.Password)
	}
	l.links = append(l.links, x)
	return l.out(x), nil
}

func (l *links) Update(_ context.Context, f store.LinkFilter, in store.UpdateLink) ([]domain.Link, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []domain.Link
	for _, x := range l.links {
		if !l.match(x, f) {
			continue
		}
		if in.Address != nil {
			x.Address = *in.Address
		}
		if in.Target != nil {
			x.Target = *in.Target
		}
		if in.Description != nil {
			x.Description = cp(in.Description)
		}
		if in.Password != nil {
			x.Password = l.hash(*in.Password)
		}
		if in.ClearPassword {
			x.Password = nil
		}
		if in.ExpireIn != nil {
			x.ExpireIn = cp(in.ExpireIn)
		}
		if in.ClearExpireIn {
			x.ExpireIn = nil
		}
		if in.Banned != nil {
			x.Banned = *in.Banned
		}
		if in.BannedByID != nil {
			x.BannedByID = cp(in.BannedByID)
		}
		x.UpdatedAt = (*Store)(l).Now()
		out = append(out, *l.out(x))
	}
	return out, nil
}

func (l *links) Remove(_ context.Context, f store.LinkFilter) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, x := range l.links {
		if l.match(x, f) {
			l.links = append(l.links[:i], l.links[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (l *links) RemoveExpired(_ context.Context, now time.Time) ([]store.LinkKey, error) {
	return nil, nil
}

func (l *links) Resolve(_ context.Context, address string, domainID *int64) (*domain.RedirectTarget, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, x := range l.links {
		if x.Address == address && ((domainID == nil && x.DomainID == nil) || (domainID != nil && x.DomainID != nil && *x.DomainID == *domainID)) {
			return &domain.RedirectTarget{LinkID: x.ID, UUID: x.UUID, UserID: cp(x.UserID), Target: x.Target,
				Banned: x.Banned, HasPassword: x.Password != nil, ExpireIn: cp(x.ExpireIn)}, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (l *links) AddressTaken(ctx context.Context, address string, domainID *int64) (bool, error) {
	_, err := l.Resolve(ctx, address, domainID)
	return err == nil, nil
}

// ---------- domains ----------

type domains Store

func (d *domains) match(x *domain.Domain, f store.DomainFilter) bool {
	if f.ID != nil && x.ID != *f.ID || f.UUID != nil && x.UUID != *f.UUID || f.Address != nil && x.Address != *f.Address {
		return false
	}
	if f.UserID != nil && (x.UserID == nil || *x.UserID != *f.UserID) {
		return false
	}
	return f.Banned == nil || x.Banned == *f.Banned
}

func (d *domains) Find(_ context.Context, f store.DomainFilter) (*domain.Domain, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, x := range d.domains {
		if d.match(x, f) {
			return cp(x), nil
		}
	}
	return nil, domain.ErrNotFound
}

func (d *domains) List(_ context.Context, f store.DomainFilter) ([]domain.Domain, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []domain.Domain{}
	for _, x := range d.domains {
		if d.match(x, f) {
			out = append(out, *x)
		}
	}
	return out, nil
}

func (d *domains) Add(_ context.Context, in store.UpsertDomain) (*domain.Domain, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := (*Store)(d)
	addr := strings.ToLower(in.Address)
	for _, x := range d.domains {
		if x.Address == addr {
			x.Homepage, x.UserID, x.UpdatedAt = cp(in.Homepage), cp(in.UserID), s.Now()
			return cp(x), nil
		}
	}
	now := s.Now()
	x := &domain.Domain{ID: s.id(), UUID: uuid.NewString(), Address: addr, Homepage: cp(in.Homepage), UserID: cp(in.UserID), CreatedAt: now, UpdatedAt: now}
	d.domains = append(d.domains, x)
	return cp(x), nil
}

func (d *domains) Update(_ context.Context, f store.DomainFilter, in store.UpdateDomain) ([]domain.Domain, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []domain.Domain
	for _, x := range d.domains {
		if !d.match(x, f) {
			continue
		}
		if in.Homepage != nil {
			x.Homepage = cp(in.Homepage)
		}
		if in.UserID != nil {
			x.UserID = cp(in.UserID)
		}
		if in.ClearUserID {
			x.UserID = nil
		}
		if in.Banned != nil {
			x.Banned = *in.Banned
		}
		if in.BannedByID != nil {
			x.BannedByID = cp(in.BannedByID)
		}
		x.UpdatedAt = (*Store)(d).Now()
		out = append(out, *x)
	}
	return out, nil
}

func (d *domains) Remove(_ context.Context, id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, x := range d.domains {
		if x.ID == id {
			d.domains = append(d.domains[:i], d.domains[i+1:]...)
			return nil
		}
	}
	return nil
}

func (d *domains) adminList(f store.AdminDomainFilter) []store.AdminDomain {
	var out []store.AdminDomain
	for i := len(d.domains) - 1; i >= 0; i-- {
		x := d.domains[i]
		if f.Banned != nil && x.Banned != *f.Banned || f.Owner != nil && (x.UserID != nil) != *f.Owner {
			continue
		}
		if f.Search != "" && !strings.Contains(x.Address, f.Search) {
			continue
		}
		ad := store.AdminDomain{Domain: *x}
		for _, l := range d.links {
			if l.DomainID != nil && *l.DomainID == x.ID {
				ad.LinksCount++
			}
		}
		if x.UserID != nil {
			for _, u := range d.users {
				if u.ID == *x.UserID {
					ad.Email = cp(&u.Email)
				}
			}
		}
		out = append(out, ad)
	}
	return out
}

func (d *domains) ListAdmin(_ context.Context, f store.AdminDomainFilter, p store.Page) ([]store.AdminDomain, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return paginate(d.adminList(f), p), nil
}

func (d *domains) TotalAdmin(_ context.Context, f store.AdminDomainFilter) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return int64(len(d.adminList(f))), nil
}

// ---------- hosts ----------

type hosts Store

func (h *hosts) Find(_ context.Context, address string) (*domain.Host, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, x := range h.hosts {
		if x.Address == address {
			return cp(x), nil
		}
	}
	return nil, domain.ErrNotFound
}

func (h *hosts) Ban(_ context.Context, address string, by int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, x := range h.hosts {
		if x.Address == address {
			x.Banned, x.BannedByID = true, &by
			return nil
		}
	}
	h.hosts = append(h.hosts, &domain.Host{ID: (*Store)(h).id(), Address: address, Banned: true, BannedByID: &by})
	return nil
}

// ---------- visits ----------

type visits Store

func (v *visits) ApplyDeltas(context.Context, []store.VisitDelta) error { return nil }

func (v *visits) Stats(_ context.Context, linkID int64, now time.Time) (*store.Stats, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if st, ok := v.Visits[linkID]; ok {
		return st, nil
	}
	mk := func(n int) store.PeriodStats {
		return store.PeriodStats{Views: make([]int64, n), Stats: store.StatBuckets{
			Browser:  zeroes("chrome", "edge", "firefox", "ie", "opera", "other", "safari"),
			OS:       zeroes("android", "ios", "linux", "macos", "other", "windows"),
			Country:  []store.NameValue{},
			Referrer: []store.NameValue{},
		}}
	}
	return &store.Stats{LastDay: mk(24), LastWeek: mk(7), LastMonth: mk(30), LastYear: mk(12), UpdatedAt: now}, nil
}

func zeroes(names ...string) []store.NameValue {
	out := make([]store.NameValue, len(names))
	for i, n := range names {
		out[i] = store.NameValue{Name: n}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out
}

// ---------- seeding helpers ----------

// SeedUser inserts a user directly.
func (s *Store) SeedUser(u domain.User) *domain.User {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.ID == 0 {
		u.ID = s.id()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt, u.UpdatedAt = s.Now(), s.Now()
	}
	x := u
	s.users = append(s.users, &x)
	return cp(&x)
}

// SeedLink inserts a link directly (Password must already be hashed).
func (s *Store) SeedLink(l domain.Link) *domain.Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.ID == 0 {
		l.ID = s.id()
	}
	if l.UUID == "" {
		l.UUID = uuid.NewString()
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt, l.UpdatedAt = s.Now(), s.Now()
	}
	x := l
	s.links = append(s.links, &x)
	return cp(&x)
}

// SeedDomain inserts a domain directly.
func (s *Store) SeedDomain(d domain.Domain) *domain.Domain {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.ID == 0 {
		d.ID = s.id()
	}
	if d.UUID == "" {
		d.UUID = uuid.NewString()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt, d.UpdatedAt = s.Now(), s.Now()
	}
	x := d
	s.domains = append(s.domains, &x)
	return cp(&x)
}

// ---------- resolvers ----------

// Resolver is a LinkResolver/DomainResolver over the store that records invalidations.
type Resolver struct {
	S             *Store
	mu            sync.Mutex
	Invalidated   []string
	DomainInvalid []string
}

func key(address string, d *int64) string {
	if d == nil {
		return address + "@default"
	}
	return address + "@" + strconv.FormatInt(*d, 10)
}

func (r *Resolver) Resolve(ctx context.Context, address string, d *int64) (*domain.RedirectTarget, error) {
	return r.S.Links().Resolve(ctx, address, d)
}

func (r *Resolver) Invalidate(_ context.Context, address string, d *int64) error {
	r.mu.Lock()
	r.Invalidated = append(r.Invalidated, key(address, d))
	r.mu.Unlock()
	return nil
}

func (r *Resolver) ResolveHost(ctx context.Context, host string) (*domain.Domain, error) {
	return r.S.Domains().Find(ctx, store.DomainFilter{Address: &host})
}

// DomainResolver wraps Resolver's ResolveHost with Invalidate(address).
type DomainResolver struct{ *Resolver }

func (d DomainResolver) Invalidate(_ context.Context, address string) error {
	d.mu.Lock()
	d.DomainInvalid = append(d.DomainInvalid, address)
	d.mu.Unlock()
	return nil
}

// Invalidations returns a snapshot of link invalidation keys.
func (r *Resolver) Invalidations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.Invalidated...)
}

// DomainInvalidations returns a snapshot of domain invalidations.
func (r *Resolver) DomainInvalidations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.DomainInvalid...)
}
