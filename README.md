# Tusk

Tusk is a Go port of [Kutt](https://github.com/thedevs-network/kutt) v3.2.6, the open source URL
shortener, built for high traffic. It is a **drop-in replacement**:

- It runs on your existing Kutt database (Postgres, MySQL/MariaDB or SQLite) with no schema changes.
- API v2 keeps the same routes and JSON shapes, so existing clients and API keys keep working.
- Passwords (bcrypt), the `token` JWT cookie and `X-API-KEY` keys stay valid. Users stay logged in.
- The same environment variables apply, with the same defaults, including the `*_FILE` Docker-secret forms.
- The same Docker volumes and ports (`/var/lib/kutt`, `/kutt/custom`, `:3000`).

What changes:

- **Redirect hot path**: an in-process LRU, then Redis, then one indexed DB query, with singleflight and
  a negative cache. There are no DB writes on the request path. The budget is p99 < 5 ms at the origin.
- **Visit pipeline**: clicks become events, and a worker folds them into per-(link, hour) deltas.
  10k clicks/s on one link costs about one row update per second instead of 10k transactions. The stored
  rows are identical to Kutt's.
- **Optional microservice split**: `redirector` ×N, `api` ×M and `worker` ×K from a single image, behind
  Traefik and Cloudflare. See [deploy/README.md](deploy/README.md).
- **Configurable geo**: the Cloudflare `CF-IPCountry` header, a MaxMind or DB-IP `.mmdb` file, or none.
- **New web UI**: a React 19 + shadcn/ui SPA with the same features as Kutt, a dark mode and mobile
  layouts. It is embedded in the binary.

Architecture: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Requirements: [docs/srs/](docs/srs/).

## Quick start

```sh
# Single binary + SQLite (same as Kutt's docker-compose.yml)
JWT_SECRET=$(openssl rand -hex 32) DEFAULT_DOMAIN=localhost:3000 docker compose up -d
open http://localhost:3000          # first visit → create the admin account
```

With Postgres or MariaDB:

```sh
DB_PASSWORD=change-me JWT_SECRET=... docker compose -f docker-compose.postgres.yml up -d
DB_PASSWORD=change-me JWT_SECRET=... docker compose -f docker-compose.mariadb.yml up -d
```

Scaled out behind Traefik + Cloudflare: [deploy/README.md](deploy/README.md).

### From source

Requires Go 1.27+ and Node 22+ (26 recommended).

```sh
make build        # npm ci + vite build → internal/webui/dist → static binaries in bin/
make run          # ./bin/tusk on :3000 (SQLite at db/data)
make test         # go vet, go test -race ./..., web typecheck + lint
make docker       # docker build -t tusk:dev .
```

`go build ./...` also works without Node. The binary then serves a placeholder page instead of the UI,
and the API and redirects work normally. For UI development, run `cd web && npm run dev`, which proxies
`/api` to `TUSK_API` (default `http://127.0.0.1:3000`).

### Binaries

| Binary | Roles | Port |
|---|---|---|
| `tusk` | all (`SERVICE_ROLES=all`), same as Kutt | `PORT` (3000) |
| `redirector` | `GET /{id}`, custom-domain homepage redirect, protected / banned / 404 pages, `/api/v2/health` | `REDIRECTOR_PORT` (3001) |
| `api` | `/api/v2/*`, auth, OIDC, admin, web UI | `PORT` (3000) |
| `worker` | visit event consumer, expired-link sweeper | metrics only |

The Docker image contains all four under `/usr/local/bin/`. `tusk` is the default entrypoint.

## Configuration

Every Kutt variable is honoured with the same name and default. Each one also accepts `NAME_FILE=/path`,
which reads the value from a file (Docker secrets).

### Kutt variables

| Variable | Description | Default |
|---|---|---|
| `JWT_SECRET` | Signs auth tokens. **Keep the same value as your Kutt install** so sessions survive. Required in production. | dev: `securekey` |
| `PORT` | HTTP port (all-in-one and `api`) | `3000` |
| `SITE_NAME` | Site name shown in the UI and page titles | `Kutt` |
| `DEFAULT_DOMAIN` | Domain the app runs on | `localhost:3000` |
| `LINK_LENGTH` | Length of generated addresses | `6` |
| `LINK_CUSTOM_ALPHABET` | Alphabet for generated addresses | `abcdefghkmnpqrstuvwxyzABCDEFGHKLMNPQRSTUVWXYZ23456789` |
| `DISALLOW_REGISTRATION` | Disable sign-up (sign-up also needs `MAIL_ENABLED`) | `true` |
| `DISALLOW_LOGIN_FORM` | Hide email/password login (use with OIDC) | `false` |
| `DISALLOW_ANONYMOUS_LINKS` | Require login to shorten | `true` |
| `TRUST_PROXY` | Trust `X-Forwarded-For` from the proxy | `true` |
| `DB_CLIENT` | `pg`, `pg-native`, `mysql`, `mysql2`, `sqlite3`, `better-sqlite3` | `better-sqlite3` |
| `DB_FILENAME` | SQLite file | `db/data` |
| `DB_HOST` / `DB_PORT` / `DB_NAME` / `DB_USER` / `DB_PASSWORD` | Postgres/MySQL connection | `localhost` / `5432` / `kutt` / `postgres` / – |
| `DB_SSL` | TLS to the database | `false` |
| `DB_POOL_MIN` / `DB_POOL_MAX` | Connection pool bounds | `0` / `10` |
| `REDIS_ENABLED` | Use Redis (cache, and event stream in split mode) | `false` |
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_PASSWORD` / `REDIS_DB` | Redis connection | `127.0.0.1` / `6379` / – / `0` |
| `SERVER_IP_ADDRESS` | IP shown in the custom-domain instructions (display only) | – |
| `SERVER_CNAME_ADDRESS` | CNAME target shown in the custom-domain instructions (display only) | – |
| `CUSTOM_DOMAIN_USE_HTTPS` | Build `https://` short URLs for custom domains | `false` |
| `ENABLE_RATE_LIMIT` | Rate-limit auth and link-creation routes | `false` |
| `MAIL_ENABLED` | Emails for sign-up, verification, password reset and reports | `false` |
| `MAIL_HOST` / `MAIL_PORT` / `MAIL_SECURE` / `MAIL_USER` / `MAIL_PASSWORD` / `MAIL_FROM` | SMTP | – / `587` / `false` / – / – / – |
| `OIDC_ENABLED` | OpenID Connect login | `false` |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` / `OIDC_PROMPT` | OIDC provider | – |
| `OIDC_SCOPE` | Scopes | `openid profile email` |
| `OIDC_EMAIL_CLAIM` | Claim holding the email | `email` |
| `OIDC_BUTTON_TEXT` | Login button label | `Log in with OIDC` |
| `REPORT_EMAIL` | Receives abuse reports; enables `/report` | – |
| `CONTACT_EMAIL` | Support address shown in the footer | – |
| `NODE_ENV` / `TUSK_ENV` | `production` turns on production checks (for example, a required `JWT_SECRET`) | – |

### Tusk additions

| Variable | Description | Default |
|---|---|---|
| `GEO_PROVIDER` | Visitor country source: `auto` (trusted header, then `.mmdb`, then unknown), `cloudflare`, `geoip`, `none` | `auto` |
| `GEO_HEADER` | Country header name | `CF-IPCountry` |
| `GEOIP_DB_PATH` | MaxMind GeoLite2-Country / DB-IP lite `.mmdb` (required for `geoip`) | – |
| `GEO_TRUST_CLOUDFLARE_ONLY` | Honour `CF-IPCountry` / `CF-Connecting-IP` only from Cloudflare IP ranges | `true` |
| `CLOUDFLARE_IP_RANGES_URLS` | Where to refresh Cloudflare ranges (daily; an embedded list is the fallback) | `https://www.cloudflare.com/ips-v4,https://www.cloudflare.com/ips-v6` |
| `SERVICE_ROLES` | `all`, or a comma list of `api`, `redirector`, `worker`. Anything other than `all` requires `REDIS_ENABLED=true`. | `all` |
| `REDIRECTOR_PORT` | Listen port of `cmd/redirector` | `3001` |
| `METRICS_PORT` | Prometheus `/metrics` listener; `0` disables it | `9090` |
| `LOCAL_CACHE_SIZE` | In-process LRU entries (links + domains) | `100000` |
| `LOCAL_CACHE_TTL` | Upper bound on staleness across nodes after an edit | `30s` |
| `REDIS_CACHE_TTL` | Redis link-cache TTL (Kutt: 15 min) | `15m` |
| `VISIT_BATCH_SIZE` | Visit events folded per DB flush | `500` |
| `VISIT_FLUSH_INTERVAL` | Max wait before a flush | `1s` |
| `VISIT_BUFFER_SIZE` | In-memory event buffer (single-binary mode); overflow is dropped and counted | `100000` |
| `EXPIRED_LINK_SWEEP` | Expired-link cleanup interval (Kutt: 30 s) | `30s` |
| `TUSK_CUSTOM_DIR` | Directory for `images/` and `css/` overrides | `custom` (image: `/kutt/custom`) |

Durations use Go syntax (`500ms`, `30s`, `15m`).

## Theming (`custom/`)

As in Kutt, files in `custom/images/` and `custom/css/` override the built-in ones. They are served at
`/images/*` and `/css/*`. Every `custom/css/*.css` file is linked from the UI. The UI uses shadcn/ui CSS
variables, so a theme can be a few lines:

```css
/* custom/css/theme.css */
:root { --primary: oklch(0.55 0.22 25); --radius: 0.25rem; }
.dark { --primary: oklch(0.7 0.19 25); }
```

Replace `custom/images/logo.png` and the favicons the same way.

**Breaking change:** `custom/views/*.hbs` template overrides no longer apply, because the UI is now a
React app. CSS and image overrides still work.

## Migrating from Kutt

1. **Back up** the database, and `custom/` if you use it.
2. **Drain Bull**: stop Kutt and let its Redis/Bull queue empty. Tusk does not import Bull jobs.
3. **Check migrations**: Tusk requires Kutt's last knex migration (`20250106070444_remove_cooldown`) in
   `knex_migrations`, and refuses to start otherwise. Upgrade Kutt to v3.2.6 first if needed.
4. **Large installs**: if you have **≥ 1,000,000 visits** rows, Kutt skipped the `visits.user_id`
   backfill (`20241223103044`). Run it by hand before cutover.
5. **Use the same environment**: the same `.env`, especially `JWT_SECRET`, `DB_*`, `DEFAULT_DOMAIN`
   and `LINK_*`. Point the image at the same volumes:
   `docker compose up -d` with this repo's `docker-compose*.yml` (same paths as Kutt's).
6. **Optional strangler cut-over**: run Tusk next to Kutt on the same database, and route `GET /{id}` to
   Tusk first, then `/api/v2`, then the UI. The data is shared and the stats rows are identical, so you
   can roll back at any point.
7. **Custom views**: re-create any `custom/views/*.hbs` customisations as CSS variables (see Theming).

API clients need no changes: `X-API-KEY`, `/api/v2/links`, `/api/v2/domains`, `/api/v2/users`, and the
same JSON.

## Web UI

`web/` is Vite + React 19 + TypeScript + Tailwind CSS v4 + shadcn/ui (default theme and components),
TanStack Query and React Router. It talks only to `/api/v2` with `credentials: "include"`, and every call
lives in [`web/src/lib/api.ts`](web/src/lib/api.ts). The admin, settings and stats pages are code-split;
Recharts only loads on the stats page. `npm run build` writes precompressed `.br`/`.gz` files, and
`internal/webui` serves them with immutable caching for hashed assets, `no-cache` for `index.html`,
`custom/` overrides and an SPA fallback.

## License

MIT, like Kutt.
