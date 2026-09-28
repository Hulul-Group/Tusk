# Deploying Tusk

Three ways to run it, from simplest to most scalable:

| Setup | File | Processes | Needs Redis |
|---|---|---|---|
| Single binary + SQLite (drop-in for Kutt) | `../docker-compose.yml` | 1 × `tusk` | no |
| Single binary + Postgres / MariaDB | `../docker-compose.postgres.yml`, `../docker-compose.mariadb.yml` | 1 × `tusk` | optional (cache) |
| Microservice split behind Traefik + Cloudflare | `docker-compose.scale.yml` | N × `redirector`, M × `api`, K × `worker` | **yes** |

All of them use the same image. It contains `/usr/local/bin/{tusk,redirector,api,worker}`.
`tusk` is the default entrypoint; the scale file picks a binary per service with `entrypoint:`.

## Microservice split (`docker-compose.scale.yml`)

```
Internet → Cloudflare → Traefik ─┬─ Host(DEFAULT_DOMAIN) && /{id}          → redirector ×N (:3001)
                                 ├─ custom domains (all paths except /api/) → redirector ×N
                                 ├─ /, /api/*, /assets/*, SPA routes        → api ×M (:3000)
                                 └─ (no ingress)                              worker ×K
redirector ──XADD──▶ Redis stream "tusk:visits" ──XREADGROUP──▶ worker ──▶ Postgres (hourly buckets)
api ──PUBLISH tusk:inval──▶ redirectors (drop local cache entries on edit/delete/ban)
```

```sh
cp deploy/.env.example deploy/.env      # edit DEFAULT_DOMAIN, JWT_SECRET, DB_PASSWORD, CF_DNS_API_TOKEN, ACME_EMAIL
docker compose -f deploy/docker-compose.scale.yml --env-file deploy/.env up -d
docker compose -f deploy/docker-compose.scale.yml --env-file deploy/.env up -d --scale redirector=6 --scale api=2
```

### Routing rules

Traefik picks the router with the highest priority whose rule matches:

| Router | Priority | Rule | Service |
|---|---|---|---|
| `app` | 100 | main host and (`/api/`, `/assets/`, `/images/`, `/css/`, `/fonts/`, `/`, `/favicon.ico`, `/robots.txt`, or a SPA route: `login`, `settings`, `admin`, `stats`, `report`, `terms`, `banned`, `404`, `reset-password/*`, `verify/*`, `verify-email/*`, `create-admin`, `logout`) | api |
| `app-custom` | 50 | custom host and `/api/` (the password form for protected links posts here) | api |
| `redirect` | 10 | main host and a single path segment `^/[^/]+/?$` | redirector |
| `redirect-custom` | 5 | any other host except `/api/` (homepage redirect plus `/{id}`) | redirector |

The SPA route list must match `internal/webui.SPARoutes`. Kutt's `preservedURLs` already stops users from
creating short links with those names, so this routing never hides a real link.

`/banned`, `/404` and the password-protected link page are rendered by the redirector with
`html/template`, so they work without JavaScript. `/banned` and `/404` also exist as SPA routes on the
main domain.

## Cloudflare zone settings

1. **DNS**: `A`/`AAAA` for `DEFAULT_DOMAIN` → origin, **Proxied (orange cloud)**.
2. **SSL/TLS → Overview**: **Full (strict)**. Traefik gets a real origin certificate through ACME
   DNS-01 (`CF_DNS_API_TOKEN` with *Zone → DNS → Edit*). You can also use a Cloudflare Origin CA cert.
3. **Network → IP Geolocation: ON.** Cloudflare then adds `CF-IPCountry`, which Tusk reads when
   `GEO_PROVIDER=cloudflare|auto`. If this is off, the stats country chart only shows "unknown".
4. **Restrict the origin to Cloudflare.** Three layers, each one on its own is enough to stop spoofed
   `CF-IPCountry` / `CF-Connecting-IP` headers:
   - Host firewall: allow 80/443 only from https://www.cloudflare.com/ips-v4 and `ips-v6`
     (for example `ufw allow from 173.245.48.0/20 to any port 443` per range), or use Cloudflare Tunnel.
   - Traefik: the `cf-only` IP allow-list middleware and `forwardedHeaders.trustedIPs=${CLOUDFLARE_IPS}`.
   - Tusk: `GEO_TRUST_CLOUDFLARE_ONLY=true` (the default) only honours CF headers when the peer is inside
     Cloudflare's ranges (embedded list, refreshed every 24 h from `CLOUDFLARE_IP_RANGES_URLS`).
5. **Caching**: do **not** cache redirects at the edge. Stats count every visit, and edits, bans and
   expiry must take effect immediately. The default Cloudflare cache level already skips 302s without
   `Cache-Control`. `/assets/*` is sent with `Cache-Control: public, max-age=31536000, immutable`, so the
   edge caches it with no extra rules.
6. **Speed → Optimization**: turn *Rocket Loader* off (it breaks module scripts). Brotli is fine.
   Tusk also ships precompressed `.br`/`.gz` files.
7. **Security**: WAF managed rules and Bot Fight Mode are fine. Rate-limit `POST /api/v2/links` and
   `/api/v2/auth/*` at the edge if `ENABLE_RATE_LIMIT` is off.

### Custom domains

Kutt tells users to point an `A` record at `SERVER_IP_ADDRESS` or a `CNAME` at `SERVER_CNAME_ADDRESS`,
and, if they use Cloudflare themselves, to set it to **DNS only**. Two ways to handle this:

- **Customer DNS-only → origin**: requests reach Traefik directly, not from Cloudflare IPs. Set
  `CUSTOM_DOMAIN_MIDDLEWARES=none@docker` and open 80/443 to the world. Serve custom domains over HTTP, or
  add an HTTP-01 resolver for them. Country stats then need `GEO_PROVIDER=auto` with `GEOIP_DB_PATH`.
- **Cloudflare for SaaS** (custom hostnames on your zone): Cloudflare terminates TLS for every customer
  hostname and keeps the origin locked down. Point `SERVER_CNAME_ADDRESS` at your fallback origin.

## Geo (`GEO_PROVIDER`)

| Value | Source | When to use |
|---|---|---|
| `auto` (default) | trusted `GEO_HEADER`, then the `.mmdb` if `GEOIP_DB_PATH` is set, else `unknown` | mixed traffic (Cloudflare plus DNS-only custom domains) |
| `cloudflare` | `GEO_HEADER` (default `CF-IPCountry`) only; `XX`/`T1`/empty → unknown | everything is behind Cloudflare |
| `geoip` | `GEOIP_DB_PATH` (MaxMind GeoLite2-Country or DB-IP lite `.mmdb`) | no CDN; mount the file read-only |
| `none` | always `unknown` | privacy: no country tracking |

With `geoip`, mount the database and update it on a schedule (for example with `geoipupdate`):

```yaml
    volumes: ["/srv/geoip/GeoLite2-Country.mmdb:/geo/country.mmdb:ro"]
    environment: { GEO_PROVIDER: geoip, GEOIP_DB_PATH: /geo/country.mmdb }
```

## Scaling guidance

- **redirector**: stateless and CPU-bound. A cache hit costs well under a millisecond, and it never
  writes to the DB on the request path. Scale on CPU (target ~60 %). Each replica keeps an in-process LRU
  (`LOCAL_CACHE_SIZE`, default 100k entries ≈ 30–50 MB). Size it to cover your hot set. After an edit,
  other replicas can serve the old target for at most `LOCAL_CACHE_TTL` (30 s); pub/sub invalidation
  usually clears it in milliseconds.
- **api**: low traffic. Two replicas for availability. Admin tables and stats queries hit the DB; keep
  `DB_POOL_MAX × replicas` below Postgres `max_connections`, or put PgBouncer in front.
- **worker**: one is enough for tens of thousands of clicks per second, because it folds events into
  per-(link, hour) deltas (`VISIT_BATCH_SIZE`, `VISIT_FLUSH_INTERVAL`). Add replicas only if the stream
  lag (`XINFO GROUPS tusk:visits`) keeps growing. They share one consumer group. Delivery is
  at-least-once and events are acked only after the DB commit.
- **Redis**: holds the link cache and the visit stream. Use AOF plus `noeviction` (set in the scale file)
  so unacked visits are never evicted. Memory ≈ link cache plus stream backlog.
- **Postgres**: the only write load is one UPDATE per hot bucket per flush, plus link CRUD. SSD,
  `shared_buffers` ≈ 25 % RAM. `synchronous_commit=off` is a reasonable trade-off for visit counters.
- **Metrics**: every binary exposes Prometheus metrics on `METRICS_PORT` (9090, not published). Scrape
  it on the internal network.

## Load testing

```sh
# origin, bypassing Cloudflare (from a host in the allow-list or on the internal network)
k6 run -e BASE=http://10.0.0.5:3001 -e HOST=s.example.com -e IDS=abc123,def456,ghi789 -e RATE=20000 deploy/k6/redirect.js
# end-to-end through Cloudflare
k6 run -e BASE=https://s.example.com -e IDS=abc123 -e RATE=2000 -e P99_MS=80 deploy/k6/redirect.js
```

The script fails if p99 on cache hits is above `P99_MS` (default 5 ms, the origin budget), if more than
0.1 % of requests error, or if any hit gets a status other than 302/200. 5 % of requests go to random
missing ids to exercise the negative cache.
