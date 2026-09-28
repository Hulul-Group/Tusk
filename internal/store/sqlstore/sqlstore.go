// Package sqlstore implements store.Store over database/sql for the three Kutt dialects:
// postgres (pgx stdlib), mysql/mariadb (go-sql-driver) and sqlite (modernc, pure Go).
//
// Every query mirrors Kutt v3.2.6 server/queries/*.js; comments cite the source line.
// Queries are written once with '?' placeholders and rebound for postgres.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/store"
)

type dialect int

const (
	dPostgres dialect = iota
	dMySQL
	dSQLite
)

func (d dialect) String() string {
	switch d {
	case dPostgres:
		return "postgres"
	case dMySQL:
		return "mysql"
	}
	return "sqlite"
}

// DB implements store.Store.
type DB struct {
	db *sql.DB
	d  dialect

	rebindCache sync.Map // string -> string (postgres only)

	links   *linkStore
	users   *userStore
	domains *domainStore
	hosts   *hostStore
	visits  *visitStore
}

var _ store.Store = (*DB)(nil)

// Open connects using the Kutt DB_* settings and applies DB_POOL_MIN/MAX.
func Open(ctx context.Context, cfg *config.Config) (*DB, error) {
	var (
		d          dialect
		driver     string
		dsn        string
		err        error
		poolMax    = cfg.DBPoolMax
		poolMin    = cfg.DBPoolMin
		sqliteFile string
	)
	switch {
	case cfg.IsPostgres():
		d, driver = dPostgres, "pgx"
		dsn = postgresDSN(cfg)
	case cfg.IsMySQL():
		d, driver = dMySQL, "mysql"
		dsn, err = mysqlDSN(cfg)
		if err != nil {
			return nil, err
		}
	case cfg.IsSQLite():
		d, driver = dSQLite, "sqlite"
		sqliteFile = cfg.DBFilename
		dsn = sqliteDSN(sqliteFile)
	default:
		return nil, fmt.Errorf("sqlstore: unsupported DB_CLIENT %q", cfg.DBClient)
	}
	return openDSN(ctx, d, driver, dsn, poolMin, poolMax)
}

// OpenDSN opens with an explicit driver DSN (tests, tools). kind is postgres|mysql|sqlite.
// For sqlite, dsn is a file path; for mysql it must include parseTime=true&loc=UTC.
func OpenDSN(ctx context.Context, kind, dsn string, poolMin, poolMax int) (*DB, error) {
	switch kind {
	case "postgres", "pg":
		return openDSN(ctx, dPostgres, "pgx", dsn, poolMin, poolMax)
	case "mysql":
		return openDSN(ctx, dMySQL, "mysql", dsn, poolMin, poolMax)
	case "sqlite":
		return openDSN(ctx, dSQLite, "sqlite", sqliteDSN(dsn), poolMin, poolMax)
	}
	return nil, fmt.Errorf("sqlstore: unknown kind %q", kind)
}

func openDSN(ctx context.Context, d dialect, driver, dsn string, poolMin, poolMax int) (*DB, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if poolMax <= 0 {
		poolMax = 10
	}
	// knex/tarn semantics: at most DB_POOL_MAX connections, idle ones reaped after 30s,
	// DB_POOL_MIN kept warm (pre-opened below).
	db.SetMaxOpenConns(poolMax)
	db.SetMaxIdleConns(poolMax)
	db.SetConnMaxIdleTime(30 * time.Second)
	db.SetConnMaxLifetime(30 * time.Minute)

	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlstore: connect %s: %w", d, err)
	}
	if poolMin > 1 {
		conns := make([]*sql.Conn, 0, poolMin)
		for i := 0; i < poolMin && i < poolMax; i++ {
			c, err := db.Conn(pctx)
			if err != nil {
				break
			}
			conns = append(conns, c)
		}
		for _, c := range conns {
			c.Close()
		}
	}
	s := &DB{db: db, d: d}
	s.links = &linkStore{s}
	s.users = &userStore{s}
	s.domains = &domainStore{s}
	s.hosts = &hostStore{s}
	s.visits = &visitStore{s}
	return s, nil
}

func postgresDSN(cfg *config.Config) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.DBUser, cfg.DBPassword),
		Host:   cfg.DBHost + ":" + strconv.Itoa(cfg.DBPort),
		Path:   "/" + cfg.DBName,
	}
	q := url.Values{}
	if cfg.DBSSL {
		// Kutt passes ssl:true to node-postgres; "require" encrypts without CA pinning,
		// which is what most managed-Postgres Kutt installs rely on.
		q.Set("sslmode", "require")
	} else {
		q.Set("sslmode", "disable")
	}
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	return u.String()
}

func mysqlDSN(cfg *config.Config) (string, error) {
	mc := mysql.NewConfig()
	mc.User = cfg.DBUser
	mc.Passwd = cfg.DBPassword
	mc.Net = "tcp"
	mc.Addr = cfg.DBHost + ":" + strconv.Itoa(cfg.DBPort)
	mc.DBName = cfg.DBName
	mc.ParseTime = true
	mc.Loc = time.UTC
	mc.Params = map[string]string{"time_zone": "'+00:00'", "charset": "utf8mb4"}
	if cfg.DBSSL {
		mc.TLSConfig = "skip-verify"
	}
	return mc.FormatDSN(), nil
}

// sqliteDSN: WAL, busy_timeout, foreign keys ON, IMMEDIATE write transactions.
func sqliteDSN(path string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	return "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
}

func (s *DB) Links() store.LinkStore     { return s.links }
func (s *DB) Users() store.UserStore     { return s.users }
func (s *DB) Domains() store.DomainStore { return s.domains }
func (s *DB) Hosts() store.HostStore     { return s.hosts }
func (s *DB) Visits() store.VisitStore   { return s.visits }
func (s *DB) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}
func (s *DB) Close() error { return s.db.Close() }

// SQL exposes the pool (tests, health metrics).
func (s *DB) SQL() *sql.DB { return s.db }

// Dialect returns postgres|mysql|sqlite.
func (s *DB) Dialect() string { return s.d.String() }

// ---------- query helpers ----------

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// q rebinds '?' placeholders to $n for postgres (cached).
func (s *DB) q(query string) string {
	if s.d != dPostgres {
		return query
	}
	if v, ok := s.rebindCache.Load(query); ok {
		return v.(string)
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	inStr := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c == '\'' {
			inStr = !inStr
		}
		if c == '?' && !inStr {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	out := b.String()
	s.rebindCache.Store(query, out)
	return out
}

// like returns a case-insensitive LIKE predicate for expr (knex compatibleILIKE).
// Postgres: ILIKE (knex.js:31). MySQL/SQLite: LOWER(expr) LIKE LOWER(?).
func (s *DB) like(expr string) string {
	if s.d == dPostgres {
		return expr + " ILIKE ?"
	}
	return "LOWER(" + expr + ") LIKE LOWER(?)"
}

// ts converts a time to the dialect's storage form (utils.js dateToUTC:132-147):
// sqlite 'YYYY-MM-DD HH:MM:SS' UTC text; mysql DATETIME in UTC (session time_zone +00:00);
// postgres timestamptz.
func (s *DB) ts(t time.Time) any {
	t = t.UTC()
	switch s.d {
	case dSQLite:
		return t.Format("2006-01-02 15:04:05")
	case dMySQL:
		return t.Truncate(time.Second)
	}
	return t.Truncate(time.Millisecond) // JS Date precision
}

func (s *DB) tsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return s.ts(*t)
}

// b converts a bool for binding (sqlite/mysql store 0/1).
func (s *DB) b(v bool) any {
	if s.d == dPostgres {
		return v
	}
	if v {
		return 1
	}
	return 0
}

func i64Ptr(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func strPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// insertID runs an INSERT and returns the new id (RETURNING on postgres/sqlite,
// LastInsertId on mysql).
func (s *DB) insertID(ctx context.Context, x querier, query string, args ...any) (int64, error) {
	if s.d == dMySQL {
		res, err := x.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	var id int64
	err := x.QueryRowContext(ctx, s.q(query+" RETURNING id"), args...).Scan(&id)
	return id, err
}

// inTx runs fn in a transaction.
func (s *DB) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---------- scanners ----------

// nullTime scans postgres/mysql time.Time and sqlite TEXT ('YYYY-MM-DD HH:MM:SS', ISO 8601,
// or epoch millis as written by some knex/better-sqlite3 paths). Values are UTC.
type nullTime struct {
	T     time.Time
	Valid bool
}

var timeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02",
}

func (n *nullTime) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		n.Valid = false
		return nil
	case time.Time:
		n.T, n.Valid = x.UTC(), true
		return nil
	case string:
		return n.parse(x)
	case []byte:
		return n.parse(string(x))
	case int64:
		// better-sqlite3 binds JS Date as epoch millis when not converted by dateToUTC.
		n.T, n.Valid = time.UnixMilli(x).UTC(), true
		return nil
	case float64:
		n.T, n.Valid = time.UnixMilli(int64(x)).UTC(), true
		return nil
	}
	return fmt.Errorf("sqlstore: cannot scan %T into time", v)
}

func (n *nullTime) parse(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		n.Valid = false
		return nil
	}
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			n.T, n.Valid = t.UTC(), true
			return nil
		}
	}
	return fmt.Errorf("sqlstore: unparseable time %q", s)
}

func (n nullTime) ptr() *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.T
	return &t
}

func nsPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

func niPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// boolScan accepts bool, integers and "0"/"1"/"true"/"false" text.
type boolScan bool

func (b *boolScan) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*b = false
	case bool:
		*b = boolScan(x)
	case int64:
		*b = x != 0
	case float64:
		*b = x != 0
	case []byte:
		return b.Scan(string(x))
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "t", "true":
			*b = true
		default:
			*b = false
		}
	default:
		return fmt.Errorf("sqlstore: cannot scan %T into bool", v)
	}
	return nil
}

// isUniqueViolation detects unique-constraint errors across drivers.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return true
	}
	m := err.Error()
	return strings.Contains(m, "SQLSTATE 23505") || strings.Contains(m, "UNIQUE constraint failed") ||
		strings.Contains(m, "duplicate key value")
}

// retryable reports transient concurrency errors (deadlock, serialization, busy).
func retryable(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && (me.Number == 1213 || me.Number == 1205) {
		return true
	}
	m := err.Error()
	return strings.Contains(m, "SQLSTATE 40001") || strings.Contains(m, "SQLSTATE 40P01") ||
		strings.Contains(m, "database is locked") || strings.Contains(m, "SQLITE_BUSY")
}

// placeholders returns "?, ?, ?" for n args.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
}

// jsParseInt mimics JS parseInt(x) for the admin "user"/"search" filters:
// leading whitespace, optional sign, then digits; NaN when no digits.
func jsParseInt(s string) (int64, bool) {
	s = strings.TrimLeft(s, " \t\n\r\f\v")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n == 0 {
		return 0, false
	}
	v, err := strconv.ParseInt(s[:n], 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		v = -v
	}
	return v, true
}
