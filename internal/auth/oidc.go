package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// OIDCConfig mirrors the OIDC_* env vars.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scope        string // space separated (OIDC_SCOPE)
	Prompt       string // OIDC_PROMPT, optional
	EmailClaim   string // OIDC_EMAIL_CLAIM
	RedirectURL  string // utils.getSiteURL() + "/login/oidc" (passport.js:93)
	CookieSecret string // JWT_SECRET (Kutt: cookie-session keys: [JWT_SECRET], server.js:47)
	Secure       bool
}

// MsgOIDCFailed is Kutt's OIDC error (auth.handler.js:25).
const MsgOIDCFailed = "OIDC authentication failed."

const oidcCookie = "tusk_oidc"

// OIDC implements passport.js's openid-client strategy: authorization-code
// flow with state, nonce and PKCE S256 (openid-client 5.x passport_strategy
// defaults usePKCE=true -> S256). Instead of cookie-session, the per-login
// state is kept in a short-lived HMAC-signed httpOnly cookie.
type OIDC struct {
	cfg   OIDCConfig
	users store.UserStore

	once     sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
	now      func() time.Time
}

// NewOIDC returns a lazily-discovering OIDC handler (discovery on first use
// so the API starts even if the IdP is briefly down, like Kutt's async enableOIDC).
func NewOIDC(cfg OIDCConfig, users store.UserStore) (*OIDC, error) {
	for k, v := range map[string]string{"OIDC_ISSUER": cfg.Issuer, "OIDC_CLIENT_ID": cfg.ClientID,
		"OIDC_CLIENT_SECRET": cfg.ClientSecret, "OIDC_SCOPE": cfg.Scope, "OIDC_EMAIL_CLAIM": cfg.EmailClaim} {
		if v == "" {
			return nil, fmt.Errorf("Missing required env %s", k)
		}
	}
	return &OIDC{cfg: cfg, users: users, now: time.Now}, nil
}

func (o *OIDC) init(ctx context.Context) error {
	o.once.Lock()
	defer o.once.Unlock()
	if o.provider != nil {
		return nil
	}
	p, err := oidc.NewProvider(ctx, o.cfg.Issuer)
	if err != nil {
		return fmt.Errorf("Failed connecting to OIDC issuer: %w", err)
	}
	o.provider = p
	o.verifier = p.Verifier(&oidc.Config{ClientID: o.cfg.ClientID})
	o.oauth = &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  o.cfg.RedirectURL,
		Scopes:       strings.Fields(o.cfg.Scope),
	}
	return nil
}

type oidcState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Exp      int64  `json:"e"`
}

func randToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (o *OIDC) sign(payload []byte) string {
	m := hmac.New(sha256.New, []byte(o.cfg.CookieSecret))
	m.Write([]byte("tusk-oidc:"))
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (o *OIDC) unsign(v string) ([]byte, bool) {
	i := strings.LastIndexByte(v, '.')
	if i < 0 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(v[:i])
	if err != nil {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(v[i+1:])
	if err != nil {
		return nil, false
	}
	m := hmac.New(sha256.New, []byte(o.cfg.CookieSecret))
	m.Write([]byte("tusk-oidc:"))
	m.Write(payload)
	return payload, hmac.Equal(sig, m.Sum(nil))
}

// Begin redirects to the IdP (first leg of GET /login/oidc).
func (o *OIDC) Begin(w http.ResponseWriter, r *http.Request) error {
	if err := o.init(r.Context()); err != nil {
		return err
	}
	st := oidcState{State: randToken(), Nonce: randToken(), Verifier: oauth2.GenerateVerifier(),
		Exp: o.now().Add(10 * time.Minute).Unix()}
	b, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: o.sign(b), Path: "/login/oidc",
		MaxAge: 600, HttpOnly: true, Secure: o.cfg.Secure, SameSite: http.SameSiteLaxMode})
	opts := []oauth2.AuthCodeOption{oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)}
	if o.cfg.Prompt != "" {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", o.cfg.Prompt))
	}
	http.Redirect(w, r, o.oauth.AuthCodeURL(st.State, opts...), http.StatusFound)
	return nil
}

// Callback completes the flow (second leg of GET /login/oidc?code=...) and
// returns the local user, creating it like passport.js:108-129 when absent.
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) (*domain.User, error) {
	fail := domain.NewError(http.StatusUnauthorized, MsgOIDCFailed)
	if err := o.init(r.Context()); err != nil {
		return nil, fail
	}
	c, err := r.Cookie(oidcCookie)
	if err != nil {
		return nil, fail
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/login/oidc", MaxAge: -1, HttpOnly: true, Secure: o.cfg.Secure})
	payload, ok := o.unsign(c.Value)
	if !ok {
		return nil, fail
	}
	var st oidcState
	if json.Unmarshal(payload, &st) != nil || st.Exp < o.now().Unix() {
		return nil, fail
	}
	q := r.URL.Query()
	if q.Get("error") != "" || q.Get("state") != st.State || q.Get("code") == "" {
		return nil, fail
	}
	tok, err := o.oauth.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return nil, fail
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, fail
	}
	idt, err := o.verifier.Verify(r.Context(), rawID)
	if err != nil || idt.Nonce != st.Nonce {
		return nil, fail
	}
	// openid-client's strategy passes `userinfo` (fetched from the userinfo
	// endpoint when available); fall back to ID-token claims.
	claims := map[string]any{}
	_ = idt.Claims(&claims)
	if ui, err := o.provider.UserInfo(r.Context(), oauth2.StaticTokenSource(tok)); err == nil {
		uc := map[string]any{}
		if ui.Claims(&uc) == nil {
			for k, v := range uc {
				claims[k] = v
			}
		}
	}
	email, _ := claims[o.cfg.EmailClaim].(string)
	if email == "" {
		return nil, fail
	}
	return o.findOrCreate(r.Context(), email)
}

func (o *OIDC) findOrCreate(ctx context.Context, email string) (*domain.User, error) {
	e := email
	u, err := o.users.Find(ctx, store.UserFilter{Email: &e})
	if err == nil && u != nil {
		return u, nil
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	// passport.js:117-128: random password (never usable), verified=true,
	// no verification token. NOTE: Kutt stores the random password UNHASHED
	// (query.user.add receives the raw string); we hash it, which is strictly
	// safer and indistinguishable to users (nobody knows either value).
	hash, err := HashPassword(GenerateRandomPassword())
	if err != nil {
		return nil, err
	}
	return o.users.Create(ctx, store.CreateUser{Email: email, Password: hash, Role: domain.RoleUser, Verified: true})
}

// GenerateRandomPassword ports utils.generateRandomPassword: 24-64 chars from '!'..'}'.
func GenerateRandomPassword() string {
	var lb [1]byte
	_, _ = rand.Read(lb[:])
	n := 24 + int(lb[0])%41
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = b[i]%93 + 33
	}
	return string(b)
}
