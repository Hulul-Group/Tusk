# Tusk API extensions (beyond Kutt v3.2.6)

Every Kutt route (`server/routes/*.routes.js`) is mounted under both `/api/v2` and
`/api` (server.js:78-79) with Kutt's validation messages, status codes and JSON
shapes. The endpoints and fields below are **Tusk additions**, needed because
Tusk's SPA replaces Kutt's server-rendered/htmx pages. Existing Kutt API clients
are unaffected.

| Method & path (`/api/v2` and `/api`) | Auth | Response | Replaces in Kutt |
|---|---|---|---|
| `GET /config` | none | `{site_name, default_domain, link_length, disallow_registration, disallow_anonymous_links, disallow_login_form, oidc_enabled, oidc_button_text, mail_enabled, report_email, contact_email, server_ip_address, server_cname_address, custom_domain_use_https, needs_admin_setup}` (`Cache-Control: no-store`) | `locals.config` / `res.locals` (locals.handler.js) |
| `POST /auth/logout` | none | 200 `{message:"Logged out."}`, clears the `token` cookie | `GET /logout` render (still served as `GET /logout` → 302 `/`) |
| `POST /auth/verify/{token}` | none | 200 `{token, message}` + cookie; 400 `{error:"Invalid or expired verification link."}` | `GET /verify/:token` render (auth.handler.js verify) |
| `POST /auth/verify-email/{token}` | none | 200 `{token, message}` + cookie; 400 `{error}` | `GET /verify-email/:token` render (auth.handler.js verifyChangeEmail) |
| `POST /links/{id}/protected` | none | JSON clients: `{target}` / `{error}`; HTML form posts: 302 to the target, or the protected page with the error | Kutt `POST /api/v2/links/:id/protected` (same path; the HTML branch renders Tusk's own template) |

## Extra fields and behaviour

- `GET /users` adds `id`, `role` (`"ADMIN"|"USER"`) and `verified` after Kutt's `{apikey, email, domains}`.
- `POST /auth/login`, `/auth/create-admin` and the verify endpoints set the httpOnly `token` cookie for JSON clients as well (Kutt: `utils.setToken`).
- `POST /users/delete` returns 200 `{message:"Account has been deleted."}` and clears the cookie. Kutt returns the text `OK`.
- `POST /links/report` returns 200 `{message}`. Kutt only renders a partial here.
- Unknown `/api/*` paths return 404 `{error:"Not found."}`. Kutt renders the HTML 404 page.
- `POST /users/admin` with `verification_email` issues a real verification token. Kutt sends a link containing `undefined` (users.handler.js create).
- `DELETE /domains/admin/{id}?links=false` keeps the links. Kutt's `if (links)` treats any non-empty string as true.
- `reuse:"false"` (string) counts as false. Kutt tests the raw value for truthiness.
- Mail text bodies use the correct sentences. Kutt's mail/text.js:1-11 swaps the verify and change-email texts.
- Every service answers `GET /api/v2/health` and `GET /api/health` with `OK`. Prometheus metrics are served on `METRICS_PORT` at `/metrics`.
