# Contract change requests

Build agents write requests here instead of editing shared files
(internal/config, internal/domain, internal/store/store.go). The orchestrator merges them.

Format: `- [agent] file: change — reason`

## Agent C (web SPA → /api/v2) — endpoint requests

The SPA (`web/src/lib/api.ts`, the single typed client) talks only to `/api/v2` with
`credentials: "include"`. Everything below is either a shape the SPA relies on or a
**TUSK EXTENSION** that Kutt only exposed via server-rendered HTML/htmx. All are marked
`TUSK EXTENSION` in `api.ts`.

### Required

- [C] internal/api: `GET /api/v2/config` (public, no auth) → `{site_name, default_domain, disallow_registration, disallow_anonymous_links, disallow_login_form, oidc_enabled, oidc_button_text, mail_enabled, report_email, contact_email, server_ip_address, server_cname_address, needs_admin_setup?}` — replaces Kutt's `res.locals` (locals.handler.js `config`). Values are exactly the Kutt semantics (e.g. `disallow_registration` is true when `!MAIL_ENABLED`). `report_email`/`contact_email` empty string when unset. `needs_admin_setup` (optional bool) = users table empty; the SPA sends the user to `/create-admin` when true (Kutt did this in renders.homepage).
- [C] internal/api: `POST /api/v2/auth/login` MUST set the httpOnly `token` cookie (same attributes as Kutt's `utils.setToken`: httpOnly, sameSite=lax, secure in production, maxAge 7d) for JSON clients too, and still return `{token}` for API compatibility. Same for `POST /auth/create-admin` (Kutt logs the new admin in) and the verify endpoints below.
- [C] internal/api: `GET /api/v2/users` (auth) → Kutt's `{apikey, email, domains}` **plus** `id`, `role` (`"ADMIN"|"USER"`), `verified` — the SPA needs `role` for the Admin nav/route and `id` to hide "ban yourself". 401 JSON `{error}` when not logged in (the SPA treats 401 as "logged out").
- [C] internal/api: `POST /api/v2/auth/logout` (TUSK EXTENSION) → clears the `token` cookie, 200 `{message}`. Kutt did this in the `GET /logout` page render. Fallback in the SPA: if this 404s it does `window.location.assign("/logout")`, so the Go router should ALSO keep `GET /logout` → clear cookie + 302 `/` (Kutt parity for old bookmarks); note `internal/webui` would otherwise answer `/logout` with the SPA shell.
- [C] internal/api: `POST /api/v2/auth/verify/{token}` (TUSK EXTENSION) → JSON form of Kutt's `GET /verify/:token` render: verifies the account, sets the cookie, returns `{token, message}`; 400 `{error}` for invalid/expired token. The SPA route `/verify/:token` calls it.
- [C] internal/api: `POST /api/v2/auth/verify-email/{token}` (TUSK EXTENSION) → JSON form of Kutt's `GET /verify-email/:token` (change-email confirmation). Same response shape.
- [C] internal/api: `POST /api/v2/auth/new-password` must accept `reset_password_token` in the JSON body (Kutt reads it from the form that `/reset-password/:token` rendered after validating the token). SPA route `/reset-password/:token` posts `{reset_password_token, new_password, repeat_password}` (Kutt field names).
- [C] internal/api: JSON error bodies everywhere as `{error: string}` (Kutt's `CustomError` JSON path), including validator errors (`field` optional). The SPA shows `error` verbatim.
- [C] internal/api: admin list endpoints (`GET /links/admin`, `/users/admin`, `/domains/admin`) return Kutt's JSON `{total, limit, skip, data}` with `sanitize.*_admin` rows. The SPA tolerates both numeric and Kutt's `toLocaleString` string forms for `visit_count` / `links_count`, and `link` as a string or `{url}` object.
- [C] internal/api: `POST /api/v2/links/report` returns 200 `{message}` in JSON mode (Kutt only rendered a partial).
- [C] internal/api: `POST /api/v2/users/delete` body `{password}` → 200 `{message}` and clears the cookie.

### Routing (cmd/*, internal/httpx)

- [C] cmd/tusk, cmd/api: mount `webui.Handler(cfg)` for the paths in `webui.SPARoutes` and `webui.AssetPrefixes` (Go 1.22 ServeMux patterns exported from `internal/webui`). Everything else single-segment (`/{id}`) goes to the redirector. `/api/*` is never answered by webui (it returns 404 for `/api/...` defensively).
- [C] cmd/tusk: `/login/oidc` and `/login/oidc/callback` stay server routes (Agent B); the SPA links to `/login/oidc` with a full page navigation.
- [C] internal/redirect: `/banned`, `/404` and the password page should keep server-rendered (html/template) versions for the redirector-only deployment; on the main domain the SPA also has `/banned` and `/404` routes.

### Nice to have

- [C] internal/api: `GET /api/v2/links/{id}/stats` — keep Kutt's shape (`lastDay/lastWeek/lastMonth/lastYear` each with `{stats:{browser,os,country,referrer}, views:[], total}`, plus top-level `total`, `updatedAt`, link fields). The SPA renders Day/Week/Month/Year tabs from these four keys.

## Agent A (data + pipeline) — notes / requests

- [A] internal/redirect (B): use `geo.NewClientIP(cfg)` (`go cip.Run(ctx)` refreshes CF ranges every 24h); `ip := cip.IP(r)`, `country := geoResolver.Country(cip.GeoHeaders(r), ip)`. `GeoHeaders` hides CF-IPCountry unless the request came through Cloudflare (or GEO_TRUST_CLOUDFLARE_ONLY=false).
- [A] internal/redirect (B): publish a VisitEvent only when `target.UserID != nil && !uaparse.IsBot(ua)` (links.handler.js:528-529). `worker.Fold` also drops UserID==nil events defensively. Resolver results are shared pointers: read-only.
- [A] internal/api (B): after any domain add/update/remove/ban call `(*cache.DomainResolver).Invalidate(ctx, address)`; after link edit/delete/ban call `LinkResolver.Invalidate` for the OLD (address, domain_id) too. cmd/* (B): `go cache.Subscribe(ctx, rdb, linkResolver, domainResolver)`.
- [A] internal/store/store.go (orchestrator, optional): `UpdateLink` has no DomainID, which matches Kutt (the edit form can't move a link between domains). No change needed.

## Agent B (HTTP services) — requests

- [B] internal/store/store.go: `AdminUserFilter` add `HasLinks, HasDomains *bool` — Kutt's `GET /users/admin?links=&domains=` filters (users.handler.js getAdmin → user.queries `links`/`domains` is [not] null). Tusk currently ignores these two query params.
- [B] internal/store/store.go: `LinkStore.RemoveByDomain(ctx, domainID) ([]LinkKey, error)` — `DELETE /domains/admin/{id}?links=true` now lists and deletes links one at a time so each can be invalidated. A bulk delete that returns the keys would be O(1) round trips.
- [B] internal/store/store.go: `UserStore.Remove` could return the removed users' `[]LinkKey` and domain addresses. internal/api currently lists them before deleting so redirect caches can be invalidated (links cascade in SQL).
- [B] internal/config: `SITE_URL`/`NODE_ENV` field — auth.Mode is derived from `NODE_ENV` (envalid: unset ⇒ production) in internal/auth because config has no field for it. Promote it to config so every package agrees.
- [B] internal/cache (A): consider exporting `LinkResolver` hit/miss as Prometheus collectors. cmd wires `Hits`/`Misses` atomics as CounterFuncs (`tusk_link_cache_{hits,misses}_total`) for now.
- [B] internal/webui (C): `GET /404` and `GET /banned` on the main domain are answered by the SPA. The standalone redirector serves server-rendered versions (internal/redirect/templates).
