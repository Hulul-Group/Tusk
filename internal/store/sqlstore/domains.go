package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

type domainStore struct{ s *DB }

const domainCols = `domains.id, domains.uuid, domains.address, domains.homepage, domains.user_id,
 domains.banned, domains.banned_by_id, domains.created_at, domains.updated_at`

func scanDomain(r rowScanner, extra ...any) (*domain.Domain, error) {
	var (
		d                domain.Domain
		home             sql.NullString
		userID, bannedBy sql.NullInt64
		banned           boolScan
		created, updated nullTime
	)
	if err := r.Scan(append([]any{&d.ID, &d.UUID, &d.Address, &home, &userID, &banned, &bannedBy, &created, &updated}, extra...)...); err != nil {
		return nil, err
	}
	d.Homepage, d.UserID, d.BannedByID = nsPtr(home), niPtr(userID), niPtr(bannedBy)
	d.Banned = bool(banned)
	d.CreatedAt, d.UpdatedAt = created.T, updated.T
	return &d, nil
}

func (s *DB) domainWhere(f store.DomainFilter) ([]string, []any, bool) {
	var w []string
	var args []any
	if f.ID != nil {
		w = append(w, "domains.id = ?")
		args = append(args, *f.ID)
	}
	if f.UUID != nil {
		if !s.validUUID(*f.UUID) {
			return nil, nil, false
		}
		w = append(w, "domains.uuid = ?")
		args = append(args, *f.UUID)
	}
	if f.Address != nil {
		w = append(w, "domains.address = ?")
		args = append(args, *f.Address)
	}
	if f.UserID != nil {
		w = append(w, "domains.user_id = ?")
		args = append(args, *f.UserID)
	}
	if f.Banned != nil {
		w = append(w, "domains.banned = ?")
		args = append(args, s.b(*f.Banned))
	}
	return w, args, true
}

// Find: domain.queries.js find:6-21.
func (ds *domainStore) Find(ctx context.Context, f store.DomainFilter) (*domain.Domain, error) {
	s := ds.s
	w, args, ok := s.domainWhere(f)
	if !ok || len(w) == 0 {
		return nil, domain.ErrNotFound
	}
	d, err := scanDomain(s.db.QueryRowContext(ctx, s.q("SELECT "+domainCols+" FROM domains"+whereSQL(w)+" LIMIT 1"), args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return d, err
}

// List: domain.queries.js get:23-25 (knex default order = insertion/id).
func (ds *domainStore) List(ctx context.Context, f store.DomainFilter) ([]domain.Domain, error) {
	s := ds.s
	w, args, ok := s.domainWhere(f)
	if !ok {
		return []domain.Domain{}, nil
	}
	rows, err := s.db.QueryContext(ctx, s.q("SELECT "+domainCols+" FROM domains"+whereSQL(w)+" ORDER BY domains.id"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Domain{}
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// Add: domain.queries.js add:27-63. Address lower-cased; if it exists the row is updated
// (homepage/user_id only when provided; banned reset to !!params.banned = false, as Kutt does),
// otherwise inserted.
func (ds *domainStore) Add(ctx context.Context, in store.UpsertDomain) (*domain.Domain, error) {
	s := ds.s
	addr := strings.ToLower(in.Address)
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, s.q("SELECT id FROM domains WHERE address = ?"+s.forUpdate()), addr).Scan(&id)
		now := s.ts(time.Now())
		if errors.Is(err, sql.ErrNoRows) {
			id, err = s.insertID(ctx, tx, s.q(`INSERT INTO domains (uuid, address, homepage, user_id, banned, created_at, updated_at)
 VALUES (?, ?, ?, ?, ?, ?, ?)`), uuid.NewString(), addr, strPtr(in.Homepage), i64Ptr(in.UserID), s.b(false), now, now)
			return err
		}
		if err != nil {
			return err
		}
		set := []string{"address = ?", "banned = ?", "updated_at = ?"}
		args := []any{addr, s.b(false), now}
		if in.Homepage != nil {
			set = append(set, "homepage = ?")
			args = append(args, *in.Homepage)
		}
		if in.UserID != nil {
			set = append(set, "user_id = ?")
			args = append(args, *in.UserID)
		}
		_, err = tx.ExecContext(ctx, s.q("UPDATE domains SET "+strings.Join(set, ", ")+" WHERE id = ?"), append(args, id)...)
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, err
	}
	return ds.Find(ctx, store.DomainFilter{ID: &id})
}

func (s *DB) forUpdate() string {
	if s.d == dSQLite {
		return "" // sqlite: the IMMEDIATE transaction already holds the write lock
	}
	return " FOR UPDATE"
}

// Update: domain.queries.js update:65-85.
func (ds *domainStore) Update(ctx context.Context, f store.DomainFilter, in store.UpdateDomain) ([]domain.Domain, error) {
	s := ds.s
	w, wargs, ok := s.domainWhere(f)
	if !ok {
		return []domain.Domain{}, nil
	}
	if len(w) == 0 {
		return nil, errors.New("sqlstore: refusing to update all domains")
	}
	var set []string
	var args []any
	add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }
	if in.Homepage != nil {
		add("homepage", *in.Homepage)
	}
	if in.ClearUserID {
		add("user_id", nil)
	} else if in.UserID != nil {
		add("user_id", *in.UserID)
	}
	if in.Banned != nil {
		add("banned", s.b(*in.Banned))
	}
	if in.BannedByID != nil {
		add("banned_by_id", *in.BannedByID)
	}
	add("updated_at", s.ts(time.Now()))
	// Re-select by primary key: a filter on user_id would miss rows after ClearUserID
	// (Kutt re-selects with the same match; ids are the stable equivalent).
	var out []domain.Domain
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		ids, err := s.collectIDs(ctx, tx, "SELECT domains.id FROM domains"+whereSQL(w), wargs)
		if err != nil || len(ids) == 0 {
			return err
		}
		idArgs := make([]any, len(ids))
		for i, id := range ids {
			idArgs[i] = id
		}
		ph := placeholders(len(ids))
		if _, err := tx.ExecContext(ctx, s.q("UPDATE domains SET "+strings.Join(set, ", ")+" WHERE id IN ("+ph+")"),
			append(args, idArgs...)...); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, s.q("SELECT "+domainCols+" FROM domains WHERE id IN ("+ph+") ORDER BY id"), idArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDomain(rows)
			if err != nil {
				return err
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	if out == nil {
		out = []domain.Domain{}
	}
	return out, err
}

// Remove: domain.queries.js remove:204-212. links.domain_id has no ON DELETE rule, so the
// caller must batchRemove the domain's links first (domains.handler.js:97-101) or the FK fails.
func (ds *domainStore) Remove(ctx context.Context, id int64) error {
	s := ds.s
	res, err := s.db.ExecContext(ctx, s.q("DELETE FROM domains WHERE id = ?"), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// adminDomainWhere: domains.handler.js getAdmin:115-131 + domain.queries.js getAdmin/totalAdmin.
func (s *DB) adminDomainWhere(f store.AdminDomainFilter) ([]string, []any) {
	var w []string
	var args []any
	if f.Banned != nil {
		w = append(w, "domains.banned = ?")
		args = append(args, s.b(*f.Banned))
	}
	if f.Owner != nil {
		if *f.Owner {
			w = append(w, "domains.user_id IS NOT NULL")
		} else {
			w = append(w, "domains.user_id IS NULL")
		}
	}
	if f.User != "" {
		if id, ok := jsParseInt(f.User); ok {
			w = append(w, "domains.user_id = ?")
			args = append(args, id)
		} else {
			w = append(w, s.like("users.email"))
			args = append(args, "%"+f.User+"%")
		}
	}
	if f.Search != "" {
		w = append(w, s.like("concat_ws(' ', domains.address, domains.homepage)"))
		args = append(args, "%"+f.Search+"%")
	}
	return w, args
}

const adminDomainFrom = ` FROM domains
 LEFT JOIN (SELECT domain_id, COUNT(*) AS links_count FROM links GROUP BY domain_id) l ON domains.id = l.domain_id
 LEFT JOIN users ON domains.user_id = users.id`

func (ds *domainStore) ListAdmin(ctx context.Context, f store.AdminDomainFilter, p store.Page) ([]store.AdminDomain, error) {
	s := ds.s
	w, args := s.adminDomainWhere(f)
	limit, skip := pageArgs(p)
	args = append(args, limit, skip)
	rows, err := s.db.QueryContext(ctx, s.q("SELECT "+domainCols+", users.email, l.links_count"+adminDomainFrom+
		whereSQL(w)+" ORDER BY domains.id DESC LIMIT ? OFFSET ?"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AdminDomain{}
	for rows.Next() {
		var (
			email sql.NullString
			lc    sql.NullInt64
		)
		d, err := scanDomain(rows, &email, &lc)
		if err != nil {
			return nil, err
		}
		out = append(out, store.AdminDomain{Domain: *d, Email: nsPtr(email), LinksCount: lc.Int64})
	}
	return out, rows.Err()
}

func (ds *domainStore) TotalAdmin(ctx context.Context, f store.AdminDomainFilter) (int64, error) {
	s := ds.s
	w, args := s.adminDomainWhere(f)
	var n int64
	err := s.db.QueryRowContext(ctx, s.q("SELECT COUNT(*)"+adminDomainFrom+whereSQL(w)), args...).Scan(&n)
	return n, err
}

// ---------- hosts ----------

type hostStore struct{ s *DB }

// Find: host.queries.js find:6-22.
func (hs *hostStore) Find(ctx context.Context, address string) (*domain.Host, error) {
	s := hs.s
	var (
		h                domain.Host
		banned           boolScan
		bannedBy         sql.NullInt64
		created, updated nullTime
	)
	err := s.db.QueryRowContext(ctx, s.q("SELECT id, address, banned, banned_by_id, created_at, updated_at FROM hosts WHERE address = ? LIMIT 1"), address).
		Scan(&h.ID, &h.Address, &banned, &bannedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	h.Banned, h.BannedByID = bool(banned), niPtr(bannedBy)
	h.CreatedAt, h.UpdatedAt = created.T, updated.T
	return &h, nil
}

// Ban: host.queries.js add:24-56 with { banned: true, banned_by_id }.
func (hs *hostStore) Ban(ctx context.Context, address string, bannedByID int64) error {
	s := hs.s
	addr := strings.ToLower(address)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var id int64
		now := s.ts(time.Now())
		err := tx.QueryRowContext(ctx, s.q("SELECT id FROM hosts WHERE address = ?"+s.forUpdate()), addr).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, s.q("INSERT INTO hosts (address, banned, banned_by_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)"),
				addr, s.b(true), bannedByID, now, now)
			return err
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.q("UPDATE hosts SET banned = ?, banned_by_id = ?, updated_at = ? WHERE id = ?"),
			s.b(true), bannedByID, now, id)
		return err
	})
	if isUniqueViolation(err) {
		return hs.Ban(ctx, address, bannedByID) // concurrent insert won; update it
	}
	return err
}
