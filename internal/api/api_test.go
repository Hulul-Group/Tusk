package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

type tc struct {
	name   string
	method string
	path   string
	auth   string
	body   any
	code   int
	want   string // substring (or exact JSON when it starts with '{')
}

func runCases(t *testing.T, e *env, cases []tc) {
	t.Helper()
	for _, c := range cases {
		for _, prefix := range []string{"/api/v2", "/api"} {
			if strings.HasPrefix(c.path, "!") && prefix == "/api" {
				continue
			}
			path := prefix + strings.TrimPrefix(c.path, "!")
			t.Run(prefix+" "+c.name, func(t *testing.T) {
				r := e.do(c.method, path, c.auth, c.body)
				if r.code != c.code {
					t.Fatalf("%s %s: status %d want %d body=%s", c.method, path, r.code, c.code, r.body)
				}
				if c.want != "" {
					if strings.HasPrefix(c.want, "{") {
						if r.body != c.want {
							t.Fatalf("body %s\nwant %s", r.body, c.want)
						}
					} else if !strings.Contains(r.body, c.want) {
						t.Fatalf("body %s missing %q", r.body, c.want)
					}
				}
			})
		}
	}
}

func TestHealthAndNotFound(t *testing.T) {
	e := newEnv(t)
	runCases(t, e, []tc{
		{"health", "GET", "/health", "", nil, 200, "OK"},
		{"unknown api route", "GET", "/nope", "", nil, 404, `{"error":"Not found."}`},
	})
}

func TestAuthRequiredAndForbidden(t *testing.T) {
	e := newEnv(t)
	banned := e.st.SeedUser(domain.User{Email: "banned@example.com", Password: hash(t, "whatever1"), Banned: true, Verified: true, APIKey: ptr("banned-key")})
	unverified := e.st.SeedUser(domain.User{Email: "new@example.com", Password: hash(t, "whatever1")})
	un := `{"error":"Unauthorized."}`
	adminOnly := `{"error":"Unauthorized"}`
	bannedMsg := `{"error":"You're banned from using this website."}`
	id := "11111111-1111-1111-1111-111111111111"
	runCases(t, e, []tc{
		{"links no auth", "GET", "/links", "", nil, 401, un},
		{"links bad key", "GET", "/links", "key:nope", nil, 401, un},
		{"links bad cookie", "GET", "/links", "cookie:garbage", nil, 401, un},
		{"links banned key", "GET", "/links", "key:banned-key", nil, 403, bannedMsg},
		{"links banned jwt", "GET", "/links", e.jwt(banned), nil, 403, bannedMsg},
		{"links unverified jwt", "GET", "/links", e.jwt(unverified), nil, 400, `{"error":"Your email address is not verified. Sign up to get the verification link again."}`},
		{"users no auth", "GET", "/users", "", nil, 401, un},
		{"links admin as user", "GET", "/links/admin", "user", nil, 401, adminOnly},
		{"users admin as user", "GET", "/users/admin", "user", nil, 401, adminOnly},
		{"domains admin as user", "GET", "/domains/admin", "user", nil, 401, adminOnly},
		{"create user as user", "POST", "/users/admin", "user", map[string]any{}, 401, adminOnly},
		{"ban link as user", "POST", "/links/admin/ban/" + id, "user", map[string]any{}, 401, adminOnly},
		{"ban user as user", "POST", "/users/admin/ban/1", "user", map[string]any{}, 401, adminOnly},
		{"ban domain as user", "POST", "/domains/admin/ban/1", "user", map[string]any{}, 401, adminOnly},
		{"delete user admin as user", "DELETE", "/users/admin/1", "user", nil, 401, adminOnly},
		{"delete domain admin as user", "DELETE", "/domains/admin/1", "user", nil, 401, adminOnly},
		{"add domain admin as user", "POST", "/domains/admin", "user", map[string]any{}, 401, adminOnly},
		{"edit link admin as user", "PATCH", "/links/admin/" + id, "user", map[string]any{}, 401, adminOnly},
		{"stats no auth", "GET", "/links/" + id + "/stats", "", nil, 401, un},
		{"delete link no auth", "DELETE", "/links/" + id, "", nil, 401, un},
		{"edit link no auth", "PATCH", "/links/" + id, "", map[string]any{}, 401, un},
		{"add domain no auth", "POST", "/domains", "", map[string]any{}, 401, un},
		{"delete domain no auth", "DELETE", "/domains/" + id, "", nil, 401, un},
		{"delete account no auth", "POST", "/users/delete", "", map[string]any{}, 401, un},
		// auth.jwt-only routes ignore X-API-KEY (auth.routes.js)
		{"apikey via apikey", "POST", "/auth/apikey", "user", nil, 401, un},
		{"change-password via apikey", "POST", "/auth/change-password", "user", map[string]any{}, 401, un},
		// api key in query string (passport-localapikey reads query.apikey)
		{"apikey query", "GET", "/links?apikey=" + e.userK, "", nil, 200, `"total":0`},
		// admin allowed
		{"links admin", "GET", "/links/admin", "admin", nil, 200, `"data":[]`},
	})
}

func TestAuthRoutes(t *testing.T) {
	e := newEnv(t)
	runCases(t, e, []tc{
		{"login missing", "POST", "/auth/login", "", map[string]any{}, 400, `{"error":"Password is not valid."}`},
		{"login short pw", "POST", "/auth/login", "", map[string]any{"password": "short", "email": "a@b.co"}, 400, `{"error":"Password length must be between 8 and 64."}`},
		{"login bad email", "POST", "/auth/login", "", map[string]any{"password": "longenough", "email": "nope"}, 400, `{"error":"Email is not valid."}`},
		{"login long email", "POST", "/auth/login", "", map[string]any{"password": "longenough", "email": strings.Repeat("a", 250) + "@b.com"}, 400, `{"error":"Email length must be max 255."}`},
		{"login wrong", "POST", "/auth/login", "", map[string]any{"password": "wrongpass", "email": "user@example.com"}, 401, `{"error":"Login credentials are wrong."}`},
		{"login unknown", "POST", "/auth/login", "", map[string]any{"password": "wrongpass", "email": "x@example.com"}, 401, `{"error":"Login credentials are wrong."}`},
		{"login ok form", "POST", "/auth/login", "", "email=user%40example.com&password=userpass1", 200, `"token":"`},
		{"signup disabled", "POST", "/auth/signup", "", map[string]any{}, 400, `{"error":"Request is not allowed."}`},
		{"reset disabled", "POST", "/auth/reset-password", "", map[string]any{"email": "a@b.co"}, 400, `{"error":"Request is not allowed."}`},
		{"change-email disabled", "POST", "/auth/change-email", e.jwt(e.user), map[string]any{}, 400, `{"error":"Request is not allowed."}`},
		{"report disabled", "POST", "/links/report", "", map[string]any{"link": "kutt.test/x"}, 400, `{"error":"Request is not allowed."}`},
		{"create-admin exists", "POST", "/auth/create-admin", "", map[string]any{"email": "b@b.co", "password": "longenough"}, 400, `{"error":"Can not create the admin user because a user already exists."}`},
		{"create-admin invalid", "POST", "/auth/create-admin", "", map[string]any{"email": "b@b.co"}, 400, `{"error":"Password is not valid."}`},
		{"change-password wrong", "POST", "/auth/change-password", e.jwt(e.user), map[string]any{"currentpassword": "wrongpass1", "newpassword": "newpass123"}, 401, `{"error":"Current password is not correct."}`},
		{"new-password token", "POST", "/auth/new-password", "", map[string]any{"reset_password_token": "short"}, 400, `{"error":"Reset password token is invalid."}`},
		{"new-password mismatch", "POST", "/auth/new-password", "", map[string]any{"reset_password_token": strings.Repeat("a", 36), "new_password": "abcdefgh", "repeat_password": "abcdefgx"}, 400, `{"error":"Passwords don't match."}`},
		{"new-password bad token", "POST", "/auth/new-password", "", map[string]any{"reset_password_token": strings.Repeat("a", 36), "new_password": "abcdefgh", "repeat_password": "abcdefgh"}, 500, `{"error":"Could not set the password. Please try again later."}`},
		{"verify bad", "POST", "!/auth/verify/nope", "", nil, 400, `{"error":"Invalid or expired verification link."}`},
		{"logout", "POST", "!/auth/logout", "", nil, 200, `{"message":"Logged out."}`},
	})
	// login sets the httpOnly cookie
	r := e.do("POST", "/api/v2/auth/login", "", map[string]any{"email": "user@example.com", "password": "userpass1"})
	c := r.hdr.Get("Set-Cookie")
	if !strings.Contains(c, "token=") || !strings.Contains(c, "HttpOnly") || !strings.Contains(c, "SameSite=Lax") ||
		!strings.Contains(c, "Secure") || !strings.Contains(c, "Max-Age=604800") {
		t.Fatalf("cookie %q", c)
	}
	tok := r.json(t)["token"].(string)
	if g := e.do("GET", "/api/v2/users", "cookie:"+tok, nil); g.code != 200 {
		t.Fatalf("jwt from login rejected: %d %s", g.code, g.body)
	}
	// change password then login with the new one
	if r := e.do("POST", "/api/v2/auth/change-password", "cookie:"+tok, map[string]any{"currentpassword": "userpass1", "newpassword": "newpass123"}); r.code != 200 ||
		r.body != `{"message":"Your password has been changed successfully."}` {
		t.Fatalf("%d %s", r.code, r.body)
	}
	if r := e.do("POST", "/api/v2/auth/login", "", map[string]any{"email": "user@example.com", "password": "newpass123"}); r.code != 200 {
		t.Fatalf("login new pw %d %s", r.code, r.body)
	}
	// generate api key
	r = e.do("POST", "/api/v2/auth/apikey", "cookie:"+tok, nil)
	key, _ := r.json(t)["apikey"].(string)
	if r.code != 201 || len(key) != 40 {
		t.Fatalf("apikey %d %s", r.code, r.body)
	}
	if g := e.do("GET", "/api/v2/users", "key:"+key, nil); g.code != 200 {
		t.Fatalf("new key rejected")
	}
}

func TestCreateAdminFlow(t *testing.T) {
	st := newEnv(t)
	// fresh store without users
	e := newEnv(t)
	_ = st
	for _, u := range []*domain.User{e.admin, e.user} {
		_ = e.st.Users().Remove(t.Context(), u.ID)
	}
	if r := e.do("GET", "/api/v2/config", "", nil); !strings.Contains(r.body, `"needs_admin_setup":true`) {
		t.Fatal(r.body)
	}
	r := e.do("POST", "/api/v2/auth/create-admin", "", map[string]any{"email": " Boss@Example.com ", "password": "supersecret"})
	if r.code != 201 || !strings.HasPrefix(r.body, `{"token":"`) || !strings.Contains(r.hdr.Get("Set-Cookie"), "token=") {
		t.Fatalf("%d %s", r.code, r.body)
	}
	u, err := e.st.Users().Find(t.Context(), store.UserFilter{Email: ptr("Boss@Example.com")})
	if err != nil || u.Role != domain.RoleAdmin || !u.Verified || !strings.HasPrefix(u.Password, "$2a$12$") {
		t.Fatalf("admin %+v %v", u, err)
	}
	golden(t, "config", e.do("GET", "/api/v2/config", "", nil).body)
}

func TestMailFlows(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.MailEnabled = true
		c.DisallowRegistration = false
		c.ReportEmail = "abuse@kutt.test"
	})
	// signup
	r := e.do("POST", "/api/v2/auth/signup", "", map[string]any{"email": "new@example.com", "password": "password1"})
	if r.code != 201 || r.body != `{"message":"A verification email has been sent."}` {
		t.Fatalf("%d %s", r.code, r.body)
	}
	if r := e.do("POST", "/api/v2/auth/signup", "", map[string]any{"email": "user@example.com", "password": "password1"}); r.body != `{"error":"You can't use this email address."}` {
		t.Fatal(r.body)
	}
	m := e.mail.all()
	if len(m) != 1 || m[0].kind != "verify" || m[0].to != "new@example.com" || len(m[0].token) != 36 {
		t.Fatalf("%+v", m)
	}
	// login before verification
	if r := e.do("POST", "/api/v2/auth/login", "", map[string]any{"email": "new@example.com", "password": "password1"}); r.code != 400 {
		t.Fatalf("unverified login %d %s", r.code, r.body)
	}
	r = e.do("POST", "/api/v2/auth/verify/"+m[0].token, "", nil)
	if r.code != 200 || !strings.Contains(r.body, `"token":"`) {
		t.Fatalf("verify %d %s", r.code, r.body)
	}
	if r := e.do("POST", "/api/v2/auth/login", "", map[string]any{"email": "new@example.com", "password": "password1"}); r.code != 200 {
		t.Fatalf("login after verify %d %s", r.code, r.body)
	}
	// reset password
	if r := e.do("POST", "/api/v2/auth/reset-password", "", map[string]any{"email": "user@example.com"}); r.body != `{"message":"If email address exists, a reset password email has been sent."}` {
		t.Fatal(r.body)
	}
	var reset sentMail
	for i := 0; i < 100 && reset.token == ""; i++ {
		for _, s := range e.mail.all() {
			if s.kind == "reset" {
				reset = s
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if reset.to != "user@example.com" {
		t.Fatalf("no reset mail: %+v", e.mail.all())
	}
	if r := e.do("POST", "/api/v2/auth/new-password", "", map[string]any{"reset_password_token": reset.token, "new_password": "brandnew1", "repeat_password": "brandnew1"}); r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	if r := e.do("POST", "/api/v2/auth/login", "", map[string]any{"email": "user@example.com", "password": "brandnew1"}); r.code != 200 {
		t.Fatalf("login after reset %d", r.code)
	}
	// change email
	jwt := e.jwt(e.user)
	if r := e.do("POST", "/api/v2/auth/change-email", jwt, map[string]any{"email": "admin@kutt.test", "password": "brandnew1"}); r.body != `{"error":"Can't use this email address."}` {
		t.Fatal(r.body)
	}
	if r := e.do("POST", "/api/v2/auth/change-email", jwt, map[string]any{"email": "moved@example.com", "password": "wrongpass"}); r.code != 401 {
		t.Fatal(r.body)
	}
	if r := e.do("POST", "/api/v2/auth/change-email", jwt, map[string]any{"email": "moved@example.com", "password": "brandnew1"}); r.body != `{"message":"A verification link has been sent to the requested email address."}` {
		t.Fatal(r.body)
	}
	var ch sentMail
	for _, s := range e.mail.all() {
		if s.kind == "change" {
			ch = s
		}
	}
	if ch.to != "moved@example.com" {
		t.Fatalf("%+v", e.mail.all())
	}
	if r := e.do("POST", "/api/v2/auth/verify-email/"+ch.token, "", nil); r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	if u, _ := e.st.Users().Find(t.Context(), store.UserFilter{ID: &e.user.ID}); u.Email != "moved@example.com" {
		t.Fatalf("email not changed: %s", u.Email)
	}
	// report
	runCases(t, e, []tc{
		{"report missing", "POST", "/links/report", "", map[string]any{}, 400, `{"error":"No link has been provided."}`},
		{"report foreign", "POST", "/links/report", "", map[string]any{"link": "https://evil.com/x"}, 400, `{"error":"You can only report a kutt.test link."}`},
		{"report ok", "POST", "/links/report", "", map[string]any{"link": "kutt.test/abc"}, 200, `{"message":"Thanks for the report, we'll take actions shortly."}`},
	})
}

func TestCreateLinkValidation(t *testing.T) {
	e := newEnv(t)
	e.st.SeedDomain(domain.Domain{Address: "banned.example", Banned: true})
	e.st.SeedDomain(domain.Domain{Address: "mine.link", UserID: &e.user.ID})
	e.st.SeedLink(domain.Link{Address: "taken", Target: "https://x.com"})
	e.dns["malware.example"] = "6.6.6.6"
	_ = e.st.Hosts().Ban(t.Context(), "6.6.6.6", e.admin.ID)
	only := `{"error":"Only users can use this field."}`
	runCases(t, e, []tc{
		{"missing target", "POST", "/links", "user", map[string]any{}, 400, `{"error":"Target is missing."}`},
		{"target not string", "POST", "/links", "user", map[string]any{"target": 5}, 400, `{"error":"Invalid value"}`},
		{"target too long", "POST", "/links", "user", map[string]any{"target": "https://a.com/" + strings.Repeat("a", 2040)}, 400, `{"error":"Maximum URL length is 2040."}`},
		{"target invalid", "POST", "/links", "user", map[string]any{"target": "not a url"}, 400, `{"error":"URL is not valid."}`},
		{"target private ip", "POST", "/links", "user", map[string]any{"target": "http://192.168.1.1/"}, 400, `{"error":"URL is not valid."}`},
		{"target default domain", "POST", "/links", "user", map[string]any{"target": "https://www.kutt.test/abc"}, 400, `{"error":"kutt.test URLs are not allowed."}`},
		{"password anon", "POST", "/links", "", map[string]any{"target": "https://a.com", "password": "abc"}, 400, only},
		{"customurl anon", "POST", "/links", "", map[string]any{"target": "https://a.com", "customurl": "abc"}, 400, only},
		{"reuse anon", "POST", "/links", "", map[string]any{"target": "https://a.com", "reuse": true}, 400, only},
		{"domain anon", "POST", "/links", "", map[string]any{"target": "https://a.com", "domain": "mine.link"}, 400, only},
		{"password short", "POST", "/links", "user", map[string]any{"target": "https://a.com", "password": "ab"}, 400, `{"error":"Password length must be between 3 and 64."}`},
		{"customurl long", "POST", "/links", "user", map[string]any{"target": "https://a.com", "customurl": strings.Repeat("a", 65)}, 400, `{"error":"Custom URL length must be between 1 and 64."}`},
		{"customurl chars", "POST", "/links", "user", map[string]any{"target": "https://a.com", "customurl": "a b"}, 400, `{"error":"Custom URL is not valid."}`},
		{"customurl preserved", "POST", "/links", "user", map[string]any{"target": "https://a.com", "customurl": "login"}, 400, `{"error":"You can't use this custom URL."}`},
		{"customurl taken", "POST", "/links", "user", map[string]any{"target": "https://a.com", "customurl": "taken"}, 500, `{"error":"Custom URL is already in use."}`},
		{"reuse not bool", "POST", "/links", "user", map[string]any{"target": "https://a.com", "reuse": "yes"}, 400, `{"error":"Reuse must be boolean."}`},
		{"description long", "POST", "/links", "user", map[string]any{"target": "https://a.com", "description": strings.Repeat("d", 2041)}, 400, `{"error":"Description length must be between 1 and 2040."}`},
		{"expire bad", "POST", "/links", "user", map[string]any{"target": "https://a.com", "expire_in": "soon"}, 400, `{"error":"Expire format is invalid. Valid examples: 1m, 8h, 42 days."}`},
		{"expire short", "POST", "/links", "user", map[string]any{"target": "https://a.com", "expire_in": "30s"}, 400, `{"error":"Expire time should be more than 1 minute."}`},
		{"domain not owned", "POST", "/links", "user", map[string]any{"target": "https://a.com", "domain": "other.link"}, 400, `{"error":"You can't use this domain."}`},
		{"domain not string", "POST", "/links", "user", map[string]any{"target": "https://a.com", "domain": 5}, 400, `{"error":"Domain should be string."}`},
		{"banned target domain", "POST", "/links", "user", map[string]any{"target": "https://www.banned.example/x"}, 400, `{"error":"Domain is banned."}`},
		{"banned host", "POST", "/links", "user", map[string]any{"target": "https://malware.example/x"}, 400, `{"error":"URL is containing malware/scam."}`},
	})
}

func TestAnonymousDisallowed(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.DisallowAnonymousLinks = true })
	r := e.do("POST", "/api/v2/links", "", map[string]any{"target": "https://a.com"})
	if r.code != 401 || r.body != `{"error":"Unauthorized."}` {
		t.Fatalf("%d %s", r.code, r.body)
	}
}

func TestLinkLifecycle(t *testing.T) {
	e := newEnv(t)
	e.st.SeedDomain(domain.Domain{Address: "mine.link", UserID: &e.user.ID, Homepage: ptr("https://home.mine.link")})
	// create (default domain, custom url, password, expire, description)
	r := e.do("POST", "/api/v2/links", "user", map[string]any{
		"target": "example.com/path?q=1", "customurl": "my-link", "password": "secret", "expire_in": "2 hours", "description": "  hello  "})
	if r.code != 201 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	m := r.json(t)
	id := m["id"].(string)
	if m["target"] != "http://example.com/path?q=1" || m["password"] != true || m["link"] != "https://kutt.test/my-link" ||
		m["description"] != "hello" || m["expire_in"] != "2026-09-28T14:00:00.000Z" || m["visit_count"] != float64(0) {
		t.Fatalf("create json %s", r.body)
	}
	if _, has := m["domain"]; has {
		t.Fatalf("default-domain create must omit domain key (links.handler.js create): %s", r.body)
	}
	golden(t, "link_create", strings.ReplaceAll(r.body, id, "UUID"))
	if inv := e.res.Invalidations(); len(inv) != 1 || inv[0] != "my-link@default" {
		t.Fatalf("create invalidation %v", inv)
	}
	// anonymous create
	r = e.do("POST", "/api/v2/links", "", map[string]any{"target": "https://anon.example"})
	if r.code != 201 || !strings.HasPrefix(r.json(t)["link"].(string), "https://kutt.test/") || len(r.json(t)["address"].(string)) != 6 {
		t.Fatalf("anon %d %s", r.code, r.body)
	}
	// custom domain + reuse
	r = e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://reuse.me", "domain": "MINE.link"})
	if r.code != 201 || r.json(t)["link"] != "http://mine.link/"+r.json(t)["address"].(string) || r.json(t)["domain"] != "mine.link" {
		t.Fatalf("domain link %d %s", r.code, r.body)
	}
	first := r.json(t)["id"]
	r = e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://reuse.me", "domain": "mine.link", "reuse": true})
	if r.code != 200 || r.json(t)["id"] != first {
		t.Fatalf("reuse %d %s", r.code, r.body)
	}
	// list
	r = e.do("GET", "/api/v2/links?limit=1", "user", nil)
	lm := r.json(t)
	if r.code != 200 || lm["total"] != float64(2) || lm["limit"] != float64(1) || lm["skip"] != float64(0) || len(lm["data"].([]any)) != 1 {
		t.Fatalf("list %s", r.body)
	}
	if r := e.do("GET", "/api/v2/links?limit=1&limit=2", "user", nil); r.code != 400 || r.body != `{"error":"limit query is not valid."}` {
		t.Fatalf("dup limit %d %s", r.code, r.body)
	}
	if r := e.do("GET", "/api/v2/links?limit=500", "user", nil); r.json(t)["limit"] != float64(50) {
		t.Fatalf("limit cap %s", r.body)
	}
	// edit
	runCases(t, e, []tc{
		{"edit bad id", "PATCH", "/links/abc", "user", map[string]any{"target": "https://b.com"}, 400, `{"error":"ID is invalid."}`},
		{"edit other's", "PATCH", "/links/" + id, e.jwt(e.admin) + "x", map[string]any{"target": "https://b.com"}, 401, ""},
		{"edit nothing", "PATCH", "/links/" + id, "user", map[string]any{"password": "••••••"}, 500, `{"error":"Should at least update one field."}`},
		{"edit bad address", "PATCH", "/links/" + id, "user", map[string]any{"address": "a/b"}, 400, `{"error":"Custom URL is not valid"}`},
		{"edit expire short", "PATCH", "/links/" + id, "user", map[string]any{"expire_in": "1s"}, 400, `{"error":"Expire time should be more than 1 minute."}`},
		{"edit missing", "PATCH", "/links/22222222-2222-2222-2222-222222222222", "user", map[string]any{"target": "https://b.com"}, 500, `{"error":"Link was not found."}`},
	})
	r = e.do("PATCH", "/api/v2/links/"+id, "user", map[string]any{"address": "renamed", "target": "https://b.com", "password": "••••••"})
	if r.code != 200 || r.json(t)["address"] != "renamed" || r.json(t)["password"] != true || r.json(t)["link"] != "https://kutt.test/renamed" {
		t.Fatalf("edit %d %s", r.code, r.body)
	}
	inv := strings.Join(e.res.Invalidations(), ",")
	if !strings.Contains(inv, "my-link@default") || !strings.Contains(inv, "renamed@default") {
		t.Fatalf("edit must invalidate old and new address: %s", inv)
	}
	// clearing the password: empty password when the link has one
	r = e.do("PATCH", "/api/v2/links/"+id, "user", map[string]any{"password": ""})
	if r.code != 200 || r.json(t)["password"] != false {
		t.Fatalf("clear pw %d %s", r.code, r.body)
	}
	// stats
	r = e.do("GET", "/api/v2/links/"+id+"/stats", "user", nil)
	if r.code != 200 {
		t.Fatalf("stats %d %s", r.code, r.body)
	}
	golden(t, "link_stats", strings.ReplaceAll(r.body, id, "UUID"))
	if r := e.do("GET", "/api/v2/links/"+id+"/stats", "admin", nil); r.code != 200 {
		t.Fatalf("admin stats %d", r.code)
	}
	if r := e.do("GET", "/api/v2/links/33333333-3333-3333-3333-333333333333/stats", "user", nil); r.body != `{"error":"Link could not be found."}` {
		t.Fatal(r.body)
	}
	// admin list golden
	r = e.do("GET", "/api/v2/links/admin?domain=kutt.test", "admin", nil)
	if r.code != 200 || r.json(t)["total"] != float64(2) {
		t.Fatalf("admin list default domain %s", r.body)
	}
	// delete
	before := len(e.res.Invalidations())
	if r := e.do("DELETE", "/api/v2/links/"+id, "user", nil); r.body != `{"message":"Link has been deleted successfully."}` {
		t.Fatal(r.body)
	}
	if inv := e.res.Invalidations(); len(inv) != before+1 || inv[len(inv)-1] != "renamed@default" {
		t.Fatalf("delete invalidation %v", inv)
	}
	if r := e.do("DELETE", "/api/v2/links/"+id, "user", nil); r.body != `{"error":"Could not find the link."}` {
		t.Fatal(r.body)
	}
}

func TestLinkJSONGolden(t *testing.T) {
	e := newEnv(t)
	d := e.st.SeedDomain(domain.Domain{Address: "mine.link", UserID: &e.user.ID, UUID: "dddddddd-dddd-dddd-dddd-dddddddddddd"})
	exp := testNow.Add(90 * time.Minute)
	e.st.SeedLink(domain.Link{UUID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Address: "abc", Target: "https://a.com/<x>&y", UserID: &e.user.ID,
		Description: ptr("desc"), VisitCount: 1234567, ExpireIn: &exp, CreatedAt: testNow.Add(-49 * time.Hour), UpdatedAt: testNow.Add(-time.Hour)})
	e.st.SeedLink(domain.Link{UUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Address: "xyz", Target: "https://b.com", UserID: &e.user.ID,
		DomainID: &d.ID, Password: ptr(hash(t, "pw1")), Banned: true, CreatedAt: testNow.Add(-400 * 24 * time.Hour), UpdatedAt: testNow})
	golden(t, "links_list", e.do("GET", "/api/v2/links", "user", nil).body)
	golden(t, "links_admin", e.do("GET", "/api/v2/links/admin", "admin", nil).body)
	golden(t, "users_get", e.do("GET", "/api/v2/users", "user", nil).body)
	golden(t, "users_admin", e.do("GET", "/api/v2/users/admin", "admin", nil).body)
	golden(t, "domains_admin", e.do("GET", "/api/v2/domains/admin", "admin", nil).body)
}

func TestBanLink(t *testing.T) {
	e := newEnv(t)
	l := e.st.SeedLink(domain.Link{Address: "evil", Target: "https://www.evil.example/x", UserID: &e.user.ID})
	e.st.SeedLink(domain.Link{Address: "evil2", Target: "https://other.example/", UserID: &e.user.ID})
	e.dns["evil.example"] = "6.6.6.7"
	runCases(t, e, []tc{
		{"bad id", "POST", "/links/admin/ban/xyz", "admin", map[string]any{}, 400, `{"error":"ID is invalid."}`},
		{"bad bool", "POST", "/links/admin/ban/" + l.UUID, "admin", map[string]any{"host": "maybe"}, 400, `{"error":"\"host\" should be a boolean."}`},
		{"missing", "POST", "/links/admin/ban/44444444-4444-4444-4444-444444444444", "admin", map[string]any{}, 400, `{"error":"No link has been found."}`},
	})
	r := e.do("POST", "/api/v2/links/admin/ban/"+l.UUID, "admin", "host=on&domain=true&user=true&userLinks=true")
	if r.code != 200 || r.body != `{"message":"Banned link successfully."}` {
		t.Fatalf("%d %s", r.code, r.body)
	}
	if r := e.do("POST", "/api/v2/links/admin/ban/"+l.UUID, "admin", map[string]any{}); r.body != `{"error":"Link has been banned already."}` {
		t.Fatal(r.body)
	}
	ctx := t.Context()
	if d, err := e.st.Domains().Find(ctx, store.DomainFilter{Address: ptr("evil.example")}); err != nil || !d.Banned {
		t.Fatalf("domain not banned %v", err)
	}
	if h, err := e.st.Hosts().Find(ctx, "6.6.6.7"); err != nil || !h.Banned {
		t.Fatalf("host not banned")
	}
	if u, _ := e.st.Users().Find(ctx, store.UserFilter{ID: &e.user.ID}); !u.Banned {
		t.Fatal("user not banned")
	}
	inv := strings.Join(e.res.Invalidations(), ",")
	if !strings.Contains(inv, "evil@default") || !strings.Contains(inv, "evil2@default") {
		t.Fatalf("ban invalidations %s", inv)
	}
	if di := e.res.DomainInvalidations(); len(di) == 0 || di[0] != "evil.example" {
		t.Fatalf("domain invalidations %v", di)
	}
}

func TestUsersAdmin(t *testing.T) {
	e := newEnv(t)
	runCases(t, e, []tc{
		{"create invalid pw", "POST", "/users/admin", "admin", map[string]any{"email": "x@y.co"}, 400, `{"error":"Password is not valid."}`},
		{"create exists", "POST", "/users/admin", "admin", map[string]any{"email": "user@example.com", "password": "password1"}, 400, `{"error":"User already exists."}`},
		{"create bad role", "POST", "/users/admin", "admin", map[string]any{"email": "r@y.co", "password": "password1", "role": "ROOT"}, 400, `{"error":"Role is not valid."}`},
		{"delete bad id", "DELETE", "/users/admin/abc", "admin", nil, 400, `{"error":"ID is invalid."}`},
		{"delete missing", "DELETE", "/users/admin/999", "admin", nil, 400, `{"message":"Could not find the user."}`},
		{"ban missing", "POST", "/users/admin/ban/999", "admin", map[string]any{}, 400, `{"error":"No user has been found."}`},
		{"ban bad bool", "POST", "/users/admin/ban/2", "admin", map[string]any{"links": "x"}, 400, `{"error":"\"links\" should be a boolean."}`},
	})
	r := e.do("POST", "/api/v2/users/admin", "admin", map[string]any{"email": "made@example.com", "password": "password1", "role": "ADMIN", "verified": true, "banned": "on"})
	if r.code != 201 || r.body != `{"message":"The user has been created successfully."}` {
		t.Fatalf("%d %s", r.code, r.body)
	}
	u, err := e.st.Users().Find(t.Context(), store.UserFilter{Email: ptr("made@example.com")})
	if err != nil || u.Role != domain.RoleAdmin || !u.Verified || !u.Banned {
		t.Fatalf("%+v", u)
	}
	l := e.st.SeedLink(domain.Link{Address: "ul", Target: "https://x.y", UserID: &e.user.ID})
	if r := e.do("POST", "/api/v2/users/admin/ban/2", "admin", map[string]any{"links": true, "domains": true}); r.body != `{"message":"Banned user successfully."}` {
		t.Fatal(r.body)
	}
	if r := e.do("POST", "/api/v2/users/admin/ban/2", "admin", map[string]any{}); r.body != `{"error":"User has been banned already."}` {
		t.Fatal(r.body)
	}
	if got, _ := e.st.Links().Find(t.Context(), store.LinkFilter{ID: &l.ID}); !got.Banned {
		t.Fatal("user links not banned")
	}
	if r := e.do("DELETE", "/api/v2/users/admin/2", "admin", nil); r.body != `{"message":"User has been deleted successfully."}` {
		t.Fatal(r.body)
	}
	if inv := strings.Join(e.res.Invalidations(), ","); !strings.Contains(inv, "ul@default") {
		t.Fatalf("user delete invalidation %s", inv)
	}
}

func TestDeleteOwnAccount(t *testing.T) {
	e := newEnv(t)
	runCases(t, e, []tc{
		{"wrong pw", "POST", "/users/delete", "user", map[string]any{"password": "wrongpass"}, 400, `{"error":"Password is not correct."}`},
		{"short pw", "POST", "/users/delete", "user", map[string]any{"password": "short"}, 400, `{"error":"Password is not valid."}`},
	})
	r := e.do("POST", "/api/v2/users/delete", "user", map[string]any{"password": "userpass1"})
	if r.code != 200 || !strings.Contains(r.hdr.Get("Set-Cookie"), "token=;") {
		t.Fatalf("%d %s %s", r.code, r.body, r.hdr.Get("Set-Cookie"))
	}
	if _, err := e.st.Users().Find(t.Context(), store.UserFilter{ID: &e.user.ID}); err == nil {
		t.Fatal("not deleted")
	}
}

func TestDomains(t *testing.T) {
	e := newEnv(t)
	e.st.SeedDomain(domain.Domain{Address: "owned.example", UserID: &e.admin.ID})
	e.st.SeedDomain(domain.Domain{Address: "bad.example", Banned: true})
	runCases(t, e, []tc{
		{"missing", "POST", "/domains", "user", map[string]any{}, 400, `{"error":"Domain is not valid."}`},
		{"short", "POST", "/domains", "user", map[string]any{"address": "ab"}, 400, `{"error":"Domain length must be between 3 and 64."}`},
		{"invalid", "POST", "/domains", "user", map[string]any{"address": "not a domain"}, 400, `{"error":"Domain is not valid."}`},
		{"default", "POST", "/domains", "user", map[string]any{"address": "https://www.kutt.test"}, 400, `{"error":"You can't use the default domain."}`},
		{"owned", "POST", "/domains", "user", map[string]any{"address": "owned.example"}, 400, `{"error":"You can't add this domain."}`},
		{"banned", "POST", "/domains", "user", map[string]any{"address": "bad.example"}, 400, `{"error":"You can't add this domain."}`},
		{"homepage bad", "POST", "/domains", "user", map[string]any{"address": "fresh.example", "homepage": "nope nope"}, 400, `{"error":"Homepage is not valid."}`},
		{"admin exists", "POST", "/domains/admin", "admin", map[string]any{"address": "owned.example"}, 400, `{"error":"Domain already exists."}`},
		{"admin default", "POST", "/domains/admin", "admin", map[string]any{"address": "kutt.test"}, 400, `{"error":"You can't add the default domain."}`},
		{"remove bad id", "DELETE", "/domains/xyz", "user", nil, 400, `{"error":"ID is invalid."}`},
		{"remove not mine", "DELETE", "/domains/55555555-5555-5555-5555-555555555555", "user", nil, 400, `{"error":"Could not delete the domain."}`},
		{"admin remove bad", "DELETE", "/domains/admin/x", "admin", nil, 400, `{"error":"ID is invalid."}`},
		{"admin remove missing", "DELETE", "/domains/admin/999", "admin", nil, 400, `{"error":"Could not find the domain."}`},
		{"admin ban missing", "POST", "/domains/admin/ban/999", "admin", map[string]any{}, 400, `{"error":"No domain has been found."}`},
	})
	r := e.do("POST", "/api/v2/domains", "user", map[string]any{"address": "https://www.Go.Mine.IO/path", "homepage": "mine.io"})
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	m := r.json(t)
	if m["address"] != "go.mine.io" || m["homepage"] != "http://mine.io" || m["banned"] != false {
		t.Fatalf("%s", r.body)
	}
	did := m["id"].(string)
	golden(t, "domain_add", strings.ReplaceAll(r.body, did, "UUID"))
	if di := e.res.DomainInvalidations(); len(di) != 1 || di[0] != "go.mine.io" {
		t.Fatalf("%v", di)
	}
	if r := e.do("GET", "/api/v2/users", "user", nil); !strings.Contains(r.body, `"address":"go.mine.io"`) {
		t.Fatal(r.body)
	}
	if r := e.do("DELETE", "/api/v2/domains/"+did, "user", nil); r.body != `{"message":"Domain deleted successfully"}` {
		t.Fatal(r.body)
	}
	if d, _ := e.st.Domains().Find(t.Context(), store.DomainFilter{UUID: &did}); d == nil || d.UserID != nil {
		t.Fatal("domain should be released, not deleted")
	}
	// admin add banned, ban, remove with links
	if r := e.do("POST", "/api/v2/domains/admin", "admin", map[string]any{"address": "spam.example", "banned": "on"}); r.body != `{"message":"The domain has been added successfully."}` {
		t.Fatal(r.body)
	}
	if d, _ := e.st.Domains().Find(t.Context(), store.DomainFilter{Address: ptr("spam.example")}); !d.Banned {
		t.Fatal("not banned")
	}
	od, _ := e.st.Domains().Find(t.Context(), store.DomainFilter{Address: ptr("owned.example")})
	e.st.SeedLink(domain.Link{Address: "dl", Target: "https://t.t", DomainID: &od.ID, UserID: &e.admin.ID})
	idStr := itoa(od.ID)
	if r := e.do("POST", "/api/v2/domains/admin/ban/"+idStr, "admin", map[string]any{"links": true}); r.body != `{"message":"Banned domain successfully."}` {
		t.Fatal(r.body)
	}
	if r := e.do("POST", "/api/v2/domains/admin/ban/"+idStr, "admin", map[string]any{}); r.body != `{"error":"Domain has been banned already."}` {
		t.Fatal(r.body)
	}
	if r := e.do("DELETE", "/api/v2/domains/admin/"+idStr+"?links=true", "admin", nil); r.body != `{"message":"Domain deleted successfully"}` {
		t.Fatal(r.body)
	}
	if n, _ := e.st.Links().Total(t.Context(), store.LinkFilter{DomainID: &od.ID}, ""); n != 0 {
		t.Fatal("domain links not removed")
	}
	if inv := strings.Join(e.res.Invalidations(), ","); !strings.Contains(inv, "dl@"+idStr) {
		t.Fatalf("invalidations %s", inv)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestRateLimit(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.EnableRateLimit = true })
	for i := 0; i < 5; i++ {
		r := e.do("POST", "/api/v2/auth/login", "", map[string]any{"email": "user@example.com", "password": "wrongpass"})
		if r.code != 401 || r.hdr.Get("X-RateLimit-Limit") != "5" {
			t.Fatalf("attempt %d: %d %v", i, r.code, r.hdr)
		}
	}
	r := e.do("POST", "/api/v2/auth/login", "", map[string]any{"email": "user@example.com", "password": "userpass1"})
	if r.code != 429 || r.body != `{"error":"Too many requests, please try again later."}` || r.hdr.Get("Retry-After") == "" {
		t.Fatalf("%d %s", r.code, r.body)
	}
	// separate window per route path (baseUrl+path): /api/auth/login is independent
	if r := e.do("POST", "/api/auth/login", "", map[string]any{"email": "user@example.com", "password": "userpass1"}); r.code != 200 {
		t.Fatalf("v1 path shares window: %d", r.code)
	}
	// validation errors do not consume the limit (limiter runs after verify)
	e2 := newEnv(t, func(c *config.Config) { c.EnableRateLimit = true })
	for i := 0; i < 10; i++ {
		if r := e2.do("POST", "/api/v2/auth/login", "", map[string]any{}); r.code != 400 {
			t.Fatalf("%d", r.code)
		}
	}
	if r := e2.do("POST", "/api/v2/auth/login", "", map[string]any{"email": "user@example.com", "password": "userpass1"}); r.code != 200 {
		t.Fatalf("%d", r.code)
	}
}

func TestStatsCache(t *testing.T) {
	e := newEnv(t)
	l := e.st.SeedLink(domain.Link{Address: "s", Target: "https://s.s", UserID: &e.user.ID})
	e.st.Visits[l.ID] = &store.Stats{LastDay: store.PeriodStats{Total: 3, Views: []int64{1, 2}}, UpdatedAt: testNow}
	first := e.do("GET", "/api/v2/links/"+l.UUID+"/stats", "user", nil).body
	e.st.Visits[l.ID] = &store.Stats{LastDay: store.PeriodStats{Total: 99}, UpdatedAt: testNow}
	second := e.do("GET", "/api/v2/links/"+l.UUID+"/stats", "user", nil).body
	if first != second || !strings.Contains(first, `"lastDay":{"stats":{"browser":[],"os":[],"country":[],"referrer":[]},"views":[1,2],"total":3}`) {
		t.Fatalf("stats cache\n%s\n%s", first, second)
	}
	var _ = http.StatusOK
}
