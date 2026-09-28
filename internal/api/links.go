package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Hulul-Group/Tusk/internal/auth"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/redirect"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// link.routes.js + links.handler.js

func (s *Server) invalidateLink(ctx context.Context, address string, domainID *int64) {
	if s.o.Links == nil {
		return
	}
	if err := s.o.Links.Invalidate(ctx, address, domainID); err != nil {
		s.log.Warn("link cache invalidate", "address", address, "err", err)
	}
}

func (s *Server) invalidateDomain(ctx context.Context, address string) {
	if inv, ok := s.o.Domains.(DomainInvalidator); ok && inv != nil {
		if err := inv.Invalidate(ctx, address); err != nil {
			s.log.Warn("domain cache invalidate", "address", address, "err", err)
		}
	}
}

func uuidParam(r *http.Request) *chain {
	return valueField("id", r.PathValue("id"), "ID is invalid.", optNone).exists("").length(36, 36, "")
}

func numericParam(r *http.Request) *chain {
	return valueField("id", r.PathValue("id"), "ID is invalid.", optNone).exists("").isNumeric("")
}

// GET /links
func (s *Server) getLinks(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	q, ok := parseQuery(w, r)
	if !ok {
		return nil
	}
	uid := p.User.ID
	f := store.LinkFilter{UserID: &uid}
	search := r.URL.Query().Get("search")
	ctx := r.Context()
	links, err := s.st.Links().List(ctx, f, search, store.Page{Skip: q.skip, Limit: q.limit})
	if err != nil {
		return err
	}
	total, err := s.st.Links().Total(ctx, f, search)
	if err != nil {
		return err
	}
	data := make([]obj, 0, len(links))
	for i := range links {
		data = append(data, s.linkJSON(&links[i], true))
	}
	writeJSON(w, http.StatusOK, page(total, q, data))
	return nil
}

// GET /links/admin
func (s *Server) getLinksAdmin(w http.ResponseWriter, r *http.Request) error {
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
	f := store.AdminLinkFilter{
		Search: qs.Get("search"), User: qs.Get("user"), Domain: qs.Get("domain"),
		Anonymous: parseBooleanQuery(qs.Get("anonymous")),
		HasDomain: parseBooleanQuery(qs.Get("has_domain")),
	}
	f.Banned = parseBooleanQuery(qs.Get("banned"))
	if f.Domain == s.cfg.DefaultDomain {
		f.Domain = ""
		f.DefaultDomain = true
		f.HasDomain = nil
	}
	ctx := r.Context()
	links, err := s.st.Links().ListAdmin(ctx, f, store.Page{Skip: q.skip, Limit: q.limit})
	if err != nil {
		return err
	}
	total, err := s.st.Links().TotalAdmin(ctx, f)
	if err != nil {
		return err
	}
	now := s.o.Now()
	data := make([]obj, 0, len(links))
	for i := range links {
		data = append(data, s.linkAdminJSON(&links[i], now))
	}
	writeJSON(w, http.StatusOK, page(total, q, data))
	return nil
}

func (s *Server) targetChain(b body, optional bool) *chain {
	opt := optNone
	if optional {
		opt = optFalsy
	}
	c := bodyField(b, "target", "", opt)
	if !optional {
		c.exists("Target is missing.")
	}
	return c.isString("").trim().length(1, 2040, "Maximum URL length is 2040.").
		mapStr(addProtocol).
		str(validTarget, "URL is not valid.").
		str(func(v string) bool { return removeWww(parseLegacyURL(v).Host) != s.cfg.DefaultDomain },
			s.cfg.DefaultDomain+" URLs are not allowed.")
}

func (s *Server) expireChain(b body, now time.Time) *chain {
	c := bodyField(b, "expire_in", "", optFalsy).isString("").trim().
		str(func(v string) bool { n, ok := parseMS(v); return ok && n != 0 && !math.IsNaN(n) },
			"Expire format is invalid. Valid examples: 1m, 8h, 42 days.")
	c.sanitize(func(v any) any { n, _ := parseMS(jsString(v)); return n })
	c.check(func(v any) bool { n, _ := v.(float64); return n >= msM }, "Expire time should be more than 1 minute.")
	// Kutt stores dateToUTC(new Date() + ms) with second precision.
	c.sanitize(func(v any) any {
		n, _ := v.(float64)
		t := now.Add(time.Duration(n) * time.Millisecond).UTC().Truncate(time.Second)
		return t
	})
	return c
}

func (s *Server) customAddrValid(v string) bool {
	return customAddressRe.MatchString(v) || s.alphabetRe.MatchString(v)
}

func notPreserved(v string) bool {
	for _, p := range redirect.PreservedURLs {
		if strings.ToLower(p) == v {
			return false
		}
	}
	return true
}

// POST /links (cors enabled, server.js / link.routes.js:31)
func (s *Server) createLink(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, s.cfg.DisallowAnonymousLinks)
	if err != nil {
		return err
	}
	ctx := r.Context()
	isUser := func(any) bool { return p != nil }
	const onlyUsers = "Only users can use this field."
	var fetched *domain.Domain
	now := s.o.Now()

	domainChain := bodyField(b, "domain", "", optFalsy).
		sanitize(func(v any) any {
			if s2, ok := v.(string); ok && s2 == s.cfg.DefaultDomain {
				return nil
			}
			return v
		}).
		check(isUser, onlyUsers).
		isString("Domain should be string.").
		mapStr(strings.ToLower)
	domainChain.check(func(v any) bool {
		addr := jsString(v)
		uid := p.User.ID
		d, err := s.st.Domains().Find(ctx, store.DomainFilter{Address: &addr, UserID: &uid})
		if err != nil || d == nil {
			return false
		}
		fetched = d
		return true
	}, "You can't use this domain.")

	if err := verify(
		s.targetChain(b, false),
		bodyField(b, "password", "", optFalsy).check(isUser, onlyUsers).isString("").
			length(3, 64, "Password length must be between 3 and 64."),
		bodyField(b, "customurl", "", optFalsy).check(isUser, onlyUsers).isString("").trim().
			length(1, 64, "Custom URL length must be between 1 and 64.").
			str(s.customAddrValid, "Custom URL is not valid.").
			str(notPreserved, "You can't use this custom URL."),
		bodyField(b, "reuse", "", optNullable).check(isUser, onlyUsers).isBoolean("Reuse must be boolean."),
		bodyField(b, "description", "", optFalsy).isString("").trim().
			length(1, 2040, "Description length must be between 1 and 2040."),
		s.expireChain(b, now),
		domainChain,
	); err != nil {
		return err
	}

	target := b.str("target")
	var domainID *int64
	if fetched != nil {
		domainID = &fetched.ID
	}
	// reuse: truthy value (Kutt: `reuse && query.link.find(...)`)
	// Kutt tests `reuse &&` so the string "false" would count as true; Tusk uses the boolean value.
	if p != nil && boolish(b.get("reuse")) {
		uid := p.User.ID
		f := store.LinkFilter{UserID: &uid, DomainID: domainID, DefaultDomain: domainID == nil}
		if l, err := s.findLinkByTarget(ctx, f, target); err != nil {
			return err
		} else if l != nil {
			writeJSON(w, http.StatusOK, s.linkJSON(l, true))
			return nil
		}
	}
	customurl, _ := b.get("customurl").(string)
	if !truthy(b.get("customurl")) {
		customurl = ""
	}
	targetHost := removeWww(parseLegacyURL(target).Hostname)
	if err := s.bannedDomain(ctx, targetHost); err != nil {
		return err
	}
	if customurl != "" {
		taken, err := s.st.Links().AddressTaken(ctx, customurl, domainID)
		if err != nil {
			return err
		}
		if taken {
			return kuttErr("Custom URL is already in use.")
		}
	}
	if err := s.bannedHost(ctx, targetHost); err != nil {
		return err
	}
	address := customurl
	if address == "" {
		if address, err = s.generateID(ctx, domainID); err != nil {
			return err
		}
	}
	in := store.CreateLink{Address: address, Target: target, DomainID: domainID}
	if pw, ok := b.get("password").(string); ok && pw != "" {
		in.Password = &pw
	}
	if d, ok := b.get("description").(string); ok && d != "" {
		in.Description = &d
	}
	if t, ok := b.get("expire_in").(time.Time); ok {
		in.ExpireIn = &t
	}
	if p != nil {
		uid := p.User.ID
		in.UserID = &uid
	}
	l, err := s.st.Links().Create(ctx, in)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return kuttErr("Custom URL is already in use.")
		}
		return err
	}
	if fetched != nil {
		a := fetched.Address
		l.DomainAddress = &a
	} else {
		l.DomainAddress = nil
	}
	// A negative-cache entry may exist for this address.
	s.invalidateLink(ctx, l.Address, domainID)
	writeJSON(w, http.StatusCreated, s.linkCreatedJSON(l))
	return nil
}

func (s *Server) findLinkByTarget(ctx context.Context, f store.LinkFilter, target string) (*domain.Link, error) {
	// The store has no target filter; Kutt's reuse query is
	// {target, user_id, domain_id}. Page through the user's links using the
	// search index narrowed by target (ILIKE %target% is a superset).
	links, err := s.st.Links().List(ctx, f, target, store.Page{Limit: 50})
	if err != nil {
		return nil, err
	}
	for i := range links {
		if links[i].Target == target {
			return &links[i], nil
		}
	}
	return nil, nil
}

func (s *Server) generateID(ctx context.Context, domainID *int64) (string, error) {
	for i := 0; i < 50; i++ {
		a := nanoid(s.cfg.LinkAlphabet, s.cfg.LinkLength)
		taken, err := s.st.Links().AddressTaken(ctx, a, domainID)
		if err != nil {
			return "", err
		}
		if !taken {
			return a, nil
		}
	}
	return "", kuttErr("Couldn't generate a unique short URL. Try again later.")
}

// bannedDomain: validators.handler.js:504-513.
func (s *Server) bannedDomain(ctx context.Context, host string) error {
	if host == "" {
		return nil
	}
	t := true
	d, err := s.st.Domains().Find(ctx, store.DomainFilter{Address: &host, Banned: &t})
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if d != nil && err == nil {
		return errBody(http.StatusBadRequest, "Domain is banned.")
	}
	return nil
}

// bannedHost: validators.handler.js:515-534 (dns.lookup, errors ignored).
func (s *Server) bannedHost(ctx context.Context, host string) error {
	if host == "" {
		return nil
	}
	ip, err := s.o.LookupIP(ctx, host)
	if err != nil || ip == "" {
		return nil
	}
	h, err := s.st.Hosts().Find(ctx, ip)
	if err == nil && h != nil && h.Banned {
		return errBody(http.StatusBadRequest, "URL is containing malware/scam.")
	}
	return nil
}

// PATCH /links/{id} and /links/admin/{id}
func (s *Server) editLink(w http.ResponseWriter, r *http.Request) error { return s.edit(w, r, false) }
func (s *Server) editLinkAdmin(w http.ResponseWriter, r *http.Request) error {
	return s.edit(w, r, true)
}

func (s *Server) edit(w http.ResponseWriter, r *http.Request, adminRoute bool) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if adminRoute {
		if err := s.admin(p); err != nil {
			return err
		}
	}
	now := s.o.Now()
	if err := verify(
		s.targetChain(b, true),
		bodyField(b, "password", "", optFalsy).isString("").length(3, 64, "Password length must be between 3 and 64."),
		bodyField(b, "address", "", optFalsy).isString("").trim().
			length(1, 64, "Custom URL length must be between 1 and 64.").
			str(s.customAddrValid, "Custom URL is not valid").
			str(notPreserved, "You can't use this custom URL."),
		s.expireChain(b, now),
		bodyField(b, "description", "", optFalsy).isString("").trim().
			length(0, 2040, "Description length must be between 0 and 2040."),
		uuidParam(r),
	); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("id")
	f := store.LinkFilter{UUID: &id}
	if !p.Admin {
		uid := p.User.ID
		f.UserID = &uid
	}
	link, err := s.st.Links().Find(ctx, f)
	if err != nil || link == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		return kuttErr("Link was not found.")
	}

	var up store.UpdateLink
	changed := false
	// address
	if v, ok := b.get("address").(string); ok && v != "" && v != link.Address {
		up.Address, changed = &v, true
	}
	if v, ok := b.get("target").(string); ok && v != "" && v != link.Target {
		up.Target, changed = &v, true
	}
	if v, ok := b.get("description").(string); ok && v != "" && (link.Description == nil || v != *link.Description) {
		up.Description, changed = &v, true
	}
	if t, ok := b.get("expire_in").(time.Time); ok {
		// Kutt: isChanged only when differing by >= 60 s, but the value is still written.
		up.ExpireIn = &t
		if link.ExpireIn == nil || math.Abs(t.Sub(*link.ExpireIn).Seconds()) >= 60 {
			changed = true
		}
	}
	pwv := b.get("password")
	hasPw := link.Password != nil && *link.Password != ""
	if !truthy(pwv) {
		if hasPw {
			up.ClearPassword, changed = true, true
		}
	} else if pw, ok := pwv.(string); ok {
		if strings.ReplaceAll(pw, "•", "") != "" {
			up.Password, changed = &pw, true
		}
	}
	if !changed {
		return kuttErr("Should at least update one field.")
	}
	if up.Address != nil {
		taken, err := s.st.Links().AddressTaken(ctx, *up.Address, link.DomainID)
		if err != nil {
			return err
		}
		if taken {
			return kuttErr("Custom URL is already in use.")
		}
	}
	if up.Target != nil {
		host := removeWww(parseLegacyURL(*up.Target).Hostname)
		if err := s.bannedDomain(ctx, host); err != nil {
			return err
		}
		if err := s.bannedHost(ctx, host); err != nil {
			return err
		}
	}
	lid := link.ID
	updated, err := s.st.Links().Update(ctx, store.LinkFilter{ID: &lid}, up)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return kuttErr("Custom URL is already in use.")
		}
		return err
	}
	s.invalidateLink(ctx, link.Address, link.DomainID)
	if up.Address != nil {
		s.invalidateLink(ctx, *up.Address, link.DomainID)
	}
	if len(updated) == 0 {
		return kuttErr("Link was not found.")
	}
	writeJSON(w, http.StatusOK, s.linkJSON(&updated[0], true))
	return nil
}

// DELETE /links/{id}
func (s *Server) removeLink(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	if err := verify(uuidParam(r)); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("id")
	f := store.LinkFilter{UUID: &id}
	if !p.Admin {
		uid := p.User.ID
		f.UserID = &uid
	}
	link, err := s.st.Links().Find(ctx, f)
	if err != nil || link == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		return kuttErr("Could not find the link.")
	}
	ok, err := s.st.Links().Remove(ctx, f)
	if err != nil {
		return err
	}
	s.invalidateLink(ctx, link.Address, link.DomainID)
	if !ok {
		return kuttErr("Could not delete the link.")
	}
	return message(w, http.StatusOK, "Link has been deleted successfully.")
}

// POST /links/admin/ban/{id}
func (s *Server) banLink(w http.ResponseWriter, r *http.Request) error {
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
	if err := verify(uuidParam(r), boolChain("host"), boolChain("user"), boolChain("userLinks"), boolChain("domain")); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("id")
	link, err := s.st.Links().Find(ctx, store.LinkFilter{UUID: &id})
	if err != nil || link == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		return errBody(http.StatusBadRequest, "No link has been found.")
	}
	if link.Banned {
		return errBody(http.StatusBadRequest, "Link has been banned already.")
	}
	t := true
	by := p.User.ID
	up := store.UpdateLink{Banned: &t, BannedByID: &by}
	fail := func(err error) error {
		s.log.Error("ban link", "err", err)
		return kuttErr("Couldn't ban entries.")
	}
	if _, err := s.st.Links().Update(ctx, store.LinkFilter{UUID: &id}, up); err != nil {
		return fail(err)
	}
	s.invalidateLink(ctx, link.Address, link.DomainID)
	host := removeWww(parseLegacyURL(link.Target).Hostname)
	if boolish(b.get("domain")) && host != "" {
		if d, err := s.st.Domains().Add(ctx, store.UpsertDomain{Address: host}); err != nil {
			return fail(err)
		} else if _, err := s.st.Domains().Update(ctx, store.DomainFilter{ID: &d.ID}, store.UpdateDomain{Banned: &t, BannedByID: &by}); err != nil {
			return fail(err)
		}
		s.invalidateDomain(ctx, host)
	}
	if boolish(b.get("host")) {
		ip, err := s.o.LookupIP(ctx, host)
		if err != nil {
			return kuttErr("Couldn't fetch DNS info.")
		}
		if err := s.st.Hosts().Ban(ctx, ip, by); err != nil {
			return fail(err)
		}
	}
	if boolish(b.get("user")) && link.UserID != nil {
		if _, err := s.st.Users().Update(ctx, store.UserFilter{ID: link.UserID}, store.UpdateUser{Banned: &t, BannedByID: &by}); err != nil {
			return fail(err)
		}
	}
	if boolish(b.get("userLinks")) && link.UserID != nil {
		updated, err := s.st.Links().Update(ctx, store.LinkFilter{UserID: link.UserID}, up)
		if err != nil {
			return fail(err)
		}
		for _, l := range updated {
			s.invalidateLink(ctx, l.Address, l.DomainID)
		}
	}
	return message(w, http.StatusOK, "Banned link successfully.")
}

// GET /links/{id}/stats
func (s *Server) linkStats(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	if err := verify(uuidParam(r)); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("id")
	f := store.LinkFilter{UUID: &id}
	if !p.Admin {
		uid := p.User.ID
		f.UserID = &uid
	}
	link, err := s.st.Links().Find(ctx, f)
	if err != nil || link == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		return kuttErr("Link could not be found.")
	}
	statsBody := s.stats.get(ctx, link.ID)
	if statsBody == nil {
		st, err := s.st.Visits().Stats(ctx, link.ID, s.o.Now())
		if err != nil || st == nil {
			s.log.Error("stats", "err", err)
			return kuttErr("Could not get the short link stats. Try again later.")
		}
		if statsBody, err = statsJSON(st); err != nil {
			return err
		}
		s.stats.put(ctx, link.ID, statsBody)
	}
	// {...stats, ...sanitize.link(link)}: splice the two objects.
	lb, err := marshal(s.linkJSON(link, true))
	if err != nil {
		return err
	}
	out := make([]byte, 0, len(statsBody)+len(lb))
	out = append(out, statsBody[:len(statsBody)-1]...)
	out = append(out, ',')
	out = append(out, lb[1:]...)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	return nil
}

// POST /links/report (MAIL_ENABLED)
func (s *Server) reportLink(w http.ResponseWriter, r *http.Request) error {
	if err := featureAccess(s.cfg.MailEnabled); err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if err := verify(bodyField(b, "link", "No link has been provided.", optNone).exists("").
		mapStr(addProtocol).
		str(func(v string) bool { return removeWww(parseLegacyURL(v).Host) == s.cfg.DefaultDomain },
			"You can only report a "+s.cfg.DefaultDomain+" link.")); err != nil {
		return err
	}
	if err := s.o.Mailer.SendReport(r.Context(), jsString(b.get("link"))); err != nil {
		s.log.Error("report mail", "err", err)
		return kuttErr("Couldn't submit the report. Try again later.")
	}
	return message(w, http.StatusOK, "Thanks for the report, we'll take actions shortly.")
}

var _ = auth.MsgBanned
