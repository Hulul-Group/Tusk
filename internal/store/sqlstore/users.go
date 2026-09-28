package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

type userStore struct{ s *DB }

const userCols = `users.id, users.email, users.password, users.apikey, users.role, users.verified, users.banned,
 users.banned_by_id, users.verification_token, users.verification_expires, users.reset_password_token,
 users.reset_password_expires, users.change_email_token, users.change_email_expires,
 users.change_email_address, users.created_at, users.updated_at`

func scanUser(r rowScanner, extra ...any) (*domain.User, error) {
	var (
		u                              domain.User
		apikey, vt, rpt, cet, cea      sql.NullString
		role                           string
		verified, banned               boolScan
		bannedBy                       sql.NullInt64
		ve, rpe, cee, created, updated nullTime
	)
	dest := []any{&u.ID, &u.Email, &u.Password, &apikey, &role, &verified, &banned, &bannedBy, &vt, &ve,
		&rpt, &rpe, &cet, &cee, &cea, &created, &updated}
	if err := r.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	u.APIKey, u.VerificationToken, u.ResetPasswordToken = nsPtr(apikey), nsPtr(vt), nsPtr(rpt)
	u.ChangeEmailToken, u.ChangeEmailAddress = nsPtr(cet), nsPtr(cea)
	u.Role = domain.Role(role)
	u.Verified, u.Banned = bool(verified), bool(banned)
	u.BannedByID = niPtr(bannedBy)
	u.VerificationExpires, u.ResetPasswordExpires, u.ChangeEmailExpires = ve.ptr(), rpe.ptr(), cee.ptr()
	u.CreatedAt, u.UpdatedAt = created.T, updated.T
	return &u, nil
}

// userWhere mirrors user.queries.js find(match): plain equality on the given columns.
// TokenNotExpired adds `<token>_expires > ?` for each token filter present
// (e.g. verify: { verification_token, verification_expires: [">", now] }).
func (s *DB) userWhere(f store.UserFilter) ([]string, []any) {
	var w []string
	var args []any
	eq := func(col string, v any) { w = append(w, col+" = ?"); args = append(args, v) }
	exp := func(col string) {
		if f.TokenNotExpired != nil {
			w = append(w, col+" > ?")
			args = append(args, s.ts(*f.TokenNotExpired))
		}
	}
	if f.ID != nil {
		eq("users.id", *f.ID)
	}
	if f.Email != nil {
		eq("users.email", *f.Email)
	}
	if f.APIKey != nil {
		eq("users.apikey", *f.APIKey)
	}
	if f.VerificationToken != nil {
		eq("users.verification_token", *f.VerificationToken)
		exp("users.verification_expires")
	}
	if f.ResetPasswordToken != nil {
		eq("users.reset_password_token", *f.ResetPasswordToken)
		exp("users.reset_password_expires")
	}
	if f.ChangeEmailToken != nil {
		eq("users.change_email_token", *f.ChangeEmailToken)
		exp("users.change_email_expires")
	}
	return w, args
}

func (us *userStore) Find(ctx context.Context, f store.UserFilter) (*domain.User, error) {
	s := us.s
	w, args := s.userWhere(f)
	if len(w) == 0 {
		return nil, domain.ErrNotFound
	}
	u, err := scanUser(s.db.QueryRowContext(ctx, s.q("SELECT "+userCols+" FROM users"+whereSQL(w)+" LIMIT 1"), args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return u, err
}

// Create: user.queries.js create:226-243 (+ verification fields from add:39-65).
func (us *userStore) Create(ctx context.Context, in store.CreateUser) (*domain.User, error) {
	s := us.s
	role := in.Role
	if role == "" {
		role = domain.RoleUser
	}
	now := time.Now()
	id, err := s.insertID(ctx, s.db, `INSERT INTO users
 (email, password, role, verified, banned, verification_token, verification_expires, created_at, updated_at)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Email, in.Password, string(role), s.b(in.Verified), s.b(false),
		strPtr(in.VerificationToken), s.tsPtr(in.VerificationExpires), s.ts(now), s.ts(now))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, err
	}
	return us.Find(ctx, store.UserFilter{ID: &id})
}

// Update: user.queries.js update:67-95 — in a transaction, pick the first matching id,
// update it (with updated_at), return the fresh row. ErrNotFound when nothing matches.
func (us *userStore) Update(ctx context.Context, f store.UserFilter, in store.UpdateUser) (*domain.User, error) {
	s := us.s
	w, wargs := s.userWhere(f)
	if len(w) == 0 {
		return nil, domain.ErrNotFound
	}
	var set []string
	var sargs []any
	add := func(col string, v any) { set = append(set, col+" = ?"); sargs = append(sargs, v) }
	if in.Email != nil {
		add("email", *in.Email)
	}
	if in.Password != nil {
		add("password", *in.Password)
	}
	if in.APIKey != nil {
		add("apikey", *in.APIKey)
	}
	if in.Role != nil {
		add("role", string(*in.Role))
	}
	if in.Verified != nil {
		add("verified", s.b(*in.Verified))
	}
	if in.Banned != nil {
		add("banned", s.b(*in.Banned))
	}
	if in.BannedByID != nil {
		add("banned_by_id", *in.BannedByID)
	}
	if in.ClearVerification {
		add("verification_token", nil)
		add("verification_expires", nil)
	} else {
		if in.VerificationToken != nil {
			add("verification_token", *in.VerificationToken)
		}
		if in.VerificationExpires != nil {
			add("verification_expires", s.ts(*in.VerificationExpires))
		}
	}
	if in.ClearResetPassword {
		add("reset_password_token", nil)
		add("reset_password_expires", nil)
	} else {
		if in.ResetPasswordToken != nil {
			add("reset_password_token", *in.ResetPasswordToken)
		}
		if in.ResetPasswordExpires != nil {
			add("reset_password_expires", s.ts(*in.ResetPasswordExpires))
		}
	}
	if in.ClearChangeEmail {
		add("change_email_token", nil)
		add("change_email_expires", nil)
		add("change_email_address", nil)
	} else {
		if in.ChangeEmailToken != nil {
			add("change_email_token", *in.ChangeEmailToken)
		}
		if in.ChangeEmailExpires != nil {
			add("change_email_expires", s.ts(*in.ChangeEmailExpires))
		}
		if in.ChangeEmailAddress != nil {
			add("change_email_address", *in.ChangeEmailAddress)
		}
	}
	add("updated_at", s.ts(time.Now()))

	var out *domain.User
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, s.q("SELECT users.id FROM users"+whereSQL(w)+" LIMIT 1"), wargs...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q("UPDATE users SET "+strings.Join(set, ", ")+" WHERE id = ?"),
			append(sargs, id)...); err != nil {
			if isUniqueViolation(err) {
				return domain.ErrConflict
			}
			return err
		}
		out, err = scanUser(tx.QueryRowContext(ctx, s.q("SELECT "+userCols+" FROM users WHERE id = ?"), id))
		return err
	})
	return out, err
}

// Remove: user.queries.js remove:97-105 relies on FK cascades (links, visits CASCADE;
// domains SET NULL). Done explicitly too, so legacy SQLite DBs created without enforced
// FKs behave the same. banned_by_id references are nulled first so removing an admin
// who banned things doesn't fail on the (NO ACTION) banned_by_id FKs (Kutt would 500).
func (us *userStore) Remove(ctx context.Context, id int64) error {
	s := us.s
	return s.inTx(ctx, func(tx *sql.Tx) error {
		stmts := []string{
			"UPDATE users SET banned_by_id = NULL WHERE banned_by_id = ? AND id <> ?",
			"UPDATE links SET banned_by_id = NULL WHERE banned_by_id = ?",
			"UPDATE domains SET banned_by_id = NULL WHERE banned_by_id = ?",
			"UPDATE hosts SET banned_by_id = NULL WHERE banned_by_id = ?",
			"DELETE FROM visits WHERE user_id = ?",
			"DELETE FROM visits WHERE link_id IN (SELECT id FROM links WHERE user_id = ?)",
			"DELETE FROM links WHERE user_id = ?",
			"UPDATE domains SET user_id = NULL WHERE user_id = ?",
		}
		for _, q := range stmts {
			args := []any{id}
			if strings.Count(q, "?") == 2 {
				args = append(args, id)
			}
			if _, err := tx.ExecContext(ctx, s.q(q), args...); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, s.q("DELETE FROM users WHERE id = ?"), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}

// Any: user.queries.js findAny.
func (us *userStore) Any(ctx context.Context) (bool, error) {
	var one int
	err := us.s.db.QueryRowContext(ctx, "SELECT 1 FROM users LIMIT 1").Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *DB) stringAgg(col string) string {
	switch s.d {
	case dMySQL:
		return "group_concat(" + col + " SEPARATOR ', ')"
	case dSQLite:
		return "group_concat(" + col + ", ', ')"
	}
	return "string_agg(" + col + ", ', ')"
}

// adminUserWhere: users.handler.js getAdmin:64-82 + user.queries.js getAdmin/totalAdmin.
func (s *DB) adminUserWhere(f store.AdminUserFilter) ([]string, []any) {
	var w []string
	var args []any
	if f.Role != nil {
		w = append(w, "users.role = ?")
		args = append(args, string(*f.Role))
	}
	if f.Verified != nil {
		w = append(w, "users.verified = ?")
		args = append(args, s.b(*f.Verified))
	}
	if f.Banned != nil {
		w = append(w, "users.banned = ?")
		args = append(args, s.b(*f.Banned))
	}
	if f.Search != "" {
		if id, ok := jsParseInt(f.Search); ok {
			w = append(w, "users.id = ?")
			args = append(args, id)
		} else {
			w = append(w, s.like("users.email"))
			args = append(args, "%"+f.Search+"%")
		}
	}
	if f.Domain != "" {
		w = append(w, s.like("d.domains"))
		args = append(args, "%"+f.Domain+"%")
	}
	return w, args
}

func (s *DB) adminUserFrom() string {
	return ` FROM users
 LEFT JOIN (SELECT user_id, ` + s.stringAgg("address") + ` AS domains FROM domains GROUP BY user_id) d ON users.id = d.user_id
 LEFT JOIN (SELECT user_id, COUNT(*) AS links_count FROM links GROUP BY user_id) l ON users.id = l.user_id`
}

func (us *userStore) ListAdmin(ctx context.Context, f store.AdminUserFilter, p store.Page) ([]store.AdminUser, error) {
	s := us.s
	w, args := s.adminUserWhere(f)
	limit, skip := pageArgs(p)
	args = append(args, limit, skip)
	rows, err := s.db.QueryContext(ctx, s.q("SELECT "+userCols+", l.links_count, d.domains"+s.adminUserFrom()+
		whereSQL(w)+" ORDER BY users.id DESC LIMIT ? OFFSET ?"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AdminUser{}
	for rows.Next() {
		var (
			lc   sql.NullInt64
			doms sql.NullString
		)
		u, err := scanUser(rows, &lc, &doms)
		if err != nil {
			return nil, err
		}
		au := store.AdminUser{User: *u, LinksCount: lc.Int64, Domains: []string{}}
		if doms.Valid && doms.String != "" {
			au.Domains = strings.Split(doms.String, ", ")
		}
		out = append(out, au)
	}
	return out, rows.Err()
}

func (us *userStore) TotalAdmin(ctx context.Context, f store.AdminUserFilter) (int64, error) {
	s := us.s
	w, args := s.adminUserWhere(f)
	var n int64
	err := s.db.QueryRowContext(ctx, s.q("SELECT COUNT(*)"+s.adminUserFrom()+whereSQL(w)), args...).Scan(&n)
	return n, err
}
