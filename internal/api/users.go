package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Hulul-Group/Tusk/internal/auth"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// user.routes.js + users.handler.js, domain.routes.js + domains.handler.js

// GET /users
func (s *Server) getUser(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	uid := p.User.ID
	doms, err := s.st.Domains().List(r.Context(), store.DomainFilter{UserID: &uid})
	if err != nil {
		return err
	}
	data := make([]obj, 0, len(doms))
	for i := range doms {
		data = append(data, s.domainJSON(&doms[i]))
	}
	// Kutt: {apikey, email, domains}; id/role/verified are Tusk extensions.
	writeJSON(w, http.StatusOK, obj{
		{"apikey", strOrNil(p.User.APIKey)},
		{"email", p.User.Email},
		{"domains", data},
		{"id", p.User.ID},
		{"role", string(p.User.Role)},
		{"verified", p.User.Verified},
	})
	return nil
}

// GET /users/admin
func (s *Server) getUsersAdmin(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	q, ok := parseQuery(w, r)
	if !ok {
		return nil
	}
	qs := r.URL.Query()
	f := store.AdminUserFilter{Search: qs.Get("search"),
		Verified: parseBooleanQuery(qs.Get("verified")), Banned: parseBooleanQuery(qs.Get("banned"))}
	if role := qs.Get("role"); role != "" {
		rl := domain.Role(role)
		f.Role = &rl
	}
	// NOTE: Kutt also filters by ?domains= / ?links= (is [not] null); the
	// shared store contract has no such fields (requested in CONTRACTS.md).
	ctx := r.Context()
	users, err := s.st.Users().ListAdmin(ctx, f, store.Page{Skip: q.skip, Limit: q.limit})
	if err != nil {
		return err
	}
	total, err := s.st.Users().TotalAdmin(ctx, f)
	if err != nil {
		return err
	}
	now := s.o.Now()
	data := make([]obj, 0, len(users))
	for i := range users {
		data = append(data, s.userAdminJSON(&users[i], now))
	}
	writeJSON(w, http.StatusOK, page(total, q, data))
	return nil
}

// POST /users/admin
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	ctx := r.Context()
	boolC := func(k string) *chain {
		return bodyField(b, k, "", optNullable).sanitize(sanitizeCheckbox).isBoolean("")
	}
	email := emailChain(b, "Email is not valid.", 1)
	email.check(func(v any) bool {
		e := jsString(v)
		u, err := s.findUser(ctx, store.UserFilter{Email: &e})
		return err == nil && u == nil
	}, "User already exists.")
	if err := verify(
		passwordChain(b, "password"),
		email,
		bodyField(b, "role", "Role is not valid.", optFalsy).trim().
			str(func(v string) bool { return v == string(domain.RoleUser) || v == string(domain.RoleAdmin) }, ""),
		boolC("verified"), boolC("banned"), boolC("verification_email"),
	); err != nil {
		return err
	}
	hash, err := auth.HashPassword(jsString(b.get("password")))
	if err != nil {
		return err
	}
	role := domain.RoleUser
	if b.str("role") == string(domain.RoleAdmin) {
		role = domain.RoleAdmin
	}
	in := store.CreateUser{Email: b.str("email"), Password: hash, Role: role, Verified: boolish(b.get("verified"))}
	u, err := s.st.Users().Create(ctx, in)
	if err != nil {
		return err
	}
	if boolish(b.get("banned")) {
		t := true
		by := p.User.ID
		id := u.ID
		if u, err = s.st.Users().Update(ctx, store.UserFilter{ID: &id}, store.UpdateUser{Banned: &t, BannedByID: &by}); err != nil {
			return err
		}
	}
	// Kutt: `if (verification_email && !user.banned && !user.verified) mail.verification(user)`
	// — user.verification_token is undefined there (create doesn't set one), so
	// Kutt mails a broken link. Tusk issues a real token.
	if boolish(b.get("verification_email")) && !u.Banned && !u.Verified && s.o.Mailer.Enabled() {
		if err := s.sendNewVerification(r, u); err != nil {
			return err
		}
	}
	return message(w, http.StatusCreated, "The user has been created successfully.")
}

func (s *Server) sendNewVerification(r *http.Request, u *domain.User) error {
	token := newUUID()
	exp := s.o.Now().Add(60 * 60 * 1e9).UTC()
	id := u.ID
	if _, err := s.st.Users().Update(r.Context(), store.UserFilter{ID: &id},
		store.UpdateUser{VerificationToken: &token, VerificationExpires: &exp}); err != nil {
		return err
	}
	if err := s.o.Mailer.SendVerification(r.Context(), u.Email, token); err != nil {
		s.log.Error("send verification", "err", err)
		return kuttErr("Couldn't send verification email. Try again later.")
	}
	return nil
}

// POST /users/delete
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if err := verify(bodyField(b, "password", "Password is not valid.", optNone).exists("").length(8, 64, "").
		check(func(v any) bool { return auth.ComparePassword(p.User.Password, jsString(v)) }, "Password is not correct.")); err != nil {
		return err
	}
	if err := s.removeUserAndInvalidate(r, p.User.ID); err != nil {
		return err
	}
	s.o.Auth.ClearTokenCookie(w)
	// Kutt API answers res.send("OK") (text); the SPA contract asks for {message}.
	return message(w, http.StatusOK, "Account has been deleted.")
}

// removeUserAndInvalidate deletes a user after collecting their links and
// domains so the redirect caches drop them (links cascade in the DB).
func (s *Server) removeUserAndInvalidate(r *http.Request, id int64) error {
	ctx := r.Context()
	uid := id
	var keys []store.LinkKey
	for skip := 0; ; skip += 1000 {
		links, err := s.st.Links().List(ctx, store.LinkFilter{UserID: &uid}, "", store.Page{Skip: skip, Limit: 1000})
		if err != nil {
			return err
		}
		for _, l := range links {
			keys = append(keys, store.LinkKey{Address: l.Address, DomainID: l.DomainID})
		}
		if len(links) < 1000 {
			break
		}
	}
	doms, err := s.st.Domains().List(ctx, store.DomainFilter{UserID: &uid})
	if err != nil {
		return err
	}
	if err := s.st.Users().Remove(ctx, id); err != nil {
		return err
	}
	for _, k := range keys {
		s.invalidateLink(ctx, k.Address, k.DomainID)
	}
	for _, d := range doms {
		s.invalidateDomain(ctx, d.Address)
	}
	return nil
}

// DELETE /users/admin/{id}
func (s *Server) deleteUserAdmin(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	if err := verify(numericParam(r)); err != nil {
		return err
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u, err := s.findUser(r.Context(), store.UserFilter{ID: &id})
	if err != nil {
		return err
	}
	if u == nil {
		// Kutt: res.status(400).send({ message }) (not {error}).
		writeJSON(w, http.StatusBadRequest, obj{{"message", "Could not find the user."}})
		return nil
	}
	if err := s.removeUserAndInvalidate(r, id); err != nil {
		return err
	}
	return message(w, http.StatusOK, "User has been deleted successfully.")
}

// POST /users/admin/ban/{id}
func (s *Server) banUser(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	boolChain := func(k string) *chain {
		return bodyField(b, k, `"`+k+`" should be a boolean.`, optNullable).sanitize(sanitizeCheckbox).isBoolean("")
	}
	if err := verify(numericParam(r), boolChain("links"), boolChain("domains")); err != nil {
		return err
	}
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u, err := s.findUser(ctx, store.UserFilter{ID: &id})
	if err != nil {
		return err
	}
	if u == nil {
		return errBody(http.StatusBadRequest, "No user has been found.")
	}
	if u.Banned {
		return errBody(http.StatusBadRequest, "User has been banned already.")
	}
	t := true
	by := p.User.ID
	fail := func(err error) error {
		s.log.Error("ban user", "err", err)
		return kuttErr("Couldn't ban entries.")
	}
	if _, err := s.st.Users().Update(ctx, store.UserFilter{ID: &id}, store.UpdateUser{Banned: &t, BannedByID: &by}); err != nil {
		return fail(err)
	}
	if boolish(b.get("links")) {
		updated, err := s.st.Links().Update(ctx, store.LinkFilter{UserID: &id}, store.UpdateLink{Banned: &t, BannedByID: &by})
		if err != nil {
			return fail(err)
		}
		for _, l := range updated {
			s.invalidateLink(ctx, l.Address, l.DomainID)
		}
	}
	if boolish(b.get("domains")) {
		updated, err := s.st.Domains().Update(ctx, store.DomainFilter{UserID: &id}, store.UpdateDomain{Banned: &t, BannedByID: &by})
		if err != nil {
			return fail(err)
		}
		for _, d := range updated {
			s.invalidateDomain(ctx, d.Address)
		}
	}
	return message(w, http.StatusOK, "Banned user successfully.")
}

// ---------- domains ----------

func (s *Server) domainAddressChain(b body, defaultMsg string, check func(string) (bool, error), checkMsg string) *chain {
	c := bodyField(b, "address", "Domain is not valid.", optNone).exists("").
		length(3, 64, "Domain length must be between 3 and 64.").trim().
		mapStr(addProtocol).str(func(v string) bool { return matchJS(urlRegex, v) }, "").
		mapStr(func(v string) string {
			u := parseLegacyURL(v)
			if u.Hostname != "" {
				return removeWww(u.Hostname)
			}
			return removeWww(v)
		}).
		str(func(v string) bool { return v != s.cfg.DefaultDomain }, defaultMsg)
	c.str(func(v string) bool { ok, err := check(v); return err == nil && ok }, checkMsg)
	return c
}

func homepageChain(b body) *chain {
	return bodyField(b, "homepage", "", optFalsy).mapStr(addProtocol).str(validTarget, "Homepage is not valid.")
}

// POST /domains
func (s *Server) addDomain(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := verify(
		s.domainAddressChain(b, "You can't use the default domain.", func(a string) (bool, error) {
			d, err := s.st.Domains().Find(ctx, store.DomainFilter{Address: &a})
			if errors.Is(err, domain.ErrNotFound) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return d.UserID == nil && !d.Banned, nil
		}, "You can't add this domain."),
		homepageChain(b),
	); err != nil {
		return err
	}
	uid := p.User.ID
	in := store.UpsertDomain{Address: strings.ToLower(b.str("address")), UserID: &uid}
	if hp, ok := b.get("homepage").(string); ok && hp != "" {
		in.Homepage = &hp
	}
	d, err := s.st.Domains().Add(ctx, in)
	if err != nil {
		return err
	}
	s.invalidateDomain(ctx, d.Address)
	writeJSON(w, http.StatusOK, s.domainJSON(d))
	return nil
}

// POST /domains/admin
func (s *Server) addDomainAdmin(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	ctx := r.Context()
	if err := verify(
		s.domainAddressChain(b, "You can't add the default domain.", func(a string) (bool, error) {
			_, err := s.st.Domains().Find(ctx, store.DomainFilter{Address: &a})
			if errors.Is(err, domain.ErrNotFound) {
				return true, nil
			}
			return false, err
		}, "Domain already exists."),
		homepageChain(b),
		bodyField(b, "banned", "", optNullable).sanitize(sanitizeCheckbox).isBoolean(""),
	); err != nil {
		return err
	}
	in := store.UpsertDomain{Address: strings.ToLower(b.str("address"))}
	if hp, ok := b.get("homepage").(string); ok && hp != "" {
		in.Homepage = &hp
	}
	d, err := s.st.Domains().Add(ctx, in)
	if err != nil {
		return err
	}
	if boolish(b.get("banned")) {
		t := true
		by := p.User.ID
		if _, err := s.st.Domains().Update(ctx, store.DomainFilter{ID: &d.ID}, store.UpdateDomain{Banned: &t, BannedByID: &by}); err != nil {
			return err
		}
	}
	s.invalidateDomain(ctx, d.Address)
	return message(w, http.StatusOK, "The domain has been added successfully.")
}

// DELETE /domains/{id}: the user releases the domain (user_id = NULL).
func (s *Server) removeDomain(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	if err := verify(uuidParam(r)); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("id")
	uid := p.User.ID
	d, err := s.st.Domains().Find(ctx, store.DomainFilter{UUID: &id, UserID: &uid})
	if err != nil || d == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		return errBody(http.StatusBadRequest, "Could not delete the domain.")
	}
	updated, err := s.st.Domains().Update(ctx, store.DomainFilter{ID: &d.ID}, store.UpdateDomain{ClearUserID: true})
	if err != nil || len(updated) == 0 {
		return errBody(http.StatusInternalServerError, "Could not delete the domain.")
	}
	s.invalidateDomain(ctx, d.Address)
	return message(w, http.StatusOK, "Domain deleted successfully")
}

// DELETE /domains/admin/{id}?links=true
func (s *Server) removeDomainAdmin(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	links := r.URL.Query().Get("links")
	var lv any
	if r.URL.Query().Has("links") {
		lv = links
	}
	lc := valueField("links", lv, "", optNullable).sanitize(sanitizeCheckbox).isBoolean("")
	if err := verify(numericParam(r), lc); err != nil {
		return err
	}
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := s.st.Domains().Find(ctx, store.DomainFilter{ID: &id})
	if err != nil || d == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		return errBody(http.StatusBadRequest, "Could not find the domain.")
	}
	// Kutt: `if (links)` on the raw query string -> any non-empty value
	// (even "false") deletes the links. Tusk honours the boolean.
	if boolish(lc.v) {
		for {
			ls, err := s.st.Links().List(ctx, store.LinkFilter{DomainID: &id}, "", store.Page{Limit: 1000})
			if err != nil {
				return err
			}
			if len(ls) == 0 {
				break
			}
			for _, l := range ls {
				lid := l.ID
				if _, err := s.st.Links().Remove(ctx, store.LinkFilter{ID: &lid}); err != nil {
					return err
				}
				s.invalidateLink(ctx, l.Address, l.DomainID)
			}
		}
	}
	if err := s.st.Domains().Remove(ctx, id); err != nil {
		return err
	}
	s.invalidateDomain(ctx, d.Address)
	return message(w, http.StatusOK, "Domain deleted successfully")
}

// GET /domains/admin
func (s *Server) getDomainsAdmin(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	q, ok := parseQuery(w, r)
	if !ok {
		return nil
	}
	qs := r.URL.Query()
	f := store.AdminDomainFilter{Search: qs.Get("search"), User: qs.Get("user"),
		Banned: parseBooleanQuery(qs.Get("banned")), Owner: parseBooleanQuery(qs.Get("owner"))}
	ctx := r.Context()
	doms, err := s.st.Domains().ListAdmin(ctx, f, store.Page{Skip: q.skip, Limit: q.limit})
	if err != nil {
		return err
	}
	total, err := s.st.Domains().TotalAdmin(ctx, f)
	if err != nil {
		return err
	}
	now := s.o.Now()
	data := make([]obj, 0, len(doms))
	for i := range doms {
		data = append(data, s.domainAdminJSON(&doms[i], now))
	}
	writeJSON(w, http.StatusOK, page(total, q, data))
	return nil
}

// POST /domains/admin/ban/{id}
func (s *Server) banDomain(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	boolChain := func(k string) *chain {
		return bodyField(b, k, `"`+k+`" should be a boolean.`, optNullable).sanitize(sanitizeCheckbox).isBoolean("")
	}
	if err := verify(numericParam(r), boolChain("links"), boolChain("domains")); err != nil {
		return err
	}
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := s.st.Domains().Find(ctx, store.DomainFilter{ID: &id})
	if err != nil || d == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		return errBody(http.StatusBadRequest, "No domain has been found.")
	}
	if d.Banned {
		return errBody(http.StatusBadRequest, "Domain has been banned already.")
	}
	t := true
	by := p.User.ID
	fail := func(err error) error {
		s.log.Error("ban domain", "err", err)
		return kuttErr("Couldn't ban entries.")
	}
	if _, err := s.st.Domains().Update(ctx, store.DomainFilter{ID: &id}, store.UpdateDomain{Banned: &t, BannedByID: &by}); err != nil {
		return fail(err)
	}
	s.invalidateDomain(ctx, d.Address)
	// Kutt reads req.body.user, which the validator never declares.
	if truthy(b.get("user")) && d.UserID != nil {
		if _, err := s.st.Users().Update(ctx, store.UserFilter{ID: d.UserID}, store.UpdateUser{Banned: &t, BannedByID: &by}); err != nil {
			return fail(err)
		}
	}
	if boolish(b.get("links")) {
		updated, err := s.st.Links().Update(ctx, store.LinkFilter{DomainID: &id}, store.UpdateLink{Banned: &t, BannedByID: &by})
		if err != nil {
			return fail(err)
		}
		for _, l := range updated {
			s.invalidateLink(ctx, l.Address, l.DomainID)
		}
	}
	return message(w, http.StatusOK, "Banned domain successfully.")
}
