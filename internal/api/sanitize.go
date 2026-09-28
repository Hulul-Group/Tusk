package api

import (
	"time"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// Renderers for utils.sanitize.* (utils.js:268-346). Key order follows the
// spread of the knex row (link.queries.js `selectable`, `select *` for
// domains) so the JSON is byte-identical to Kutt on Postgres.

func (s *Server) linkURL(l *domain.Link) string {
	return s.shortURL(l.Address, l.DomainAddress)
}

// linkJSON is sanitize.link. withDomainKey=false models link.create, where
// `link.domain = fetched_domain?.address` is undefined (omitted) for the
// default domain.
func (s *Server) linkJSON(l *domain.Link, withDomainKey bool) obj {
	o := obj{
		{"id", l.UUID},
		{"address", l.Address},
		{"banned", l.Banned},
		{"created_at", jsDate(l.CreatedAt)},
		{"updated_at", jsDate(l.UpdatedAt)},
		{"password", l.Password != nil && *l.Password != ""},
		{"description", strOrNil(l.Description)},
		{"expire_in", dateOrNil(l.ExpireIn)},
		{"target", l.Target},
		{"visit_count", l.VisitCount},
	}
	if withDomainKey || l.DomainAddress != nil {
		o = append(o, field{"domain", strOrNil(l.DomainAddress)})
	}
	return append(o, field{"link", s.linkURL(l)})
}

// linkAdminJSON is sanitize.link_admin.
func (s *Server) linkAdminJSON(l *store.AdminLink, now time.Time) obj {
	dom := s.cfg.DefaultDomain
	if l.DomainAddress != nil && *l.DomainAddress != "" {
		dom = *l.DomainAddress
	}
	link := l.Address
	if l.DomainAddress != nil && *l.DomainAddress != "" {
		link = *l.DomainAddress + "/" + l.Address
	} else {
		link = s.cfg.DefaultDomain + "/" + l.Address
	}
	var relExpire any = nil
	if l.ExpireIn != nil {
		relExpire = fmtMSLong(float64(l.ExpireIn.Sub(now).Milliseconds()))
	}
	return obj{
		{"id", l.UUID},
		{"address", l.Address},
		{"banned", l.Banned},
		{"created_at", jsDate(l.CreatedAt)},
		{"domain_id", int64OrNil(l.DomainID)},
		{"updated_at", jsDate(l.UpdatedAt)},
		{"password", l.Password != nil && *l.Password != ""},
		{"description", strOrNil(l.Description)},
		{"expire_in", dateOrNil(l.ExpireIn)},
		{"target", l.Target},
		{"visit_count", localeInt(l.VisitCount)},
		{"user_id", int64OrNil(l.UserID)},
		{"uuid", l.UUID},
		{"domain", dom},
		{"email", strOrNil(l.Email)},
		{"relative_created_at", timeAgo(l.CreatedAt, now)},
		{"relative_expire_in", relExpire},
		{"link", obj{{"address", l.Address}, {"link", link}, {"url", s.linkURL(&l.Link)}}},
	}
}

// linkCreatedJSON is sanitize.link over knex insert(..., "*"): the full row in
// table column order (link.model.js), plus link.domain when a custom domain was used.
func (s *Server) linkCreatedJSON(l *domain.Link) obj {
	o := obj{
		{"id", l.UUID},
		{"address", l.Address},
		{"description", strOrNil(l.Description)},
		{"banned", l.Banned},
		{"password", l.Password != nil && *l.Password != ""},
		{"expire_in", dateOrNil(l.ExpireIn)},
		{"target", l.Target},
		{"visit_count", l.VisitCount},
		{"created_at", jsDate(l.CreatedAt)},
		{"updated_at", jsDate(l.UpdatedAt)},
	}
	if l.DomainAddress != nil {
		o = append(o, field{"domain", *l.DomainAddress})
	}
	return append(o, field{"link", s.linkURL(l)})
}

// domainJSON is sanitize.domain (homepage falls back to DEFAULT_DOMAIN).
func (s *Server) domainJSON(d *domain.Domain) obj {
	hp := s.cfg.DefaultDomain
	if d.Homepage != nil && *d.Homepage != "" {
		hp = *d.Homepage
	}
	return obj{
		{"id", d.UUID},
		{"banned", d.Banned},
		{"address", d.Address},
		{"homepage", hp},
		{"created_at", jsDate(d.CreatedAt)},
		{"updated_at", jsDate(d.UpdatedAt)},
	}
}

// domainAdminJSON is sanitize.domain_admin (numeric id, raw homepage).
func (s *Server) domainAdminJSON(d *store.AdminDomain, now time.Time) obj {
	return obj{
		{"id", d.ID},
		{"address", d.Address},
		{"homepage", strOrNil(d.Homepage)},
		{"banned", d.Banned},
		{"created_at", jsDate(d.CreatedAt)},
		{"updated_at", jsDate(d.UpdatedAt)},
		{"user_id", int64OrNil(d.UserID)},
		{"uuid", d.UUID},
		{"email", strOrNil(d.Email)},
		{"links_count", localeInt(d.LinksCount)},
		{"relative_created_at", timeAgo(d.CreatedAt, now)},
		{"relative_updated_at", timeAgo(d.UpdatedAt, now)},
	}
}

// userAdminJSON is sanitize.user_admin (users.queries.js selectable_admin).
func (s *Server) userAdminJSON(u *store.AdminUser, now time.Time) obj {
	var doms any = nil
	if len(u.Domains) > 0 {
		j := ""
		for i, d := range u.Domains {
			if i > 0 {
				j += ", "
			}
			j += d
		}
		doms = j
	}
	return obj{
		{"id", u.ID},
		{"email", u.Email},
		{"verified", u.Verified},
		{"role", string(u.Role)},
		{"banned", u.Banned},
		{"banned_by_id", int64OrNil(u.BannedByID)},
		{"created_at", jsDate(u.CreatedAt)},
		{"updated_at", jsDate(u.UpdatedAt)},
		{"links_count", localeInt(u.LinksCount)},
		{"domains", doms},
		{"relative_created_at", timeAgo(u.CreatedAt, now)},
		{"relative_updated_at", timeAgo(u.UpdatedAt, now)},
	}
}

func page(total int64, q pageQuery, data any) obj {
	return obj{{"total", total}, {"limit", q.limit}, {"skip", q.skip}, {"data", data}}
}
