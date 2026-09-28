package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// BcryptCost matches Kutt (link.queries.js:204, auth.handler.js:98).
const BcryptCost = 12

type linkStore struct{ s *DB }

// selectable (link.queries.js:10-25): links.* + domains.address as domain.
const linkCols = `links.id, links.uuid, links.address, links.target, links.description, links.password,
 links.expire_in, links.user_id, links.domain_id, links.banned, links.banned_by_id, links.visit_count,
 links.created_at, links.updated_at, domains.address`

const linkFrom = ` FROM links LEFT JOIN domains ON links.domain_id = domains.id`

type rowScanner interface{ Scan(dest ...any) error }

func scanLink(r rowScanner, extra ...any) (*domain.Link, error) {
	var (
		l                            domain.Link
		desc, pw, dom                sql.NullString
		userID, domainID, bannedByID sql.NullInt64
		expire, created, updated     nullTime
		banned                       boolScan
	)
	dest := []any{&l.ID, &l.UUID, &l.Address, &l.Target, &desc, &pw, &expire, &userID, &domainID,
		&banned, &bannedByID, &l.VisitCount, &created, &updated, &dom}
	if err := r.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	l.Description, l.Password, l.DomainAddress = nsPtr(desc), nsPtr(pw), nsPtr(dom)
	l.UserID, l.DomainID, l.BannedByID = niPtr(userID), niPtr(domainID), niPtr(bannedByID)
	l.ExpireIn = expire.ptr()
	l.CreatedAt, l.UpdatedAt = created.T, updated.T
	l.Banned = bool(banned)
	return &l, nil
}

// validUUID guards postgres uuid columns: a malformed id would be a 22P02 error in pg
// (a 500 in Kutt); Tusk treats it as "not found".
func (s *DB) validUUID(u string) bool {
	if s.d != dPostgres {
		return true
	}
	_, err := uuid.Parse(u)
	return err == nil
}

// linkWhere builds normalizeMatch (link.queries.js:31-60). ok=false means "can't match".
func (s *DB) linkWhere(f store.LinkFilter, w *[]string, args *[]any) bool {
	if f.ID != nil {
		*w = append(*w, "links.id = ?")
		*args = append(*args, *f.ID)
	}
	if f.UUID != nil {
		if !s.validUUID(*f.UUID) {
			return false
		}
		*w = append(*w, "links.uuid = ?")
		*args = append(*args, *f.UUID)
	}
	if f.Address != nil {
		*w = append(*w, "links.address = ?")
		*args = append(*args, *f.Address)
	}
	if f.UserID != nil {
		*w = append(*w, "links.user_id = ?")
		*args = append(*args, *f.UserID)
	}
	if f.DefaultDomain {
		*w = append(*w, "links.domain_id IS NULL")
	} else if f.DomainID != nil {
		*w = append(*w, "links.domain_id = ?")
		*args = append(*args, *f.DomainID)
	}
	if f.Banned != nil {
		*w = append(*w, "links.banned = ?")
		*args = append(*args, s.b(*f.Banned))
	}
	return true
}

func whereSQL(w []string) string {
	if len(w) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w, " AND ")
}

func (ls *linkStore) Find(ctx context.Context, f store.LinkFilter) (*domain.Link, error) {
	s := ls.s
	var w []string
	var args []any
	if !s.linkWhere(f, &w, &args) {
		return nil, domain.ErrNotFound
	}
	l, err := scanLink(s.db.QueryRowContext(ctx, s.q("SELECT "+linkCols+linkFrom+whereSQL(w)+" LIMIT 1"), args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return l, err
}

// userSearch: concat_ws(' ', description, links.address, target, domains.address) (link.queries.js:127).
func (s *DB) linkSearch(search string, admin bool, w *[]string, args *[]any) {
	if search == "" {
		return
	}
	expr := "concat_ws(' ', links.description, links.address, links.target, domains.address)"
	if admin { // link.queries.js:158 — admin search omits domains.address
		expr = "concat_ws(' ', links.description, links.address, links.target)"
	}
	*w = append(*w, s.like(expr))
	*args = append(*args, "%"+search+"%")
}

func pageArgs(p store.Page) (int, int) {
	limit, skip := p.Limit, p.Skip
	if limit <= 0 {
		limit = 10
	}
	if skip < 0 {
		skip = 0
	}
	return limit, skip
}

func (ls *linkStore) List(ctx context.Context, f store.LinkFilter, search string, p store.Page) ([]domain.Link, error) {
	s := ls.s
	var w []string
	var args []any
	if !s.linkWhere(f, &w, &args) {
		return nil, nil
	}
	s.linkSearch(search, false, &w, &args)
	limit, skip := pageArgs(p)
	args = append(args, limit, skip)
	rows, err := s.db.QueryContext(ctx, s.q("SELECT "+linkCols+linkFrom+whereSQL(w)+
		" ORDER BY links.id DESC LIMIT ? OFFSET ?"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Link{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (ls *linkStore) Total(ctx context.Context, f store.LinkFilter, search string) (int64, error) {
	s := ls.s
	var w []string
	var args []any
	if !s.linkWhere(f, &w, &args) {
		return 0, nil
	}
	s.linkSearch(search, false, &w, &args)
	var n int64
	err := s.db.QueryRowContext(ctx, s.q("SELECT COUNT(*)"+linkFrom+whereSQL(w)), args...).Scan(&n)
	return n, err
}

// adminWhere: links.handler.js getAdmin:51-75 + link.queries.js getAdmin/totalAdmin.
func (s *DB) adminLinkWhere(f store.AdminLinkFilter, w *[]string, args *[]any) bool {
	if !s.linkWhere(f.LinkFilter, w, args) {
		return false
	}
	if f.Anonymous != nil {
		if *f.Anonymous {
			*w = append(*w, "links.user_id IS NULL")
		} else {
			*w = append(*w, "links.user_id IS NOT NULL")
		}
	}
	if f.HasDomain != nil {
		if *f.HasDomain {
			*w = append(*w, "links.domain_id IS NOT NULL")
		} else {
			*w = append(*w, "links.domain_id IS NULL")
		}
	}
	if f.User != "" {
		if id, ok := jsParseInt(f.User); ok {
			*w = append(*w, "links.user_id = ?")
			*args = append(*args, id)
		} else {
			*w = append(*w, s.like("users.email"))
			*args = append(*args, "%"+f.User+"%")
		}
	}
	s.linkSearch(f.Search, true, w, args)
	if f.Domain != "" {
		*w = append(*w, s.like("domains.address"))
		*args = append(*args, "%"+f.Domain+"%")
	}
	return true
}

const adminLinkFrom = linkFrom + ` LEFT JOIN users ON links.user_id = users.id`

func (ls *linkStore) ListAdmin(ctx context.Context, f store.AdminLinkFilter, p store.Page) ([]store.AdminLink, error) {
	s := ls.s
	var w []string
	var args []any
	if !s.adminLinkWhere(f, &w, &args) {
		return nil, nil
	}
	limit, skip := pageArgs(p)
	args = append(args, limit, skip)
	rows, err := s.db.QueryContext(ctx, s.q("SELECT "+linkCols+", users.email"+adminLinkFrom+whereSQL(w)+
		" ORDER BY links.id DESC LIMIT ? OFFSET ?"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AdminLink{}
	for rows.Next() {
		var email sql.NullString
		l, err := scanLink(rows, &email)
		if err != nil {
			return nil, err
		}
		out = append(out, store.AdminLink{Link: *l, Email: nsPtr(email)})
	}
	return out, rows.Err()
}

func (ls *linkStore) TotalAdmin(ctx context.Context, f store.AdminLinkFilter) (int64, error) {
	s := ls.s
	var w []string
	var args []any
	if !s.adminLinkWhere(f, &w, &args) {
		return 0, nil
	}
	var n int64
	err := s.db.QueryRowContext(ctx, s.q("SELECT COUNT(*)"+adminLinkFrom+whereSQL(w)), args...).Scan(&n)
	return n, err
}

// HashPassword hashes with bcrypt cost 12 (bcryptjs-compatible $2a$ verification works in Go).
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), BcryptCost)
	return string(h), err
}

// Create: link.queries.js create:193-228.
func (ls *linkStore) Create(ctx context.Context, in store.CreateLink) (*domain.Link, error) {
	s := ls.s
	var pw any
	if in.Password != nil && *in.Password != "" { // `if (params.password)`
		h, err := HashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		pw = h
	}
	var desc any
	if in.Description != nil && *in.Description != "" { // `params.description || null`
		desc = *in.Description
	}
	domainID := i64PtrNonZero(in.DomainID) // `params.domain_id || null`
	userID := i64PtrNonZero(in.UserID)
	now := time.Now()
	id, err := s.insertID(ctx, s.db, s.q(`INSERT INTO links
 (uuid, password, domain_id, user_id, address, description, expire_in, target, created_at, updated_at)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		uuid.NewString(), pw, domainID, userID, in.Address, desc, s.tsPtr(in.ExpireIn), in.Target, s.ts(now), s.ts(now))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, err
	}
	return ls.Find(ctx, store.LinkFilter{ID: &id})
}

func i64PtrNonZero(p *int64) any {
	if p == nil || *p == 0 {
		return nil
	}
	return *p
}

// Update: link.queries.js update:258-288. Updates rows matching f, sets updated_at, and
// returns the re-selected rows (joined with domains).
func (ls *linkStore) Update(ctx context.Context, f store.LinkFilter, in store.UpdateLink) ([]domain.Link, error) {
	s := ls.s
	var set []string
	var sargs []any
	add := func(col string, v any) { set = append(set, col+" = ?"); sargs = append(sargs, v) }
	if in.Address != nil {
		add("address", *in.Address)
	}
	if in.Target != nil {
		add("target", *in.Target)
	}
	if in.Description != nil {
		add("description", *in.Description)
	}
	if in.ClearPassword {
		add("password", nil)
	} else if in.Password != nil && *in.Password != "" {
		h, err := HashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		add("password", h)
	}
	if in.ClearExpireIn {
		add("expire_in", nil)
	} else if in.ExpireIn != nil {
		add("expire_in", s.ts(*in.ExpireIn))
	}
	if in.Banned != nil {
		add("banned", s.b(*in.Banned))
	}
	if in.BannedByID != nil {
		add("banned_by_id", *in.BannedByID)
	}
	add("updated_at", s.ts(time.Now()))

	var w []string
	var wargs []any
	if !s.linkWhere(f, &w, &wargs) {
		return []domain.Link{}, nil
	}
	if len(w) == 0 {
		return nil, errors.New("sqlstore: refusing to update all links")
	}
	// UPDATE cannot join in a portable way; filter by the (qualified) links columns only.
	if _, err := s.db.ExecContext(ctx, s.q("UPDATE links SET "+strings.Join(set, ", ")+whereSQL(w)),
		append(sargs, wargs...)...); err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, err
	}
	// Re-select by the same match (Kutt semantics: an address change makes an
	// address-based match miss the row, exactly like Kutt).
	f2 := f
	if in.Address != nil && f.Address != nil && f.ID == nil && f.UUID == nil {
		f2.Address = in.Address
	}
	return ls.list(ctx, f2)
}

func (ls *linkStore) list(ctx context.Context, f store.LinkFilter) ([]domain.Link, error) {
	s := ls.s
	var w []string
	var args []any
	if !s.linkWhere(f, &w, &args) {
		return []domain.Link{}, nil
	}
	rows, err := s.db.QueryContext(ctx, s.q("SELECT "+linkCols+linkFrom+whereSQL(w)+" ORDER BY links.id"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Link{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// Remove: link.queries.js remove:230-244 / batchRemove:246-256. Visits are deleted
// explicitly as well as by the ON DELETE CASCADE FK (legacy DBs may lack the FK).
func (ls *linkStore) Remove(ctx context.Context, f store.LinkFilter) (bool, error) {
	s := ls.s
	var w []string
	var args []any
	if !s.linkWhere(f, &w, &args) {
		return false, nil
	}
	if len(w) == 0 {
		return false, errors.New("sqlstore: refusing to remove all links")
	}
	var removed bool
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		ids, err := s.collectIDs(ctx, tx, "SELECT links.id FROM links"+whereSQL(w), args)
		if err != nil || len(ids) == 0 {
			return err
		}
		removed = true
		return s.deleteLinks(ctx, tx, ids)
	})
	return removed, err
}

func (s *DB) collectIDs(ctx context.Context, x querier, query string, args []any) ([]int64, error) {
	rows, err := x.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *DB) deleteLinks(ctx context.Context, x querier, ids []int64) error {
	for len(ids) > 0 {
		n := min(len(ids), 500)
		chunk := ids[:n]
		ids = ids[n:]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		ph := placeholders(len(chunk))
		if _, err := x.ExecContext(ctx, s.q("DELETE FROM visits WHERE link_id IN ("+ph+")"), args...); err != nil {
			return err
		}
		if _, err := x.ExecContext(ctx, s.q("DELETE FROM links WHERE id IN ("+ph+")"), args...); err != nil {
			return err
		}
	}
	return nil
}

// RemoveExpired: cron.js — batchRemove({ expire_in: ["<", dateToUTC(new Date())] }).
// Works in chunks so a large backlog doesn't hold one giant transaction.
func (ls *linkStore) RemoveExpired(ctx context.Context, now time.Time) ([]store.LinkKey, error) {
	s := ls.s
	var keys []store.LinkKey
	for {
		var batch []store.LinkKey
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, s.q("SELECT id, address, domain_id FROM links WHERE expire_in < ? ORDER BY id LIMIT 500"), s.ts(now))
			if err != nil {
				return err
			}
			var ids []int64
			for rows.Next() {
				var (
					id  int64
					k   store.LinkKey
					dom sql.NullInt64
				)
				if err := rows.Scan(&id, &k.Address, &dom); err != nil {
					rows.Close()
					return err
				}
				k.DomainID = niPtr(dom)
				ids = append(ids, id)
				batch = append(batch, k)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			return s.deleteLinks(ctx, tx, ids)
		})
		if err != nil {
			return keys, err
		}
		keys = append(keys, batch...)
		if len(batch) < 500 {
			return keys, nil
		}
	}
}

// Resolve is the redirect hot path: link.queries.js find({address, domain_id}) projected
// to what the redirector needs. Uses links_address_index.
func (ls *linkStore) Resolve(ctx context.Context, address string, domainID *int64) (*domain.RedirectTarget, error) {
	s := ls.s
	var (
		t      domain.RedirectTarget
		userID sql.NullInt64
		pw     sql.NullString
		expire nullTime
		banned boolScan
		row    *sql.Row
	)
	const cols = "SELECT id, uuid, user_id, target, banned, password, expire_in FROM links WHERE address = ? AND "
	if domainID == nil {
		row = s.db.QueryRowContext(ctx, s.q(cols+"domain_id IS NULL LIMIT 1"), address)
	} else {
		row = s.db.QueryRowContext(ctx, s.q(cols+"domain_id = ? LIMIT 1"), address, *domainID)
	}
	if err := row.Scan(&t.LinkID, &t.UUID, &userID, &t.Target, &banned, &pw, &expire); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	t.UserID = niPtr(userID)
	t.Banned = bool(banned)
	t.HasPassword = pw.Valid && pw.String != "" // Kutt: `if (link.password)`
	t.ExpireIn = expire.ptr()
	return &t, nil
}

func (ls *linkStore) AddressTaken(ctx context.Context, address string, domainID *int64) (bool, error) {
	s := ls.s
	var (
		one int
		err error
	)
	if domainID == nil {
		err = s.db.QueryRowContext(ctx, s.q("SELECT 1 FROM links WHERE address = ? AND domain_id IS NULL LIMIT 1"), address).Scan(&one)
	} else {
		err = s.db.QueryRowContext(ctx, s.q("SELECT 1 FROM links WHERE address = ? AND domain_id = ? LIMIT 1"), address, *domainID).Scan(&one)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
