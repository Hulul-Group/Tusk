package api

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Hulul-Group/Tusk/internal/api/apitest"
	"github.com/Hulul-Group/Tusk/internal/auth"
	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

var testNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

type sentMail struct{ kind, to, token string }

type fakeMailer struct {
	enabled bool
	mu      sync.Mutex
	sent    []sentMail
}

func (m *fakeMailer) Enabled() bool { return m.enabled }
func (m *fakeMailer) add(k, to, tok string) error {
	m.mu.Lock()
	m.sent = append(m.sent, sentMail{k, to, tok})
	m.mu.Unlock()
	return nil
}
func (m *fakeMailer) SendVerification(_ context.Context, to, t string) error {
	return m.add("verify", to, t)
}
func (m *fakeMailer) SendChangeEmail(_ context.Context, to, t string) error {
	return m.add("change", to, t)
}
func (m *fakeMailer) SendResetPassword(_ context.Context, to, t string) error {
	return m.add("reset", to, t)
}
func (m *fakeMailer) SendReport(_ context.Context, l string) error { return m.add("report", "", l) }
func (m *fakeMailer) all() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sentMail(nil), m.sent...)
}

type env struct {
	t      testing.TB
	cfg    *config.Config
	st     *apitest.Store
	res    *apitest.Resolver
	mail   *fakeMailer
	srv    *Server
	h      http.Handler
	admin  *domain.User
	user   *domain.User
	adminK string
	userK  string
	dns    map[string]string
}

func testConfig() *config.Config {
	c, err := config.LoadFrom(func(k string) (string, bool) {
		m := map[string]string{"DEFAULT_DOMAIN": "kutt.test", "JWT_SECRET": "test-secret", "NODE_ENV": "production",
			"DB_CLIENT": "better-sqlite3", "DISALLOW_ANONYMOUS_LINKS": "false"}
		v, ok := m[k]
		return v, ok
	})
	if err != nil {
		panic(err)
	}
	return c
}

func hash(t testing.TB, pw string) string {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 4)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func newEnv(t testing.TB, mutate ...func(*config.Config)) *env {
	cfg := testConfig()
	for _, m := range mutate {
		m(cfg)
	}
	st := apitest.New(testNow)
	res := &apitest.Resolver{S: st}
	e := &env{t: t, cfg: cfg, st: st, res: res, mail: &fakeMailer{enabled: cfg.MailEnabled}, dns: map[string]string{}}
	e.adminK, e.userK = "admin-api-key-000000000000000000000000000", "user-api-key-0000000000000000000000000000"
	e.admin = st.SeedUser(domain.User{Email: "admin@kutt.test", Password: hash(t, "adminpass"), Role: domain.RoleAdmin, Verified: true, APIKey: &e.adminK})
	e.user = st.SeedUser(domain.User{Email: "user@example.com", Password: hash(t, "userpass1"), Role: domain.RoleUser, Verified: true, APIKey: &e.userK})
	svc := auth.New(st.Users(), cfg.JWTSecret, auth.Mode{Prod: true})
	svc.Tokens.SetClock(func() time.Time { return testNow })
	e.srv = New(Options{
		Config: cfg, Store: st.AsStore(), Links: res, Domains: apitest.DomainResolver{Resolver: res},
		Auth: svc, Mailer: e.mail, Mode: auth.Mode{Prod: true},
		Now: func() time.Time { return testNow },
		LookupIP: func(_ context.Context, h string) (string, error) {
			if ip, ok := e.dns[h]; ok {
				return ip, nil
			}
			return "", io.EOF
		},
	})
	mux := http.NewServeMux()
	e.srv.Register(mux)
	e.h = mux
	return e
}

type resp struct {
	code int
	hdr  http.Header
	body string
}

func (r resp) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("not json object: %q", r.body)
	}
	return m
}

// do sends a request. auth: "" none, "admin"/"user" API key, "cookie:<jwt>".
func (e *env) do(method, path, auth string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd, ct = strings.NewReader(b), "application/x-www-form-urlencoded"
	default:
		j, _ := json.Marshal(b)
		rd, ct = bytes.NewReader(j), "application/json"
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = "kutt.test"
	req.RemoteAddr = "203.0.113.9:5555"
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")
	switch {
	case auth == "admin":
		req.Header.Set("X-API-KEY", e.adminK)
	case auth == "user":
		req.Header.Set("X-API-KEY", e.userK)
	case strings.HasPrefix(auth, "cookie:"):
		req.AddCookie(&http.Cookie{Name: "token", Value: strings.TrimPrefix(auth, "cookie:")})
	case strings.HasPrefix(auth, "key:"):
		req.Header.Set("X-API-KEY", strings.TrimPrefix(auth, "key:"))
	}
	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, req)
	return resp{rr.Code, rr.Header(), rr.Body.String()}
}

func (e *env) jwt(u *domain.User) string {
	tok, err := e.srv.o.Auth.Tokens.Sign(u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return "cookie:" + tok
}

// golden compares body to testdata/<name>.golden (use -update to rewrite).
func golden(t *testing.T, name, body string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("missing golden %s (run go test -update): %v", p, err)
	}
	if string(want) != body {
		t.Fatalf("golden %s mismatch\n got: %s\nwant: %s", name, body, want)
	}
}
