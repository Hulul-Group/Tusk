# Build audit — Tusk vs SRS (2026-09-28)

Built by 3 Opus 5.5 agents (A data/pipeline, B HTTP services, C web/platform), audited by the
orchestrator. Everything below was re-run by the orchestrator, not copied from agent reports.

## Verified by re-running

| Check | Result |
|---|---|
| `go build ./... && go vet ./...` | clean |
| `go test -race ./...` with real postgres:17, mariadb:11, redis:7 | all packages ok |
| Kutt-created DBs (knex migrate:latest on sqlite/pg/mysql) accepted by `Migrate()` + CRUD | pass on all 3 |
| `docker build` | ok; image 48 MB, distroless, nonroot, 4 binaries |
| E2E all-in-one (sqlite) from the image | pass (see below) |
| E2E split mode: api + redirector + worker on postgres + redis | pass (see below) |

E2E flow in both modes: create-admin 201 → API key → create link → 3 human clicks with
`CF-IPCountry: IQ` + 1 more human click + 1 Googlebot click → stats `visit_count=4` (bot excluded),
country `iq:3, unknown:1`, referrer `google[dot]com` → edit target → redirector serves the NEW target
(cache invalidation across processes via Redis pub/sub) → delete → 302 /404 → SPA `/login` 200.
Zero ERROR/WARN log lines in every container.

## SRS / compatibility gates (docs/ARCHITECTURE.md §7)

| Gate | Status | Evidence |
|---|---|---|
| C1 existing Kutt DB, no schema change | PASS | TestKuttCreatedSchema on 3 dialects; information_schema diff identical (pg/mysql) |
| C2 bcryptjs hashes verify, cost 12 | PASS | auth testdata/node_vectors.json; `BcryptCost = 12` |
| C3 Kutt JWT cookie valid in Tusk | PASS | Node jsonwebtoken vector; HS256, iss ApiAuth, 7d |
| C4 X-API-KEY | PASS | E2E + api tests |
| C5 API v2 JSON shapes | PASS with listed deviations | golden files; docs/API-EXTENSIONS.md |
| C6 visits rows interchangeable with Kutt | PASS | same columns/JSON; concurrency test 8×25 → 1 row, total 200 |
| C7 Redis coexistence | PASS | `tusk:` key prefix |
| GEO-001..005 GEO_PROVIDER auto/cloudflare/geoip/none + CF trust | PASS | geo tests; E2E country `iq` |
| Redirect: 302, no DB write, non-blocking visits | PASS | ResolveHit 103 ns 0 alloc; RedirectHit 887 ns 3 allocs |
| Batched visit writes | PASS | worker folds per (link, hour) |

## Known deviations / follow-ups (deliberate, documented)
- custom/views/*.hbs overrides no longer apply (React UI); custom/css and custom/images still do.
- Kutt bugs fixed on purpose (mail text swap, `undefined` verify link, `?links=false`, `reuse:"false"`).
- `/users/admin?links=` and `?domains=` filters ignored — needs `AdminUserFilter.HasLinks/HasDomains` (CONTRACTS.md).
- First-load JS ≈ 216 kB gzip (stats/admin code-split). Kutt's htmx pages were lighter.
- Mobile render was checked with 390px iframes, not a real device.
- No load test run yet: deploy/k6/redirect.js is included but not executed against a real deployment.
