# Kutt → Go 1.27 Port — Software Requirements Specification

Source: /root/Omran/kutt (Kutt v3.2.6, commit 279b491, ~6.1k LOC server JS, 124 .hbs templates)
Target: Go 1.27.1 (current stable per go.dev, checked 2026-09-27)
Authors: 3 Sonnet agents (Parts A/B/C); reconciled and spot-checked against the code by the orchestrator.

## Document map
- Part A: Data layer: schema (PG/SQLite/MySQL DDL), migrations, queries, Redis cache, visit pipeline, cron (DATA-xxx)
- Part B: HTTP API, routes, handlers, auth (local/JWT/API key/OIDC), validation, security, config, mail (API/AUTH/SEC-xxx)
- Part C: Views/htmx, static assets, theming, NFRs, deployment, Go architecture, migration plan, risks (FR/NFR-xxx)

## Verified facts (spot-checked in source)
- bcrypt cost 12 everywhere (auth.handler.js:98, link.queries.js:204, passport.js:118). Go x/crypto/bcrypt reads bcryptjs $2a$ hashes, so existing logins keep working. Set cost=12 explicitly because Go's default is 10.
- JWT exp = now + 7 days (utils.js:44).
- Expired-link cleanup runs on a 30s setInterval (cron.js:5-7).
- Redis TTLs: link/user/domain/host 15 min, "any-user" 5 min, stats 60 s.
- helmet has contentSecurityPolicy disabled (server.js:39). isbot is used in the redirect handler (links.handler.js:532).
- The visits.user_id backfill migration is skipped when visits >= 1,000,000 (20241223103044:27). Such installs need a manual backfill before cutover.
- Visit enrichment: express-useragent + geoip-lite (visit.js:1-2).

## Reconciliation: where parts disagree, this section wins
1. Router: Part B recommends stdlib ServeMux, Part C recommends chi. DECISION: stdlib net/http ServeMux
   (Go 1.22+ method/wildcard patterns). Small middleware chain helpers are enough, which drops one dependency. chi stays acceptable as a fallback
   if route-group ergonomics hurt. The catch-all `/{id}` redirect coexisting with fixed routes is handled by
   ServeMux precedence rules.
2. Env var count: Part C says "101". WRONG. env.js defines ~47 validated vars (Part B's 44–47 is correct).
   Use Part B §5 as the config source of truth.
3. Template count: Part C says 120; actual is 124.
4. GeoIP: Part A says "MaxMind GeoLite2". Node actually uses cf-ipcountry first (links.handler.js:537) and falls back to
   geoip-lite's bundled dataset (visit.js:20). DECISION (GEO-001..005), superseding Part A §6 geo:
   GEO-001 GEO_PROVIDER = auto | cloudflare | geoip | none (default auto).
   GEO-002 cloudflare: country from GEO_HEADER (default CF-IPCountry); "XX", "T1" and empty values are stored as "unknown".
   GEO-003 geoip: country from GEOIP_DB_PATH (.mmdb, MaxMind GeoLite2-Country or DB-IP lite). Only loaded if the path is set.
   GEO-004 auto: trusted header, then the mmdb if configured, then "unknown". none: always "unknown" (country chart empty).
   GEO-005 GEO_TRUST_CLOUDFLARE_ONLY (default true): CF-IPCountry and CF-Connecting-IP are honoured only when the peer
           is in Cloudflare's IP ranges (embedded list, refreshed daily). Stops spoofed stats and rate-limit bypass.
   The Cloudflare zone must have Network → IP Geolocation ON.
5. Timeline: Part C quotes both "11 weeks / 21 engineer-weeks" and a 21-week strangler estimate. Read it as
   ~11 calendar weeks with 2 engineers ≈ 21 engineer-weeks.
6. Queue: Part A lists asynq/River/sync. DECISION (superseded for high traffic): VisitPublisher/VisitConsumer interface
   with 2 impls: Redis Streams consumer group when REDIS_ENABLED (split mode), in-process buffered channel otherwise.
   The worker folds events into per-(link, hour) deltas before writing, so there is one DB update per bucket per flush,
   not one per click. Bull job data is not migrated.
   Drain the Bull queue before cutover.

## Consolidated recommendation
- Strategy: strangler fig behind the existing reverse proxy, running against the same DB. Order: redirect path (GET /{id}) first,
  then API v2, then web/htmx pages, then admin, then decommission Node.
- Data: sqlc with per-dialect query sets (pg/mysql/sqlite), plus a thin repository interface. Drivers: pgx/v5,
  go-sql-driver/mysql, modernc.org/sqlite (pure Go → CGO_ENABLED=0 → distroless/static image).
- Migrations: keep the knex schema as-is. Go tool (goose) starts with a baseline migration that checks
  knex_migrations for the last knex migration (20250106070444_remove_cooldown) and refuses to run otherwise.
- Templates: stdlib html/template, rewriting 124 hbs files. custom/views overrides are kept through a layered fs.FS
  (disk custom/ over embed.FS). The one real breaking change: existing user custom .hbs overrides must be rewritten.
- Auth libs: golang-jwt/jwt/v5 (HS256, same secret → existing cookies valid), coreos/go-oidc/v3 + x/oauth2,
  x/crypto/bcrypt (cost 12). Other libs: go-redis/v9, wneessen/go-mail, caarlos0/env, log/slog.
- Compatibility gates: golden tests diffing Node and Go responses for API v2 JSON and redirect behaviour, and
  testcontainers runs on all 3 DBs.

## Open questions for stakeholders
- Is dropping the Handlebars syntax for custom/ theme overrides acceptable, or do we need a raymond compatibility shim?
- (closed) GeoIP: configurable via GEO_PROVIDER, Cloudflare header by default. See item 4.
- (closed) UI: rebuilt as a React + shadcn/ui SPA. The custom/views .hbs overrides are dropped; css/images overrides are kept.
- (closed) Architecture: microservice split (redirector / api / worker) plus an all-in-one mode. See Tusk docs/ARCHITECTURE.md.
- Must all 3 DB dialects be supported at launch, or PG + SQLite first?
- Installs with >=1M visits: who runs the manual user_id backfill?
