package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// CookieName is the JWT cookie (passport.js:12, utils.js:51).
const CookieName = "token"

// APIKeyHeader is passport-localapikey-update's apiKeyHeader (passport.js:60).
// The strategy also reads body.apikey and query.apikey (strategy.js:71-73).
const APIKeyHeader = "X-API-KEY"

// Kutt error messages (auth.handler.js:84-95).
const (
	MsgUnauthorized = "Unauthorized."
	MsgBadAPIKey    = "API key is not correct."
	MsgBanned       = "You're banned from using this website."
	MsgUnverified   = "Your email address is not verified. Sign up to get the verification link again."
	MsgAdminOnly    = "Unauthorized"
	MsgLoginWrong   = "Login credentials are wrong."
)

// Service ties token signing, cookies and user lookup together.
type Service struct {
	Users  store.UserStore
	Tokens *Tokens
	Mode   Mode
}

// New creates the auth service.
func New(users store.UserStore, secret string, mode Mode) *Service {
	return &Service{Users: users, Tokens: NewTokens(secret), Mode: mode}
}

// SetTokenCookie mirrors utils.setToken: httpOnly, SameSite=Lax, secure in prod, maxAge 7d.
func (s *Service) SetTokenCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(TokenTTL / time.Second),
		Expires:  time.Now().Add(TokenTTL),
		HttpOnly: true,
		Secure:   s.Mode.Prod,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearTokenCookie mirrors utils.deleteCurrentToken.
func (s *Service) ClearTokenCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		Expires: time.Unix(0, 0), HttpOnly: true, Secure: s.Mode.Prod,
	})
}

// ---------- context ----------

type ctxKey struct{}

// Principal is the authenticated user plus how they authenticated.
type Principal struct {
	User   *domain.User
	Admin  bool
	Method string // "jwt" | "apikey"
	Exp    time.Time
}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal or nil.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

// UserFrom returns the authenticated user or nil.
func UserFrom(ctx context.Context) *domain.User {
	if p := FromContext(ctx); p != nil {
		return p.User
	}
	return nil
}

// ---------- strategies ----------

// Error is an HTTP-status-carrying auth failure, rendered by the api package.
type Error = domain.AppError

func errf(status int, msg string) *domain.AppError { return domain.NewError(status, msg) }

// APIKeyFrom extracts the key like passport-localapikey-update: body.apikey,
// then query.apikey, then the x-api-key header. bodyKey is supplied by the
// caller because the body is parsed once by the API layer.
func APIKeyFrom(r *http.Request, bodyKey string) string {
	if bodyKey != "" {
		return bodyKey
	}
	if q := r.URL.Query().Get("apikey"); q != "" {
		return q
	}
	return r.Header.Get(APIKeyHeader)
}

// userFromJWT resolves the cookie token to a user (passport.js:16-31).
func (s *Service) userFromJWT(ctx context.Context, r *http.Request) (*domain.User, *Claims, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, nil, nil
	}
	claims, err := s.Tokens.Verify(c.Value)
	if err != nil {
		return nil, nil, nil
	}
	id := claims.UserID
	u, err := s.Users.Find(ctx, store.UserFilter{ID: &id})
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return u, claims, nil
}

// Authenticate runs Kutt's chain "auth.apikey then auth.jwt|jwtLoose" and
// returns the principal (nil if anonymous) or an *AppError.
//
//	strict=true  -> auth.jwt  : no user => 401 "Unauthorized."; unverified => 400.
//	strict=false -> auth.jwtLoose: anonymous allowed.
//
// apikey strategy is never strict (isStrict=false) but a *present and wrong*
// key does not fail by itself in passport: fail() with isStrict=false passes
// through; then jwt runs. Banned users get 403 from either strategy.
func (s *Service) Authenticate(ctx context.Context, r *http.Request, bodyAPIKey string, strict bool) (*Principal, error) {
	return s.AuthenticateWith(ctx, r, APIKeyFrom(r, bodyAPIKey), true, strict)
}

// AuthenticateWith is Authenticate with an explicit key. useAPIKey=false
// models routes that only chain auth.jwt (e.g. /auth/change-password,
// /auth/apikey in auth.routes.js), where X-API-KEY is ignored.
func (s *Service) AuthenticateWith(ctx context.Context, r *http.Request, key string, useAPIKey, strict bool) (*Principal, error) {
	// 1. API key (auth.apikey, never strict).
	if useAPIKey && key != "" {
		k := key
		u, err := s.Users.Find(ctx, store.UserFilter{APIKey: &k})
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		if u != nil && err == nil {
			if u.Banned {
				return nil, errf(http.StatusForbidden, MsgBanned)
			}
			// apikey strategy is non-strict, so the "verified" check does not
			// apply here; req.user is set and auth.jwt short-circuits
			// (auth.handler.js:18 `if (req.user) return next()`).
			return &Principal{User: u, Admin: u.IsAdmin(), Method: "apikey"}, nil
		}
	}
	// 2. JWT cookie.
	u, claims, err := s.userFromJWT(ctx, r)
	if err != nil {
		return nil, err
	}
	if u == nil {
		if strict {
			return nil, errf(http.StatusUnauthorized, MsgUnauthorized)
		}
		return nil, nil
	}
	if u.Banned {
		return nil, errf(http.StatusForbidden, MsgBanned)
	}
	if strict && !u.Verified {
		return nil, errf(http.StatusBadRequest, MsgUnverified)
	}
	return &Principal{User: u, Admin: u.IsAdmin(), Method: "jwt", Exp: claims.Exp}, nil
}

// Login implements the local strategy (passport.js:38-54): wrong email or
// password => 401 "Login credentials are wrong."; banned => 403; unverified => 400.
func (s *Service) Login(ctx context.Context, email, password string) (*domain.User, error) {
	e := email
	u, err := s.Users.Find(ctx, store.UserFilter{Email: &e})
	if errors.Is(err, domain.ErrNotFound) || (err == nil && u == nil) {
		return nil, errf(http.StatusUnauthorized, MsgLoginWrong)
	}
	if err != nil {
		return nil, err
	}
	if !ComparePassword(u.Password, password) {
		return nil, errf(http.StatusUnauthorized, MsgLoginWrong)
	}
	if u.Banned {
		return nil, errf(http.StatusForbidden, MsgBanned)
	}
	if !u.Verified {
		return nil, errf(http.StatusBadRequest, MsgUnverified)
	}
	return u, nil
}

// ---------- middleware ----------

// ErrorWriter renders an auth failure; the api package passes its JSON writer.
type ErrorWriter func(w http.ResponseWriter, r *http.Request, err error)

func (s *Service) mw(strict bool, admin bool, ew ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if FromContext(r.Context()) == nil {
				p, err := s.Authenticate(r.Context(), r, "", strict)
				if err != nil {
					ew(w, r, err)
					return
				}
				if p != nil {
					r = r.WithContext(WithPrincipal(r.Context(), p))
				}
			}
			if admin {
				p := FromContext(r.Context())
				if p == nil || !p.Admin {
					ew(w, r, errf(http.StatusUnauthorized, MsgAdminOnly))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireUser = auth.apikey + auth.jwt.
func (s *Service) RequireUser(ew ErrorWriter) func(http.Handler) http.Handler {
	return s.mw(true, false, ew)
}

// OptionalUser = auth.apikey + auth.jwtLoose.
func (s *Service) OptionalUser(ew ErrorWriter) func(http.Handler) http.Handler {
	return s.mw(false, false, ew)
}

// RequireAdmin = auth.apikey + auth.jwt + auth.admin.
func (s *Service) RequireAdmin(ew ErrorWriter) func(http.Handler) http.Handler {
	return s.mw(true, true, ew)
}
