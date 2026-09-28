// Package auth implements Kutt-compatible authentication: bcrypt (bcryptjs
// hashes verify, new hashes cost 12), HS256 JWTs in the "token" cookie
// (utils.js signToken/setToken, passport.js jwtFromRequest), X-API-KEY
// (passport-localapikey-update), OIDC and the request middleware.
package auth

import (
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// BcryptCost matches bcrypt.genSalt(12) in Kutt (auth.handler.js:98, link.queries.js:204).
const BcryptCost = 12

// bcryptjs silently truncates the UTF-8 input to 72 bytes; x/crypto refuses
// longer inputs. Truncating keeps both sides agreeing on long passwords.
func trunc72(pw string) []byte {
	b := []byte(pw)
	if len(b) > 72 {
		b = b[:72]
	}
	return b
}

// HashPassword returns a $2a$-compatible bcrypt hash at cost 12.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(trunc72(pw), BcryptCost)
	return string(h), err
}

// ComparePassword reports whether pw matches hash ($2a$/$2b$/$2y$). Never errors.
func ComparePassword(hash, pw string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), trunc72(pw)) == nil
}

// ---------- environment mode (envalid semantics) ----------

// Mode mirrors envalid's env.isDev / env.isProd which Kutt uses for cookie
// "secure" (utils.js:54) and the short-URL / site-URL scheme (utils.js:84,89).
// envalid: isProd = NODE_ENV is unset or "production"; isDev = NODE_ENV == "development".
// (Verified: cleanEnv({}) -> isProd true, isDev false.)
type Mode struct {
	Dev  bool
	Prod bool
}

// ModeFromEnv derives Mode from NODE_ENV (TUSK_ENV overrides when set).
func ModeFromEnv() Mode {
	v, ok := os.LookupEnv("TUSK_ENV")
	if !ok || v == "" {
		v, ok = os.LookupEnv("NODE_ENV")
	}
	if !ok || v == "" {
		return Mode{Prod: true}
	}
	return Mode{Dev: v == "development", Prod: v == "production"}
}

// SiteScheme is utils.getSiteURL's protocol: https unless dev.
func (m Mode) SiteScheme() string {
	if m.Dev {
		return "http://"
	}
	return "https://"
}

// ---------- JWT ----------

// Issuer is the fixed "iss" claim (utils.js:41).
const Issuer = "ApiAuth"

// TokenTTL is the JWT and cookie lifetime (utils.js:44, 52).
const TokenTTL = 7 * 24 * time.Hour

// kuttClaims serialises in jsonwebtoken's key order: iss, sub, iat, exp.
type kuttClaims struct {
	Iss string `json:"iss"`
	Sub int64  `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

func (c kuttClaims) GetExpirationTime() (*jwt.NumericDate, error) {
	return jwt.NewNumericDate(time.Unix(c.Exp, 0)), nil
}
func (c kuttClaims) GetIssuedAt() (*jwt.NumericDate, error) {
	return jwt.NewNumericDate(time.Unix(c.Iat, 0)), nil
}
func (c kuttClaims) GetNotBefore() (*jwt.NumericDate, error) { return nil, nil }
func (c kuttClaims) GetIssuer() (string, error)              { return c.Iss, nil }
func (c kuttClaims) GetSubject() (string, error)             { return strconv.FormatInt(c.Sub, 10), nil }
func (c kuttClaims) GetAudience() (jwt.ClaimStrings, error)  { return nil, nil }

// Tokens signs and verifies Kutt JWTs.
type Tokens struct {
	secret []byte
	now    func() time.Time
}

// NewTokens builds a signer/verifier for JWT_SECRET.
func NewTokens(secret string) *Tokens {
	return &Tokens{
		secret: []byte(secret),
		now:    time.Now,
	}
}

// SetClock overrides time (tests).
func (t *Tokens) SetClock(now func() time.Time) { t.now = now }

// Sign returns a token with {iss:"ApiAuth", sub:id, iat, exp:+7d} (utils.js signToken).
func (t *Tokens) Sign(userID int64) (string, error) {
	now := t.now()
	c := kuttClaims{Iss: Issuer, Sub: userID, Iat: now.Unix(), Exp: now.Add(TokenTTL).Unix()}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secret)
}

// ErrInvalidToken covers bad signature, expiry and the legacy string "sub".
var ErrInvalidToken = errors.New("invalid token")

// Claims is what Verify returns.
type Claims struct {
	UserID int64
	Exp    time.Time
}

// Verify validates signature/exp. Like passport.js:21 it rejects tokens whose
// "sub" is a string (old email-based tokens) or falsy.
func (t *Tokens) Verify(token string) (*Claims, error) {
	mc := jwt.MapClaims{}
	// jsonwebtoken.verify with a string secret accepts HS256/384/512 and
	// checks exp (no iss/aud/iat checks, no leeway).
	p := jwt.NewParser(jwt.WithValidMethods([]string{"HS256", "HS384", "HS512"}), jwt.WithTimeFunc(t.now))
	_, err := p.ParseWithClaims(token, mc, func(*jwt.Token) (any, error) { return t.secret, nil })
	if err != nil {
		return nil, ErrInvalidToken
	}
	sub, ok := mc["sub"].(float64)
	if !ok || sub == 0 || sub != float64(int64(sub)) {
		return nil, ErrInvalidToken
	}
	c := &Claims{UserID: int64(sub)}
	if exp, ok := mc["exp"].(float64); ok {
		c.Exp = time.Unix(int64(exp), 0)
	}
	return c, nil
}
