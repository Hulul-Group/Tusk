package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Hulul-Group/Tusk/internal/auth"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// auth.routes.js + auth.handler.js

func emailChain(b body, msg string, minLen int) *chain {
	return bodyField(b, "email", msg, optNone).
		exists("").trim().length(minLen, 255, "Email length must be max 255.").isEmail("")
}

func passwordChain(b body, key string) *chain {
	return bodyField(b, key, "Password is not valid.", optNone).
		exists("").length(8, 64, "Password length must be between 8 and 64.")
}

func (s *Server) issue(w http.ResponseWriter, u *domain.User) (string, error) {
	tok, err := s.o.Auth.Tokens.Sign(u.ID)
	if err != nil {
		return "", err
	}
	s.o.Auth.SetTokenCookie(w, tok)
	return tok, nil
}

// POST /auth/login
func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	if err := featureAccess(!s.cfg.DisallowLoginForm); err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if err := verify(passwordChain(b, "password"), emailChain(b, "Email is not valid.", 1)); err != nil {
		return err
	}
	if err := s.rateLimit(w, r, 60, 5); err != nil {
		return err
	}
	u, err := s.o.Auth.Login(r.Context(), b.str("email"), jsString(b.get("password")))
	if err != nil {
		return err
	}
	tok, err := s.issue(w, u)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, obj{{"token", tok}})
	return nil
}

// POST /auth/signup
func (s *Server) signup(w http.ResponseWriter, r *http.Request) error {
	if err := featureAccess(!s.cfg.DisallowRegistration, s.cfg.MailEnabled); err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if err := verify(passwordChain(b, "password"), emailChain(b, "Email is not valid.", 0)); err != nil {
		return err
	}
	if err := s.rateLimit(w, r, 60, 5); err != nil {
		return err
	}
	ctx := r.Context()
	email := b.str("email")
	existing, err := s.findUser(ctx, store.UserFilter{Email: &email})
	if err != nil {
		return err
	}
	if existing != nil && existing.Verified {
		return errBody(http.StatusBadRequest, "You can't use this email address.")
	}
	hash, err := auth.HashPassword(jsString(b.get("password")))
	if err != nil {
		return err
	}
	// query.user.add: new verification token valid for 60 minutes.
	token := uuid.NewString()
	exp := s.o.Now().Add(60 * time.Minute).UTC()
	var u *domain.User
	if existing != nil {
		id := existing.ID
		u, err = s.st.Users().Update(ctx, store.UserFilter{ID: &id}, store.UpdateUser{
			Email: &email, Password: &hash, VerificationToken: &token, VerificationExpires: &exp})
	} else {
		u, err = s.st.Users().Create(ctx, store.CreateUser{Email: email, Password: hash, Role: domain.RoleUser,
			VerificationToken: &token, VerificationExpires: &exp})
	}
	if err != nil {
		return err
	}
	if err := s.o.Mailer.SendVerification(ctx, u.Email, token); err != nil {
		s.log.Error("send verification", "err", err)
		return kuttErr("Couldn't send verification email. Try again later.")
	}
	writeJSON(w, http.StatusCreated, obj{{"message", "A verification email has been sent."}})
	return nil
}

// POST /auth/create-admin
func (s *Server) createAdmin(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if err := verify(passwordChain(b, "password"), emailChain(b, "Email is not valid.", 0)); err != nil {
		return err
	}
	if err := s.rateLimit(w, r, 60, 5); err != nil {
		return err
	}
	ctx := r.Context()
	any, err := s.st.Users().Any(ctx)
	if err != nil {
		return err
	}
	if any {
		return errBody(http.StatusBadRequest, "Can not create the admin user because a user already exists.")
	}
	hash, err := auth.HashPassword(jsString(b.get("password")))
	if err != nil {
		return err
	}
	u, err := s.st.Users().Create(ctx, store.CreateUser{Email: b.str("email"), Password: hash, Role: domain.RoleAdmin, Verified: true})
	if err != nil {
		return err
	}
	tok, err := s.issue(w, u) // Kutt sets the cookie only for HTML; the SPA needs it (CONTRACTS.md).
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, obj{{"token", tok}})
	return nil
}

// POST /auth/change-password (auth.jwt only, no API key)
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, false, true)
	if err != nil {
		return err
	}
	if err := verify(passwordChain(b, "currentpassword"), passwordChain(b, "newpassword")); err != nil {
		return err
	}
	if err := s.rateLimit(w, r, 60, 5); err != nil {
		return err
	}
	if !auth.ComparePassword(p.User.Password, jsString(b.get("currentpassword"))) {
		return errBody(http.StatusUnauthorized, "Current password is not correct.")
	}
	hash, err := auth.HashPassword(jsString(b.get("newpassword")))
	if err != nil {
		return err
	}
	id := p.User.ID
	if _, err := s.st.Users().Update(r.Context(), store.UserFilter{ID: &id}, store.UpdateUser{Password: &hash}); err != nil {
		return kuttErr("Couldn't change the password. Try again later.")
	}
	return message(w, http.StatusOK, "Your password has been changed successfully.")
}

// POST /auth/change-email (auth.jwt, MAIL_ENABLED)
func (s *Server) changeEmailRequest(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, false, true)
	if err != nil {
		return err
	}
	if err := featureAccess(s.cfg.MailEnabled); err != nil {
		return err
	}
	if err := verify(passwordChain(b, "password"), emailChain(b, "Email address is not valid.", 1)); err != nil {
		return err
	}
	if err := s.rateLimit(w, r, 60, 3); err != nil {
		return err
	}
	if !auth.ComparePassword(p.User.Password, jsString(b.get("password"))) {
		return errBody(http.StatusUnauthorized, "Password is not correct.")
	}
	ctx := r.Context()
	email := b.str("email")
	if u, err := s.findUser(ctx, store.UserFilter{Email: &email}); err != nil {
		return err
	} else if u != nil {
		return errBody(http.StatusBadRequest, "Can't use this email address.")
	}
	token := uuid.NewString()
	exp := s.o.Now().Add(30 * time.Minute).UTC()
	id := p.User.ID
	if _, err := s.st.Users().Update(ctx, store.UserFilter{ID: &id}, store.UpdateUser{
		ChangeEmailAddress: &email, ChangeEmailToken: &token, ChangeEmailExpires: &exp}); err != nil {
		return err
	}
	if err := s.o.Mailer.SendChangeEmail(ctx, email, token); err != nil {
		s.log.Error("send change-email", "err", err)
		return kuttErr("Couldn't send verification email. Try again later.")
	}
	return message(w, http.StatusOK, "A verification link has been sent to the requested email address.")
}

// POST /auth/apikey (auth.jwt only)
func (s *Server) generateAPIKey(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, false, true)
	if err != nil {
		return err
	}
	if err := s.rateLimit(w, r, 60, 10); err != nil {
		return err
	}
	key := nanoid(nanoidURLAlphabet, 40)
	id := p.User.ID
	if _, err := s.st.Users().Update(r.Context(), store.UserFilter{ID: &id}, store.UpdateUser{APIKey: &key}); err != nil {
		return kuttErr("Couldn't generate API key. Please try again later.")
	}
	writeJSON(w, http.StatusCreated, obj{{"apikey", key}})
	return nil
}

// POST /auth/reset-password (MAIL_ENABLED)
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) error {
	if err := featureAccess(s.cfg.MailEnabled); err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if err := verify(emailChain(b, "Email is not valid.", 0)); err != nil {
		return err
	}
	if err := s.rateLimit(w, r, 60, 3); err != nil {
		return err
	}
	email := b.str("email")
	token := uuid.NewString()
	exp := s.o.Now().Add(30 * time.Minute).UTC()
	u, err := s.st.Users().Update(r.Context(), store.UserFilter{Email: &email},
		store.UpdateUser{ResetPasswordToken: &token, ResetPasswordExpires: &exp})
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if err == nil && u != nil {
		// Fire-and-forget like Kutt (errors are only logged).
		go func(to string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.o.Mailer.SendResetPassword(ctx, to, token); err != nil {
				s.log.Error("Send reset-password token email error", "err", err)
			}
		}(u.Email)
	}
	return message(w, http.StatusOK, "If email address exists, a reset password email has been sent.")
}

// POST /auth/new-password
func (s *Server) newPassword(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	repeat := valueField("repeat_password", b.get("repeat_password"), "Password is not valid.", optNone).
		check(func(v any) bool { return jsEqual(v, b.get("new_password")) }, "Passwords don't match.")
	if err := verify(
		bodyField(b, "reset_password_token", "Reset password token is invalid.", optNone).exists("").length(36, 36, ""),
		passwordChain(b, "new_password"),
		repeat,
	); err != nil {
		return err
	}
	if err := s.rateLimit(w, r, 60, 5); err != nil {
		return err
	}
	hash, err := auth.HashPassword(jsString(b.get("new_password")))
	if err != nil {
		return err
	}
	tok := jsString(b.get("reset_password_token"))
	now := s.o.Now().UTC()
	_, err = s.st.Users().Update(r.Context(), store.UserFilter{ResetPasswordToken: &tok, TokenNotExpired: &now},
		store.UpdateUser{Password: &hash, ClearResetPassword: true})
	if err != nil {
		return kuttErr("Could not set the password. Please try again later.")
	}
	// Kutt renders new_password_success; JSON form documented in API-EXTENSIONS.md.
	return message(w, http.StatusOK, "Your password has been changed successfully.")
}

// POST /auth/logout (Tusk extension)
func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	s.o.Auth.ClearTokenCookie(w)
	return message(w, http.StatusOK, "Logged out.")
}

// POST /auth/verify/{token} (Tusk extension; Kutt: GET /verify/:token render + auth.verify)
func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) error {
	tok := r.PathValue("token")
	now := s.o.Now().UTC()
	t := true
	u, err := s.st.Users().Update(r.Context(), store.UserFilter{VerificationToken: &tok, TokenNotExpired: &now},
		store.UpdateUser{Verified: &t, ClearVerification: true})
	if err != nil || u == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		return errBody(http.StatusBadRequest, "Invalid or expired verification link.")
	}
	jwt, err := s.issue(w, u)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, obj{{"token", jwt}, {"message", "Your account has been verified successfully."}})
	return nil
}

// POST /auth/verify-email/{token} (Tusk extension; Kutt: GET /verify-email/:token + auth.changeEmail)
func (s *Server) verifyChangeEmail(w http.ResponseWriter, r *http.Request) error {
	tok := r.PathValue("token")
	now := s.o.Now().UTC()
	ctx := r.Context()
	found, err := s.findUser(ctx, store.UserFilter{ChangeEmailToken: &tok, TokenNotExpired: &now})
	if err != nil {
		return err
	}
	if found == nil || found.ChangeEmailAddress == nil {
		return errBody(http.StatusBadRequest, "Invalid or expired verification link.")
	}
	id := found.ID
	u, err := s.st.Users().Update(ctx, store.UserFilter{ID: &id},
		store.UpdateUser{Email: found.ChangeEmailAddress, ClearChangeEmail: true})
	if err != nil {
		return err
	}
	jwt, err := s.issue(w, u)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, obj{{"token", jwt}, {"message", "Your email address has been changed successfully."}})
	return nil
}

// GET /login/oidc (renders.routes.js:28-35): begin or complete the OIDC flow.
func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) error {
	if !s.cfg.OIDCEnabled || s.o.OIDC == nil {
		return featureAccess(false)
	}
	// jwtLoosePage: an already logged-in user just gets a fresh token.
	if p, _ := s.authn(r, body{}, false, false); p != nil {
		if _, err := s.issue(w, p.User); err != nil {
			return err
		}
		http.Redirect(w, r, "/", http.StatusFound)
		return nil
	}
	if r.URL.Query().Get("code") == "" && r.URL.Query().Get("error") == "" {
		if err := s.o.OIDC.Begin(w, r); err != nil {
			s.log.Error("oidc begin", "err", err)
			return errBody(http.StatusUnauthorized, auth.MsgOIDCFailed)
		}
		return nil
	}
	u, err := s.o.OIDC.Callback(w, r)
	if err != nil {
		return err
	}
	if u.Banned {
		return errBody(http.StatusForbidden, auth.MsgBanned)
	}
	if _, err := s.issue(w, u); err != nil {
		return err
	}
	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

// GET /config (Tusk extension): locals.config + needs_admin_setup.
func (s *Server) publicConfig(w http.ResponseWriter, r *http.Request) error {
	any, err := s.st.Users().Any(r.Context())
	if err != nil {
		return err
	}
	c := s.cfg
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, obj{
		{"site_name", c.SiteName},
		{"default_domain", c.DefaultDomain},
		{"link_length", c.LinkLength},
		{"disallow_registration", c.DisallowRegistration || !c.MailEnabled},
		{"disallow_anonymous_links", c.DisallowAnonymousLinks},
		{"disallow_login_form", c.DisallowLoginForm},
		{"oidc_enabled", c.OIDCEnabled},
		{"oidc_button_text", c.OIDCButtonText},
		{"mail_enabled", c.MailEnabled},
		{"report_email", c.ReportEmail},
		{"contact_email", c.ContactEmail},
		{"server_ip_address", c.ServerIPAddress},
		{"server_cname_address", c.ServerCNAMEAddress},
		{"custom_domain_use_https", c.CustomDomainUseHTTPS},
		{"needs_admin_setup", !any},
	})
	return nil
}

func (s *Server) findUser(ctx context.Context, f store.UserFilter) (*domain.User, error) {
	u, err := s.st.Users().Find(ctx, f)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return u, err
}

// jsEqual is === for JSON values.
func jsEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false // objects/arrays compare by reference
}
