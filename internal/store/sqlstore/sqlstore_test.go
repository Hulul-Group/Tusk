package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// Integration tests. SQLite always runs; Postgres/MySQL run when
//   TUSK_TEST_PG_DSN    e.g. postgres://postgres:tusk@127.0.0.1:55432/tusk?sslmode=disable
//   TUSK_TEST_MYSQL_DSN e.g. root:tusk@tcp(127.0.0.1:53306)/tusk?parseTime=true&loc=UTC
// are set. The target databases are WIPED.

type target struct {
	name string
	open func(t *testing.T) *DB
}

func targets(t *testing.T) []target {
	ts := []target{{"sqlite", func(t *testing.T) *DB {
		db, err := OpenDSN(context.Background(), "sqlite", filepath.Join(t.TempDir(), "tusk.sqlite"), 1, 8)
		if err != nil {
			t.Fatal(err)
		}
		return db
	}}}
	if dsn := os.Getenv("TUSK_TEST_PG_DSN"); dsn != "" {
		ts = append(ts, target{"postgres", func(t *testing.T) *DB { return wiped(t, "postgres", dsn) }})
	}
	if dsn := os.Getenv("TUSK_TEST_MYSQL_DSN"); dsn != "" {
		ts = append(ts, target{"mysql", func(t *testing.T) *DB { return wiped(t, "mysql", dsn) }})
	}
	return ts
}

func wiped(t *testing.T, kind, dsn string) *DB {
	ctx := context.Background()
	db, err := OpenDSN(ctx, kind, dsn, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"visits", "links", "domains", "hosts", "ips", "users", "knex_migrations", "knex_migrations_lock"}
	if kind == "mysql" {
		db.db.Exec("SET FOREIGN_KEY_CHECKS=0")
		for _, tb := range tables {
			if _, err := db.db.Exec("DROP TABLE IF EXISTS `" + tb + "`"); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		for _, tb := range tables {
			if _, err := db.db.Exec("DROP TABLE IF EXISTS " + tb + " CASCADE"); err != nil {
				t.Fatal(err)
			}
		}
	}
	db.Close()
	db, err = OpenDSN(ctx, kind, dsn, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func forEach(t *testing.T, fn func(t *testing.T, db *DB)) {
	for _, tg := range targets(t) {
		t.Run(tg.name, func(t *testing.T) {
			db := tg.open(t)
			defer db.Close()
			if err := db.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
			fn(t, db)
		})
	}
}

func p[T any](v T) *T { return &v }

func mkUser(t *testing.T, db *DB, email string) *domain.User {
	u, err := db.Users().Create(context.Background(), store.CreateUser{Email: email, Password: "$2a$12$hash", Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestMigrate(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		if err := db.Migrate(ctx); err != nil { // idempotent
			t.Fatal(err)
		}
		var n int
		db.db.QueryRow("SELECT COUNT(*) FROM knex_migrations").Scan(&n)
		if n != 10 {
			t.Fatalf("knex_migrations rows = %d", n)
		}
		db.db.Exec(db.q("DELETE FROM knex_migrations WHERE name = ?"), LastKnexMigration)
		if err := db.Migrate(ctx); err != ErrSchemaTooOld {
			t.Fatalf("want ErrSchemaTooOld, got %v", err)
		}
	})
}

func TestUsers(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		us := db.Users()
		if any, _ := us.Any(ctx); any {
			t.Fatal("empty db has users")
		}
		exp := time.Now().Add(time.Hour)
		u, err := us.Create(ctx, store.CreateUser{Email: "a@x.com", Password: "h", VerificationToken: p("tok"), VerificationExpires: &exp})
		if err != nil {
			t.Fatal(err)
		}
		if u.Role != domain.RoleUser || u.Verified || u.ID == 0 || u.CreatedAt.IsZero() {
			t.Fatalf("bad user %+v", u)
		}
		if _, err := us.Create(ctx, store.CreateUser{Email: "a@x.com", Password: "h"}); err != domain.ErrConflict {
			t.Fatalf("dup email: %v", err)
		}
		now := time.Now()
		if _, err := us.Find(ctx, store.UserFilter{VerificationToken: p("tok"), TokenNotExpired: &now}); err != nil {
			t.Fatal("valid token", err)
		}
		later := now.Add(2 * time.Hour)
		if _, err := us.Find(ctx, store.UserFilter{VerificationToken: p("tok"), TokenNotExpired: &later}); err != domain.ErrNotFound {
			t.Fatal("expired token must miss", err)
		}
		u2, err := us.Update(ctx, store.UserFilter{VerificationToken: p("tok")}, store.UpdateUser{Verified: p(true), ClearVerification: true, APIKey: p("key1")})
		if err != nil || !u2.Verified || u2.VerificationToken != nil || *u2.APIKey != "key1" {
			t.Fatalf("update: %v %+v", err, u2)
		}
		if _, err := us.Update(ctx, store.UserFilter{Email: p("none@x")}, store.UpdateUser{Verified: p(true)}); err != domain.ErrNotFound {
			t.Fatal(err)
		}
		if f, err := us.Find(ctx, store.UserFilter{APIKey: p("key1")}); err != nil || f.ID != u.ID {
			t.Fatal(err)
		}
		// Admin list with domains + links count.
		db.Domains().Add(ctx, store.UpsertDomain{Address: "one.com", UserID: &u.ID})
		db.Domains().Add(ctx, store.UpsertDomain{Address: "two.com", UserID: &u.ID})
		db.Links().Create(ctx, store.CreateLink{Address: "l1", Target: "https://a.com", UserID: &u.ID})
		mkUser(t, db, "b@y.org")
		list, err := us.ListAdmin(ctx, store.AdminUserFilter{Search: "X.COM"}, store.Page{Limit: 10})
		if err != nil || len(list) != 1 || list[0].LinksCount != 1 || len(list[0].Domains) != 2 {
			t.Fatalf("admin: %v %+v", err, list)
		}
		if n, _ := us.TotalAdmin(ctx, store.AdminUserFilter{Domain: "two"}); n != 1 {
			t.Fatal("domain filter", n)
		}
		if n, _ := us.TotalAdmin(ctx, store.AdminUserFilter{Search: fmt.Sprint(u.ID)}); n != 1 {
			t.Fatal("id search", n)
		}
		if n, _ := us.TotalAdmin(ctx, store.AdminUserFilter{}); n != 2 {
			t.Fatal("total", n)
		}
		// Remove cascades links+visits, nulls domain owner.
		l, _ := db.Links().Find(ctx, store.LinkFilter{Address: p("l1")})
		db.Visits().ApplyDeltas(ctx, []store.VisitDelta{{LinkID: l.ID, UserID: &u.ID, Hour: time.Now(), Total: 1}})
		if err := us.Remove(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		var nl, nv int
		db.db.QueryRow("SELECT COUNT(*) FROM links").Scan(&nl)
		db.db.QueryRow("SELECT COUNT(*) FROM visits").Scan(&nv)
		d, _ := db.Domains().Find(ctx, store.DomainFilter{Address: p("one.com")})
		if nl != 0 || nv != 0 || d == nil || d.UserID != nil {
			t.Fatalf("cascade: links=%d visits=%d dom=%+v", nl, nv, d)
		}
	})
}

func TestLinks(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		u := mkUser(t, db, "l@x.com")
		d, err := db.Domains().Add(ctx, store.UpsertDomain{Address: "Short.IO", UserID: &u.ID})
		if err != nil || d.Address != "short.io" || d.UUID == "" {
			t.Fatalf("domain: %v %+v", err, d)
		}
		ls := db.Links()
		l, err := ls.Create(ctx, store.CreateLink{Address: "abc", Target: "https://example.com/x", UserID: &u.ID,
			Password: p("secret"), Description: p("My Desc")})
		if err != nil {
			t.Fatal(err)
		}
		if l.UUID == "" || l.Password == nil || bcrypt.CompareHashAndPassword([]byte(*l.Password), []byte("secret")) != nil {
			t.Fatalf("link: %+v", l)
		}
		if c, _ := bcrypt.Cost([]byte(*l.Password)); c != 12 {
			t.Fatal("cost", c)
		}
		exp := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
		l2, err := ls.Create(ctx, store.CreateLink{Address: "abc", Target: "https://b.com", DomainID: &d.ID, UserID: &u.ID, ExpireIn: &exp})
		if err != nil || l2.DomainAddress == nil || *l2.DomainAddress != "short.io" || l2.ExpireIn == nil || !l2.ExpireIn.Equal(exp) {
			t.Fatalf("link2: %v %+v", err, l2)
		}
		// Resolve default vs custom domain.
		rt, err := ls.Resolve(ctx, "abc", nil)
		if err != nil || rt.LinkID != l.ID || !rt.HasPassword || rt.Target != "https://example.com/x" || *rt.UserID != u.ID {
			t.Fatalf("resolve: %v %+v", err, rt)
		}
		if rt, _ := ls.Resolve(ctx, "abc", &d.ID); rt == nil || rt.LinkID != l2.ID || rt.HasPassword {
			t.Fatal("resolve custom")
		}
		if _, err := ls.Resolve(ctx, "nope", nil); err != domain.ErrNotFound {
			t.Fatal(err)
		}
		if ok, _ := ls.AddressTaken(ctx, "abc", &d.ID); !ok {
			t.Fatal("taken")
		}
		if ok, _ := ls.AddressTaken(ctx, "zzz", nil); ok {
			t.Fatal("not taken")
		}
		// Search is case-insensitive over description/address/target/domain.
		for _, q := range []string{"my desc", "EXAMPLE", "short.io"} {
			if n, _ := ls.Total(ctx, store.LinkFilter{UserID: &u.ID}, q); n < 1 {
				t.Errorf("search %q: %d", q, n)
			}
		}
		list, _ := ls.List(ctx, store.LinkFilter{UserID: &u.ID}, "", store.Page{Limit: 10})
		if len(list) != 2 || list[0].ID != l2.ID {
			t.Fatal("list order desc")
		}
		if pg, _ := ls.List(ctx, store.LinkFilter{UserID: &u.ID}, "", store.Page{Limit: 1, Skip: 1}); len(pg) != 1 || pg[0].ID != l.ID {
			t.Fatal("paging")
		}
		// Admin.
		al, err := ls.ListAdmin(ctx, store.AdminLinkFilter{User: "L@X", Domain: "short"}, store.Page{Limit: 5})
		if err != nil || len(al) != 1 || al[0].Email == nil || *al[0].Email != "l@x.com" {
			t.Fatalf("admin: %v %+v", err, al)
		}
		if n, _ := ls.TotalAdmin(ctx, store.AdminLinkFilter{HasDomain: p(false), Anonymous: p(false)}); n != 1 {
			t.Fatal("admin filters", n)
		}
		if _, err := ls.Find(ctx, store.LinkFilter{UUID: p("not-a-uuid")}); err != domain.ErrNotFound {
			t.Fatal("bad uuid", err)
		}
		// Update.
		up, err := ls.Update(ctx, store.LinkFilter{ID: &l.ID}, store.UpdateLink{Address: p("abc2"), ClearPassword: true, Banned: p(true), BannedByID: &u.ID})
		if err != nil || len(up) != 1 || up[0].Address != "abc2" || up[0].Password != nil || !up[0].Banned {
			t.Fatalf("update: %v %+v", err, up)
		}
		if _, err := ls.Update(ctx, store.LinkFilter{ID: &l.ID}, store.UpdateLink{Address: p("abc")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ls.Create(ctx, store.CreateLink{Address: "abc", Target: "x"}); err != nil {
			// no unique index on (address, domain_id) in Kutt: duplicates are prevented in handlers
			t.Fatal(err)
		}
		// Expired sweep.
		keys, err := ls.RemoveExpired(ctx, time.Now())
		if err != nil || len(keys) != 1 || keys[0].Address != "abc" || keys[0].DomainID == nil || *keys[0].DomainID != d.ID {
			t.Fatalf("expired: %v %+v", err, keys)
		}
		ok, err := ls.Remove(ctx, store.LinkFilter{ID: &l.ID})
		if err != nil || !ok {
			t.Fatal("remove", err)
		}
		if ok, _ := ls.Remove(ctx, store.LinkFilter{ID: &l.ID}); ok {
			t.Fatal("double remove")
		}
		// Hosts.
		if err := db.Hosts().Ban(ctx, "Evil.com", u.ID); err != nil {
			t.Fatal(err)
		}
		db.Hosts().Ban(ctx, "evil.com", u.ID)
		h, err := db.Hosts().Find(ctx, "evil.com")
		if err != nil || !h.Banned || *h.BannedByID != u.ID {
			t.Fatal("host", err)
		}
		// Domains.
		d2, _ := db.Domains().Add(ctx, store.UpsertDomain{Address: "short.io", Homepage: p("https://home")})
		if d2.ID != d.ID || *d2.Homepage != "https://home" || d2.UserID == nil {
			t.Fatalf("re-add: %+v", d2)
		}
		ud, _ := db.Domains().Update(ctx, store.DomainFilter{UserID: &u.ID}, store.UpdateDomain{ClearUserID: true})
		if len(ud) != 1 || ud[0].UserID != nil {
			t.Fatalf("domain update %+v", ud)
		}
		ad, _ := db.Domains().ListAdmin(ctx, store.AdminDomainFilter{Search: "HOME"}, store.Page{Limit: 5})
		if len(ad) != 1 {
			t.Fatal("domain admin search")
		}
	})
}

func deltaFor(linkID int64, hour time.Time, n int64) store.VisitDelta {
	return store.VisitDelta{
		LinkID: linkID, Hour: hour, Total: n,
		Browser:   map[string]int64{"chrome": n},
		OS:        map[string]int64{"windows": n},
		Countries: map[string]int64{"us": n},
		Referrers: map[string]int64{"google[dot]com": n},
	}
}

func TestVisitsConcurrent(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		u := mkUser(t, db, "v@x.com")
		l, _ := db.Links().Create(ctx, store.CreateLink{Address: "v", Target: "https://t", UserID: &u.ID})
		hour := time.Now().UTC().Truncate(time.Hour)
		const workers, per = 8, 25
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < per; i++ {
					d := deltaFor(l.ID, hour, 1)
					if i%2 == 0 {
						d.Countries = map[string]int64{fmt.Sprintf("c%d", w): 1}
					}
					if err := db.Visits().ApplyDeltas(ctx, []store.VisitDelta{d}); err != nil {
						errs <- err
						return
					}
				}
			}(w)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		var rowsN, total, chrome int64
		var cRaw []byte
		db.db.QueryRow(db.q("SELECT COUNT(*), SUM(total), SUM(br_chrome) FROM visits WHERE link_id = ?"), l.ID).Scan(&rowsN, &total, &chrome)
		db.db.QueryRow(db.q("SELECT countries FROM visits WHERE link_id = ?"), l.ID).Scan(&cRaw)
		got, _ := db.Links().Find(ctx, store.LinkFilter{ID: &l.ID})
		if rowsN != 1 || total != workers*per || chrome != workers*per || got.VisitCount != workers*per {
			t.Fatalf("rows=%d total=%d chrome=%d visit_count=%d", rowsN, total, chrome, got.VisitCount)
		}
		c, _ := parseCounts(cRaw)
		var sum int64
		for _, v := range c.vals {
			sum += v
		}
		if sum != workers*per || c.vals["c3"] != (per+1)/2 {
			t.Fatalf("countries %s", cRaw)
		}
		// Deleted link: delta is dropped silently.
		if err := db.Visits().ApplyDeltas(ctx, []store.VisitDelta{deltaFor(999999, hour, 1)}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStats(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		l, _ := db.Links().Create(ctx, store.CreateLink{Address: "s", Target: "https://t"})
		now := time.Date(2026, 3, 31, 12, 30, 0, 0, time.UTC)
		ins := func(at time.Time, total int64, br, os, country, ref string) {
			_, err := db.db.Exec(db.q(fmt.Sprintf(`INSERT INTO visits (link_id, total, br_%s, os_%s, countries, referrers, created_at, updated_at)
 VALUES (?, ?, ?, ?, %s, %s, ?, ?)`, br, os, db.jsonParam(), db.jsonParam())),
				l.ID, total, total, total, `{"`+country+`":`+fmt.Sprint(total)+`}`, `{"`+ref+`":`+fmt.Sprint(total)+`}`, db.ts(at), db.ts(at))
			if err != nil {
				t.Fatal(err)
			}
		}
		ins(now.Add(-30*time.Minute), 3, "chrome", "windows", "us", "direct")     // lastDay idx 23
		ins(now.Add(-5*time.Hour), 2, "firefox", "linux", "de", "google[dot]com") // idx 18
		ins(now.Add(-3*24*time.Hour), 4, "chrome", "android", "de", "direct")     // week idx 3
		ins(now.Add(-20*24*time.Hour), 5, "safari", "ios", "fr", "direct")        // month
		ins(now.AddDate(0, -5, 0), 7, "other", "other", "unknown", "direct")      // year
		ins(now.AddDate(-2, 0, 0), 100, "other", "other", "old", "direct")        // excluded
		st, err := db.Visits().Stats(ctx, l.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		if st.LastDay.Total != 5 || st.LastDay.Views[23] != 3 || st.LastDay.Views[18] != 2 {
			t.Fatalf("day %+v", st.LastDay)
		}
		if st.LastWeek.Total != 9 || st.LastWeek.Views[6] != 5 || st.LastWeek.Views[3] != 4 {
			t.Fatalf("week %+v", st.LastWeek.Views)
		}
		if st.LastMonth.Total != 14 || st.LastYear.Total != 21 || st.LastYear.Views[11] != 14 || st.LastYear.Views[6] != 7 {
			t.Fatalf("month/year %d %d %v", st.LastMonth.Total, st.LastYear.Total, st.LastYear.Views)
		}
		if b := st.LastDay.Stats.Browser; b[0].Name != "chrome" || b[0].Value != 3 || b[1].Name != "firefox" || b[2].Name != "edge" {
			t.Fatalf("browser order %+v", b)
		}
		if c := st.LastWeek.Stats.Country; len(c) != 2 || c[0].Name != "de" || c[0].Value != 6 {
			t.Fatalf("country %+v", c)
		}
		if len(st.LastYear.Stats.Referrer) != 2 || st.LastYear.Stats.Referrer[0].Name != "direct" {
			t.Fatalf("ref %+v", st.LastYear.Stats.Referrer)
		}
	})
}

// TestKuttCreatedSchema runs against databases created by Kutt's own knex migrations.
//
//	TUSK_TEST_KUTT_SQLITE=/path/kutt.sqlite (copied before use)
//	TUSK_TEST_KUTT_PG_DSN / TUSK_TEST_KUTT_MYSQL_DSN
func TestKuttCreatedSchema(t *testing.T) {
	ctx := context.Background()
	var ts []target
	if src := os.Getenv("TUSK_TEST_KUTT_SQLITE"); src != "" {
		ts = append(ts, target{"sqlite", func(t *testing.T) *DB {
			b, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(t.TempDir(), "kutt.sqlite")
			os.WriteFile(dst, b, 0o600)
			db, err := OpenDSN(ctx, "sqlite", dst, 1, 4)
			if err != nil {
				t.Fatal(err)
			}
			return db
		}})
	}
	for kind, env := range map[string]string{"postgres": "TUSK_TEST_KUTT_PG_DSN", "mysql": "TUSK_TEST_KUTT_MYSQL_DSN"} {
		if dsn := os.Getenv(env); dsn != "" {
			ts = append(ts, target{kind, func(t *testing.T) *DB {
				db, err := OpenDSN(ctx, kind, dsn, 1, 4)
				if err != nil {
					t.Fatal(err)
				}
				for _, tb := range []string{"visits", "links", "domains", "hosts", "users"} {
					db.db.Exec("DELETE FROM " + tb)
				}
				return db
			}})
		}
	}
	if len(ts) == 0 {
		t.Skip("no Kutt-created databases configured")
	}
	for _, tg := range ts {
		t.Run(tg.name, func(t *testing.T) {
			db := tg.open(t)
			defer db.Close()
			if err := db.Migrate(ctx); err != nil {
				t.Fatal("Migrate must accept a Kutt-migrated DB:", err)
			}
			u := mkUser(t, db, fmt.Sprintf("k%d@x.com", time.Now().UnixNano()))
			d, err := db.Domains().Add(ctx, store.UpsertDomain{Address: "kutt.example", UserID: &u.ID})
			if err != nil {
				t.Fatal(err)
			}
			l, err := db.Links().Create(ctx, store.CreateLink{Address: "k1", Target: "https://t", UserID: &u.ID, DomainID: &d.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Visits().ApplyDeltas(ctx, []store.VisitDelta{deltaFor(l.ID, time.Now(), 2), deltaFor(l.ID, time.Now(), 3)}); err != nil {
				t.Fatal(err)
			}
			st, err := db.Visits().Stats(ctx, l.ID, time.Now())
			if err != nil || st.LastDay.Total != 5 {
				t.Fatalf("stats: %v %+v", err, st)
			}
			if _, err := db.Links().Remove(ctx, store.LinkFilter{ID: &l.ID}); err != nil {
				t.Fatal(err)
			}
			if err := db.Users().Remove(ctx, u.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestSchemaMatchesKutt compares Tusk's generated sqlite schema against a Kutt-generated one
// (column names, types, nullability, defaults — ignoring the uuid default text and indexes).
func TestSchemaMatchesKutt(t *testing.T) {
	src := os.Getenv("TUSK_TEST_KUTT_SQLITE")
	if src == "" {
		t.Skip("TUSK_TEST_KUTT_SQLITE not set")
	}
	ctx := context.Background()
	mine, _ := OpenDSN(ctx, "sqlite", filepath.Join(t.TempDir(), "m.sqlite"), 1, 1)
	defer mine.Close()
	if err := mine.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ref, err := sql.Open("sqlite", "file:"+src+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ref.Close()
	for _, tb := range []string{"users", "domains", "hosts", "links", "visits", "ips", "knex_migrations"} {
		a, b := cols(t, mine.db, tb), cols(t, ref, tb)
		if a != b {
			t.Errorf("table %s differs:\n tusk: %s\n kutt: %s", tb, a, b)
		}
	}
	idx := func(db *sql.DB) string {
		rows, _ := db.Query("SELECT name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%' ORDER BY name")
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			rows.Scan(&n)
			out = append(out, n)
		}
		return strings.Join(out, ",")
	}
	if a, b := idx(mine.db), idx(ref); a != b {
		t.Errorf("indexes differ:\n tusk: %s\n kutt: %s", a, b)
	}
}

func cols(t *testing.T, db *sql.DB, table string) string {
	rows, err := db.Query("SELECT name, type, \"notnull\", COALESCE(dflt_value, ''), pk FROM pragma_table_info('" + table + "') ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, typ, dflt string
		var nn, pk int
		rows.Scan(&name, &typ, &nn, &dflt, &pk)
		if strings.Contains(dflt, "randomblob") {
			dflt = "uuid"
		}
		out = append(out, fmt.Sprintf("%s:%s:%d:%s:%d", name, strings.ToLower(typ), nn, dflt, pk))
	}
	return strings.Join(out, " ")
}
