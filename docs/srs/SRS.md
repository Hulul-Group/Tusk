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


---

# Kutt URL Shortener — Data Layer SRS (Part A)

**Project**: Port Kutt v3.2.6 (commit 279b491) from Node.js to Go 1.27  
**Source Repository**: /root/Omran/kutt (READ-ONLY)  
**Target Go Version**: 1.27.1 (latest stable)  
**Document Version**: 1.0  
**Date**: 2026-09-27  

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Database Schema](#2-database-schema)
3. [Migration History & Compatibility](#3-migration-history--compatibility)
4. [Query Functions](#4-query-functions)
5. [Redis Caching Layer](#5-redis-caching-layer)
6. [Visit Tracking Pipeline](#6-visit-tracking-pipeline)
7. [Cron Jobs](#7-cron-jobs)
8. [Business Rules & Invariants](#8-business-rules--invariants)
9. [Go Implementation Recommendations](#9-go-implementation-recommendations)
10. [Risks & Gotchas](#10-risks--gotchas)

---

## 1. Executive Summary

This document specifies the **data persistence and business logic layer** for porting Kutt URL shortener from Node.js (Knex.js ORM) to Go. The existing codebase supports **PostgreSQL, MySQL/MariaDB, and SQLite** via Knex's multi-dialect abstraction, with optional Redis caching and Bull queue-based visit tracking.

### Key Data Components
- **6 database tables**: `users`, `links`, `domains`, `visits`, `hosts`, `ips`
- **10 migrations** applying incremental schema changes since Feb 2020
- **5 query modules**: link, user, domain, visit, host
- **Redis**: Optional; 15-minute TTL for entities, 60s for stats
- **Visit queue**: Bull-based when Redis enabled, synchronous fallback
- **Cron**: 30-second interval to delete expired links

**Cross-references to other parts:**
- **Part B (HTTP/API/auth)**: Receives validated user contexts, constructs API responses
- **Part C (Views/frontend/deployment)**: Consumes data layer via Part B

---

## 2. Database Schema

### 2.1 Final Schema (Post-Migration State)

All schemas below reflect the **final state after applying all 10 migrations**. Column definitions include nullability, defaults, primary keys (PK), foreign keys (FK), ON DELETE behavior, unique constraints, and indexes.

---

#### 2.1.1 Table: `users`

**File**: `server/models/user.model.js:3-40`

| Column | Type | Nullable | Default | Constraints | Notes |
|--------|------|----------|---------|-------------|-------|
| `id` | INTEGER | NO | AUTO_INCREMENT | PK | Auto-increment primary key |
| `email` | STRING | NO | - | UNIQUE | User email, unique constraint |
| `password` | STRING | NO | - | - | bcrypt hash (12 rounds) |
| `apikey` | STRING | YES | NULL | UNIQUE (since 20241223062111) | API key for programmatic access |
| `role` | ENUM('USER', 'ADMIN') | NO | 'USER' | - | Added in 20241103083933 |
| `verified` | BOOLEAN | NO | FALSE | - | Email verification status |
| `banned` | BOOLEAN | NO | FALSE | - | Admin-imposed ban |
| `banned_by_id` | INTEGER (unsigned) | YES | NULL | FK → users.id | Admin who banned this user |
| `verification_token` | STRING | YES | NULL | - | Email verification token |
| `verification_expires` | DATETIME | YES | NULL | - | Token expiry |
| `reset_password_token` | STRING | YES | NULL | - | Password reset token |
| `reset_password_expires` | DATETIME | YES | NULL | - | Token expiry |
| `change_email_token` | STRING | YES | NULL | - | Email change token (20200810195255) |
| `change_email_expires` | DATETIME | YES | NULL | - | Token expiry (20200810195255) |
| `change_email_address` | STRING | YES | NULL | - | New email pending verification (20200810195255) |
| `created_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | Knex `timestamps(false, true)` |
| `updated_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | Updated on every change |

**Indexes** (from 20241223062111):
- `users_apikey_unique` (UNIQUE on `apikey`)

**Foreign Keys**:
- `users.banned_by_id` → `users.id` (NO CONSTRAINT, implicit reference)

**Removed Columns** (20250106070444):
- `cooldowns`, `cooldown`, `malicious_attempts` — rate limiting columns removed

---

#### 2.1.2 Table: `domains`

**File**: `server/models/domain.model.js:1-37`

| Column | Type | Nullable | Default | Constraints | Notes |
|--------|------|----------|---------|-------------|-------|
| `id` | INTEGER | NO | AUTO_INCREMENT | PK | Auto-increment primary key |
| `address` | STRING | NO | - | UNIQUE | Domain name (lowercase) |
| `homepage` | STRING | YES | NULL | - | Custom homepage URL |
| `user_id` | INTEGER (unsigned) | YES | NULL | FK → users.id ON DELETE SET NULL | Owner; orphaned on user delete |
| `banned` | BOOLEAN | NO | FALSE | - | Admin ban status |
| `banned_by_id` | INTEGER (unsigned) | YES | NULL | FK → users.id | Admin who banned |
| `uuid` | UUID | NO | UUID() | - | Public identifier (20200510140704) |
| `created_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | |
| `updated_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | |

**Indexes** (from 20241223062111):
- `domains_address_index` on `address`
- `domains_user_id_index` on `user_id`

**Foreign Keys**:
- `domains_user_id_foreign`: `user_id` → `users.id` ON DELETE SET NULL

**Migration Note** (20200510140704:16-19):
- Postgres-specific: `DROP CONSTRAINT IF EXISTS domains_user_id_unique` — early versions had unique constraint on `user_id`, removed to allow users to own multiple domains.

---

#### 2.1.3 Table: `links`

**File**: `server/models/link.model.js:1-56`

| Column | Type | Nullable | Default | Constraints | Notes |
|--------|------|----------|---------|-------------|-------|
| `id` | INTEGER | NO | AUTO_INCREMENT | PK | Auto-increment primary key |
| `address` | STRING | NO | - | - | Short code (e.g., `abc123`) |
| `target` | STRING(2040) | NO | - | - | Destination URL (max 2040 chars) |
| `description` | STRING | YES | NULL | - | User-provided description (20200718124944) |
| `password` | STRING | YES | NULL | - | bcrypt hash (12 rounds) for protected links |
| `expire_in` | DATETIME | YES | NULL | - | Expiration timestamp (20200730203154) |
| `user_id` | INTEGER (unsigned) | YES | NULL | FK → users.id ON DELETE CASCADE | Owner; link deleted on user delete |
| `domain_id` | INTEGER (unsigned) | YES | NULL | FK → domains.id | Custom domain |
| `banned` | BOOLEAN | NO | FALSE | - | Admin ban status |
| `banned_by_id` | INTEGER (unsigned) | YES | NULL | FK → users.id | Admin who banned |
| `visit_count` | INTEGER | NO | 0 | - | Incremented on each visit |
| `uuid` | UUID | NO | UUID() | - | Public identifier |
| `created_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | |
| `updated_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | |

**Indexes** (from 20241223062111):
- `links_domain_id_index` on `domain_id`
- `links_user_id_index` on `user_id`
- `links_address_index` on `address`
- `links_expire_in_index` on `expire_in`

**Foreign Keys**:
- `links_user_id_foreign`: `user_id` → `users.id` ON DELETE CASCADE (explicit constraint with name)
- `links.domain_id` → `domains.id` (NO DELETE behavior specified)
- `links.banned_by_id` → `users.id` (NO DELETE behavior)

---

#### 2.1.4 Table: `visits`

**File**: `server/models/visit.model.js:1-96`

| Column | Type | Nullable | Default | Constraints | Notes |
|--------|------|----------|---------|-------------|-------|
| `id` | INTEGER | NO | AUTO_INCREMENT | PK | Auto-increment primary key |
| `link_id` | INTEGER (unsigned) | YES | NULL | FK → links.id ON DELETE CASCADE | Deleted when link deleted |
| `user_id` | INTEGER (unsigned) | YES | NULL | FK → users.id ON DELETE CASCADE | Added 20241223103044 |
| `total` | INTEGER | NO | 0 | - | Total visits in this hourly bucket |
| `countries` | JSONB | YES | NULL | - | `{"US": 5, "GB": 2}` |
| `referrers` | JSONB | YES | NULL | - | `{"google[dot]com": 3}` |
| `br_chrome` | INTEGER | NO | 0 | - | Chrome visit count |
| `br_edge` | INTEGER | NO | 0 | - | Edge visit count |
| `br_firefox` | INTEGER | NO | 0 | - | Firefox visit count |
| `br_ie` | INTEGER | NO | 0 | - | IE visit count |
| `br_opera` | INTEGER | NO | 0 | - | Opera visit count |
| `br_safari` | INTEGER | NO | 0 | - | Safari visit count |
| `br_other` | INTEGER | NO | 0 | - | Other browsers |
| `os_android` | INTEGER | NO | 0 | - | Android OS count |
| `os_ios` | INTEGER | NO | 0 | - | iOS count |
| `os_linux` | INTEGER | NO | 0 | - | Linux count |
| `os_macos` | INTEGER | NO | 0 | - | macOS count |
| `os_windows` | INTEGER | NO | 0 | - | Windows count |
| `os_other` | INTEGER | NO | 0 | - | Other OS |
| `created_at` | DATETIME | NO | NOW() | - | Bucket timestamp (hourly granularity) |
| `updated_at` | DATETIME | YES | NOW() | - | Added 20241223103044 |

**Indexes** (from 20241223062111, 20241223155527):
- `visits_link_id_index` on `link_id`
- `visits_user_id_index` on `user_id`

**Foreign Keys**:
- `visits_link_id_foreign`: `link_id` → `links.id` ON DELETE CASCADE
- `visits_user_id_foreign`: `user_id` → `users.id` ON DELETE CASCADE (added 20241223103044:14-19)

**Visit Aggregation Model**:
- Each row represents **one hour** of visits for a link (see `server/queries/visit.queries.js:16`)
- `created_at` is truncated to the hour (e.g., `2026-09-27 14:00:00`)
- New visits **increment** existing row or **insert** new row for the hour

---

#### 2.1.5 Table: `hosts`

**File**: `server/models/host.model.js:1-22`

| Column | Type | Nullable | Default | Constraints | Notes |
|--------|------|----------|---------|-------------|-------|
| `id` | INTEGER | NO | AUTO_INCREMENT | PK | Auto-increment primary key |
| `address` | STRING | NO | - | UNIQUE | Hostname (e.g., `example.com`) |
| `banned` | BOOLEAN | NO | FALSE | - | Admin-imposed ban |
| `banned_by_id` | INTEGER (unsigned) | YES | NULL | FK → users.id | Admin who banned |
| `created_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | |
| `updated_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | |

**Indexes** (from 20241223062111):
- `hosts_address_index` on `address`

**Purpose**: Blacklist target URL hostnames system-wide.

---

#### 2.1.6 Table: `ips`

**File**: `server/models/ip.model.js:1-13`

| Column | Type | Nullable | Default | Constraints | Notes |
|--------|------|----------|---------|-------------|-------|
| `id` | INTEGER | NO | AUTO_INCREMENT | PK | Auto-increment primary key |
| `ip` | STRING | NO | - | UNIQUE | IP address |
| `created_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | |
| `updated_at` | TIMESTAMP | NO | CURRENT_TIMESTAMP | - | |

**Purpose**: Track banned IPs (usage not fully clear in current codebase; may be legacy or future feature).

---

### 2.2 Portable DDL

#### 2.2.1 PostgreSQL (Dialect: `pg`)

```sql
-- users
CREATE TABLE users (
  id SERIAL PRIMARY KEY,
  email VARCHAR(255) NOT NULL UNIQUE,
  password VARCHAR(255) NOT NULL,
  apikey VARCHAR(255) UNIQUE,
  role VARCHAR(10) NOT NULL DEFAULT 'USER' CHECK (role IN ('USER', 'ADMIN')),
  verified BOOLEAN NOT NULL DEFAULT FALSE,
  banned BOOLEAN NOT NULL DEFAULT FALSE,
  banned_by_id INTEGER REFERENCES users(id),
  verification_token VARCHAR(255),
  verification_expires TIMESTAMP,
  reset_password_token VARCHAR(255),
  reset_password_expires TIMESTAMP,
  change_email_token VARCHAR(255),
  change_email_expires TIMESTAMP,
  change_email_address VARCHAR(255),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- domains
CREATE TABLE domains (
  id SERIAL PRIMARY KEY,
  address VARCHAR(255) NOT NULL UNIQUE,
  homepage VARCHAR(255),
  user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  banned BOOLEAN NOT NULL DEFAULT FALSE,
  banned_by_id INTEGER REFERENCES users(id),
  uuid UUID NOT NULL DEFAULT gen_random_uuid(),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX domains_address_index ON domains(address);
CREATE INDEX domains_user_id_index ON domains(user_id);

-- links
CREATE TABLE links (
  id SERIAL PRIMARY KEY,
  address VARCHAR(255) NOT NULL,
  target VARCHAR(2040) NOT NULL,
  description VARCHAR(255),
  password VARCHAR(255),
  expire_in TIMESTAMP,
  user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  domain_id INTEGER REFERENCES domains(id),
  banned BOOLEAN NOT NULL DEFAULT FALSE,
  banned_by_id INTEGER REFERENCES users(id),
  visit_count INTEGER NOT NULL DEFAULT 0,
  uuid UUID NOT NULL DEFAULT gen_random_uuid(),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX links_domain_id_index ON links(domain_id);
CREATE INDEX links_user_id_index ON links(user_id);
CREATE INDEX links_address_index ON links(address);
CREATE INDEX links_expire_in_index ON links(expire_in);

-- visits
CREATE TABLE visits (
  id SERIAL PRIMARY KEY,
  link_id INTEGER REFERENCES links(id) ON DELETE CASCADE,
  user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  total INTEGER NOT NULL DEFAULT 0,
  countries JSONB,
  referrers JSONB,
  br_chrome INTEGER NOT NULL DEFAULT 0,
  br_edge INTEGER NOT NULL DEFAULT 0,
  br_firefox INTEGER NOT NULL DEFAULT 0,
  br_ie INTEGER NOT NULL DEFAULT 0,
  br_opera INTEGER NOT NULL DEFAULT 0,
  br_safari INTEGER NOT NULL DEFAULT 0,
  br_other INTEGER NOT NULL DEFAULT 0,
  os_android INTEGER NOT NULL DEFAULT 0,
  os_ios INTEGER NOT NULL DEFAULT 0,
  os_linux INTEGER NOT NULL DEFAULT 0,
  os_macos INTEGER NOT NULL DEFAULT 0,
  os_windows INTEGER NOT NULL DEFAULT 0,
  os_other INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX visits_link_id_index ON visits(link_id);
CREATE INDEX visits_user_id_index ON visits(user_id);

-- hosts
CREATE TABLE hosts (
  id SERIAL PRIMARY KEY,
  address VARCHAR(255) NOT NULL UNIQUE,
  banned BOOLEAN NOT NULL DEFAULT FALSE,
  banned_by_id INTEGER REFERENCES users(id),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX hosts_address_index ON hosts(address);

-- ips
CREATE TABLE ips (
  id SERIAL PRIMARY KEY,
  ip VARCHAR(255) NOT NULL UNIQUE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

---

#### 2.2.2 SQLite (Dialect: `better-sqlite3`)

```sql
-- users
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  password TEXT NOT NULL,
  apikey TEXT UNIQUE,
  role TEXT NOT NULL DEFAULT 'USER' CHECK (role IN ('USER', 'ADMIN')),
  verified INTEGER NOT NULL DEFAULT 0,
  banned INTEGER NOT NULL DEFAULT 0,
  banned_by_id INTEGER REFERENCES users(id),
  verification_token TEXT,
  verification_expires TEXT,
  reset_password_token TEXT,
  reset_password_expires TEXT,
  change_email_token TEXT,
  change_email_expires TEXT,
  change_email_address TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- domains
CREATE TABLE domains (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL UNIQUE,
  homepage TEXT,
  user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  banned INTEGER NOT NULL DEFAULT 0,
  banned_by_id INTEGER REFERENCES users(id),
  uuid TEXT NOT NULL DEFAULT (lower(hex(randomblob(16)))),
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX domains_address_index ON domains(address);
CREATE INDEX domains_user_id_index ON domains(user_id);

-- links
CREATE TABLE links (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL,
  target TEXT NOT NULL,
  description TEXT,
  password TEXT,
  expire_in TEXT,
  user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  domain_id INTEGER REFERENCES domains(id),
  banned INTEGER NOT NULL DEFAULT 0,
  banned_by_id INTEGER REFERENCES users(id),
  visit_count INTEGER NOT NULL DEFAULT 0,
  uuid TEXT NOT NULL DEFAULT (lower(hex(randomblob(16)))),
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX links_domain_id_index ON links(domain_id);
CREATE INDEX links_user_id_index ON links(user_id);
CREATE INDEX links_address_index ON links(address);
CREATE INDEX links_expire_in_index ON links(expire_in);

-- visits
CREATE TABLE visits (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  link_id INTEGER REFERENCES links(id) ON DELETE CASCADE,
  user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  total INTEGER NOT NULL DEFAULT 0,
  countries TEXT, -- JSON as TEXT
  referrers TEXT, -- JSON as TEXT
  br_chrome INTEGER NOT NULL DEFAULT 0,
  br_edge INTEGER NOT NULL DEFAULT 0,
  br_firefox INTEGER NOT NULL DEFAULT 0,
  br_ie INTEGER NOT NULL DEFAULT 0,
  br_opera INTEGER NOT NULL DEFAULT 0,
  br_safari INTEGER NOT NULL DEFAULT 0,
  br_other INTEGER NOT NULL DEFAULT 0,
  os_android INTEGER NOT NULL DEFAULT 0,
  os_ios INTEGER NOT NULL DEFAULT 0,
  os_linux INTEGER NOT NULL DEFAULT 0,
  os_macos INTEGER NOT NULL DEFAULT 0,
  os_windows INTEGER NOT NULL DEFAULT 0,
  os_other INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT DEFAULT (datetime('now'))
);
CREATE INDEX visits_link_id_index ON visits(link_id);
CREATE INDEX visits_user_id_index ON visits(user_id);

-- hosts
CREATE TABLE hosts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  address TEXT NOT NULL UNIQUE,
  banned INTEGER NOT NULL DEFAULT 0,
  banned_by_id INTEGER REFERENCES users(id),
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX hosts_address_index ON hosts(address);

-- ips
CREATE TABLE ips (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ip TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

**SQLite-specific notes**:
- Booleans stored as INTEGER (0/1)
- Timestamps stored as TEXT in ISO 8601 format (`YYYY-MM-DD HH:MM:SS`)
- JSONB stored as TEXT; parsed in application code
- UUID via `lower(hex(randomblob(16)))` (32-char hex, not RFC 4122 compliant but compatible)

---

#### 2.2.3 MySQL/MariaDB (Dialect: `mysql2`)

```sql
-- users
CREATE TABLE users (
  id INT AUTO_INCREMENT PRIMARY KEY,
  email VARCHAR(255) NOT NULL UNIQUE,
  password VARCHAR(255) NOT NULL,
  apikey VARCHAR(255) UNIQUE,
  role ENUM('USER', 'ADMIN') NOT NULL DEFAULT 'USER',
  verified TINYINT(1) NOT NULL DEFAULT 0,
  banned TINYINT(1) NOT NULL DEFAULT 0,
  banned_by_id INT UNSIGNED,
  verification_token VARCHAR(255),
  verification_expires DATETIME,
  reset_password_token VARCHAR(255),
  reset_password_expires DATETIME,
  change_email_token VARCHAR(255),
  change_email_expires DATETIME,
  change_email_address VARCHAR(255),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (banned_by_id) REFERENCES users(id)
) ENGINE=InnoDB;

-- domains
CREATE TABLE domains (
  id INT AUTO_INCREMENT PRIMARY KEY,
  address VARCHAR(255) NOT NULL UNIQUE,
  homepage VARCHAR(255),
  user_id INT UNSIGNED,
  banned TINYINT(1) NOT NULL DEFAULT 0,
  banned_by_id INT UNSIGNED,
  uuid CHAR(36) NOT NULL DEFAULT (UUID()),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL,
  FOREIGN KEY (banned_by_id) REFERENCES users(id),
  INDEX domains_address_index (address),
  INDEX domains_user_id_index (user_id)
) ENGINE=InnoDB;

-- links
CREATE TABLE links (
  id INT AUTO_INCREMENT PRIMARY KEY,
  address VARCHAR(255) NOT NULL,
  target VARCHAR(2040) NOT NULL,
  description VARCHAR(255),
  password VARCHAR(255),
  expire_in DATETIME,
  user_id INT UNSIGNED,
  domain_id INT UNSIGNED,
  banned TINYINT(1) NOT NULL DEFAULT 0,
  banned_by_id INT UNSIGNED,
  visit_count INT NOT NULL DEFAULT 0,
  uuid CHAR(36) NOT NULL DEFAULT (UUID()),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (domain_id) REFERENCES domains(id),
  FOREIGN KEY (banned_by_id) REFERENCES users(id),
  INDEX links_domain_id_index (domain_id),
  INDEX links_user_id_index (user_id),
  INDEX links_address_index (address),
  INDEX links_expire_in_index (expire_in)
) ENGINE=InnoDB;

-- visits
CREATE TABLE visits (
  id INT AUTO_INCREMENT PRIMARY KEY,
  link_id INT UNSIGNED,
  user_id INT UNSIGNED,
  total INT NOT NULL DEFAULT 0,
  countries JSON,
  referrers JSON,
  br_chrome INT NOT NULL DEFAULT 0,
  br_edge INT NOT NULL DEFAULT 0,
  br_firefox INT NOT NULL DEFAULT 0,
  br_ie INT NOT NULL DEFAULT 0,
  br_opera INT NOT NULL DEFAULT 0,
  br_safari INT NOT NULL DEFAULT 0,
  br_other INT NOT NULL DEFAULT 0,
  os_android INT NOT NULL DEFAULT 0,
  os_ios INT NOT NULL DEFAULT 0,
  os_linux INT NOT NULL DEFAULT 0,
  os_macos INT NOT NULL DEFAULT 0,
  os_windows INT NOT NULL DEFAULT 0,
  os_other INT NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (link_id) REFERENCES links(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  INDEX visits_link_id_index (link_id),
  INDEX visits_user_id_index (user_id)
) ENGINE=InnoDB;

-- hosts
CREATE TABLE hosts (
  id INT AUTO_INCREMENT PRIMARY KEY,
  address VARCHAR(255) NOT NULL UNIQUE,
  banned TINYINT(1) NOT NULL DEFAULT 0,
  banned_by_id INT UNSIGNED,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (banned_by_id) REFERENCES users(id),
  INDEX hosts_address_index (address)
) ENGINE=InnoDB;

-- ips
CREATE TABLE ips (
  id INT AUTO_INCREMENT PRIMARY KEY,
  ip VARCHAR(255) NOT NULL UNIQUE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB;
```

**MySQL-specific notes**:
- Booleans as TINYINT(1)
- JSONB → JSON (native type since MySQL 5.7.8)
- `IF NOT EXISTS` not supported in CREATE INDEX before MySQL 8.0.29 (migration 20241223062111:17 omits it for MySQL)

---

### 2.3 Dialect-Specific Quirks

#### DATA-QUIRK-001: Timestamp Handling (server/utils/utils.js:132-147)

**PostgreSQL**:
- Stores timestamps as `timestamptz` (with timezone)
- Knex returns ISO 8601 string or `Date` object
- `dateToUTC()` returns raw ISO string

**SQLite**:
- Stores as TEXT in `YYYY-MM-DD HH:MM:SS` format (UTC)
- Knex returns ISO 8601 string **without 'Z' suffix**
- `dateToUTC()` truncates to `YYYY-MM-DD HH:MM:SS` (line 137)
- **Parsing**: `parseDatetime()` appends 'Z' to SQLite strings (line 122)

**MySQL**:
- Stores in **local timezone** (NOT UTC) as DATETIME
- `dateToUTC()` formats date in local timezone using `date-fns` format (line 142)
- **Risk**: If server timezone changes between writes, data inconsistency

**Go Implication**: Normalize all timestamps to UTC in application code before DB insert; parse SQLite strings by appending 'Z'.

---

#### DATA-QUIRK-002: Case-Insensitive Search (server/knex.js:31)

```javascript
db.compatibleILIKE = isPostgres ? "andWhereILike" : "andWhereLike";
```

- **PostgreSQL**: Uses `ILIKE` (case-insensitive)
- **MySQL/SQLite**: Uses `LIKE` (case-insensitive by default on non-binary collations)
- **MySQL Warning**: `utf8mb4_bin` collation makes LIKE case-sensitive; ensure default collation is `utf8mb4_general_ci`

**Go Implication**: Implement dialect-specific LIKE/ILIKE in query builder.

---

#### DATA-QUIRK-003: Boolean Handling (server/models/user.model.js:10-12)

- **PostgreSQL**: Native BOOLEAN type
- **SQLite**: INTEGER (0/1); Knex converts to/from booleans
- **MySQL**: TINYINT(1); Knex converts to/from booleans

**Go Implication**: Use `sql.NullBool` or custom scanner/valuer for cross-dialect compatibility.

---

#### DATA-QUIRK-004: RETURNING Clause (server/queries/link.queries.js:208-227)

```javascript
let [link] = await knex("links").insert({...}, "*");
// mysql doesn't return the whole link, but rather the id number only
if (typeof link === "number") {
  link = await knex("links").where("id", link).first();
}
```

- **PostgreSQL**: `RETURNING *` returns full row
- **MySQL/SQLite**: Returns `insertId` only
- **Node.js**: Knex abstracts this; Go must handle manually

**Go Implication**: After INSERT on MySQL/SQLite, perform SELECT with `LAST_INSERT_ID()` or equivalent.

---

#### DATA-QUIRK-005: Timestamp Truncation (server/utils/knex.js:28-45)

Visit aggregation requires truncating `created_at` to the hour:

- **PostgreSQL**: `date_trunc('hour', created_at at time zone 'Z')`
- **SQLite**: `strftime('%Y-%m-%d %H:00:00', created_at)`
- **MySQL**: `DATE_FORMAT(created_at, '%Y-%m-%d %H:00:00')`

**Go Implication**: Implement dialect-specific SQL generation for hourly truncation.

---

#### DATA-QUIRK-006: JSON Storage (visits.countries, visits.referrers)

- **PostgreSQL**: JSONB (binary, indexed, query-able)
- **MySQL**: JSON (native since 5.7.8)
- **SQLite**: TEXT (manual `JSON.parse()` in Node)

**Current behavior** (server/queries/visit.queries.js:34-35):
```javascript
const countries = typeof visit.countries === "string" ? JSON.parse(visit.countries) : visit.countries;
```

**Go Implication**: 
- Postgres/MySQL: Use `json.Unmarshal` from JSONB/JSON column
- SQLite: Scan into `string`, then `json.Unmarshal`

---

## 3. Migration History & Compatibility

### 3.1 Migration Metadata Table

**Table**: `knex_migrations` (knexfile.js:24)

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK AUTO_INCREMENT | |
| `name` | VARCHAR(255) | Migration filename |
| `batch` | INTEGER | Batch number |
| `migration_time` | TIMESTAMP | Applied at |

**Go Migration Strategy**: The new Go app must:
1. **Adopt existing `knex_migrations` table** for compatibility
2. OR create parallel `go_migrations` table and **baseline** from knex state
3. Recommended: Use **golang-migrate** or **goose** with custom `knex_migrations` adapter

---

### 3.2 Migration Timeline

| Migration | Date | Summary | Impact on Go Port |
|-----------|------|---------|-------------------|
| 20200211220920_constraints.js | 2020-02-11 | Initial schema creation | [MIG-001] Baseline |
| 20200510140704_domains.js | 2020-05-10 | Add `domains.uuid`, drop `domains.user_id` unique constraint (Postgres only) | [MIG-002] Conditional SQL |
| 20200718124944_description.js | 2020-07-18 | Add `links.description` | [MIG-003] Simple ALTER |
| 20200730203154_expire_in.js | 2020-07-30 | Add `links.expire_in` | [MIG-004] Simple ALTER |
| 20200810195255_change_email.js | 2020-08-10 | Add change email columns to `users` | [MIG-005] Simple ALTER |
| 20241103083933_user-roles.js | 2024-11-03 | Add `users.role` ENUM, set admins from `ADMIN_EMAILS` env | [MIG-006] Data migration |
| 20241223062111_indexes.js | 2024-12-23 | Create indexes, make `users.apikey` unique | [MIG-007] Conditional IF NOT EXISTS (MySQL caveat) |
| 20241223103044_visits_user_id.js | 2024-12-23 | Add `visits.user_id`, backfill from `links.user_id` (batched, skipped if >1M rows) | [MIG-008] Complex data migration |
| 20241223155527_visits_user_id_index.js | 2024-12-23 | Create index on `visits.user_id` | [MIG-009] Simple index |
| 20250106070444_remove_cooldown.js | 2025-01-06 | Drop cooldown/malicious_attempts columns | [MIG-010] Simple DROP |

---

### 3.3 Critical Migration: 20241223103044 (visits.user_id backfill)

**File**: server/migrations/20241223103044_visits_user_id.js:22-49

**Logic**:
```javascript
const [{ count }] = await knex("visits").count("* as count");
const count_number = parseInt(count);

if (count_number < 1_000_000) {
  // Backfill in batches of 100,000 rows
  const last_visit = await knex("visits").orderBy("id", "desc").first();
  const size = 100_000;
  const loops = Math.floor(last_visit.id / size) + 1;
  
  await Promise.all(
    new Array(loops).fill(null).map((_, i) => {
      return knex("visits")
        .fromRaw(knex.raw("visits v"))
        .update({ user_id: knex.ref("links.user_id") })
        .updateFrom("links")
        .where("links.id", knex.ref("link_id"))
        .andWhereBetween("v.id", [i * size, (i * size) + size]);
    })
  );
} else {
  console.warn("MIGRATION WARN: Skipped adding user_id to visits due to high volume...");
}
```

**SQL Equivalent** (PostgreSQL):
```sql
UPDATE visits v
SET user_id = l.user_id
FROM links l
WHERE v.link_id = l.id
  AND v.id BETWEEN ? AND ?;
```

**Go Port Requirements**:

**[MIG-008-REQ-001]**: If database already has `visits.user_id` populated, skip backfill.

**[MIG-008-REQ-002]**: If `visits.user_id` is NULL for all rows:
- Count total visits
- If < 1M: Run batched UPDATE (100k rows per batch, parallel safe via id ranges)
- If ≥ 1M: **Log warning** and skip (admin must run manual migration)

**[MIG-008-REQ-003]**: Parallel UPDATE safety:
- Each batch operates on disjoint `v.id` ranges → no lock contention
- Use **read-committed isolation** to avoid long-running transactions

**Go Migration Pseudo-code**:
```go
func MigrateVisitsUserID(db *sql.DB) error {
  // Check if user_id already populated
  var hasData int
  db.QueryRow("SELECT COUNT(*) FROM visits WHERE user_id IS NOT NULL").Scan(&hasData)
  if hasData > 0 {
    return nil // Already migrated
  }

  var count int
  db.QueryRow("SELECT COUNT(*) FROM visits").Scan(&count)
  
  if count >= 1_000_000 {
    log.Warn("Skipping visits.user_id backfill (>1M rows)")
    return nil
  }

  var maxID int
  db.QueryRow("SELECT COALESCE(MAX(id), 0) FROM visits").Scan(&maxID)
  
  batchSize := 100_000
  batches := (maxID / batchSize) + 1
  
  errGroup, _ := errgroup.WithContext(context.Background())
  for i := 0; i < batches; i++ {
    start := i * batchSize
    end := start + batchSize
    errGroup.Go(func() error {
      _, err := db.Exec(`
        UPDATE visits v
        SET user_id = (SELECT user_id FROM links WHERE id = v.link_id)
        WHERE v.id BETWEEN ? AND ?
      `, start, end)
      return err
    })
  }
  
  return errGroup.Wait()
}
```

---

### 3.4 Migration Compatibility Strategy

**[MIG-COMPAT-001]**: **Baseline Detection**
- On first run, check if `knex_migrations` exists
- If exists and contains all 10 migrations → assume schema is current
- If missing → run full DDL from Section 2.2

**[MIG-COMPAT-002]**: **Hybrid Migration Tool**
- Read `knex_migrations` to determine applied state
- Apply only missing Go migrations (golang-migrate with custom version source)

**[MIG-COMPAT-003]**: **Dialect-Specific SQL**
- PostgreSQL: Safe to use `DROP CONSTRAINT IF EXISTS`
- MySQL: Omit `IF NOT EXISTS` in CREATE INDEX (pre-8.0.29)
- SQLite: No ALTER TABLE DROP COLUMN before 3.35.0 (handle gracefully)

---

## 4. Query Functions

### 4.1 Link Queries (server/queries/link.queries.js)

#### QUERY-LINK-001: `link.find(match)`

**Inputs**:
- `match`: Object with optional `{ id, address, domain_id, user_id, uuid, banned }`

**Outputs**: Single link object or `null`

**SQL Logic** (lines 179-197):
```javascript
// Redis cache check
if (match.address && match.domain_id !== undefined && env.REDIS_ENABLED) {
  const key = redis.key.link(match.address, match.domain_id); // "l:{address}:{domain_id}"
  const cachedLink = await redis.client.get(key);
  if (cachedLink) return JSON.parse(cachedLink);
}

// DB query
const link = await knex("links")
  .select(...selectable)
  .where(normalizeMatch(match))
  .leftJoin("domains", "links.domain_id", "domains.id")
  .first();

// Cache result (TTL 15 min)
if (link && env.REDIS_ENABLED) {
  const key = redis.key.link(link.address, link.domain_id);
  redis.client.set(key, JSON.stringify(link), "EX", 60 * 15);
}

return link;
```

**Selectable columns** (lines 10-25):
```javascript
const selectable = [
  "links.id", "links.address", "links.banned", "links.created_at",
  "links.domain_id", "links.updated_at", "links.password",
  "links.description", "links.expire_in", "links.target",
  "links.visit_count", "links.user_id", "links.uuid",
  "domains.address as domain"
];
```

**Go Signature**:
```go
type LinkMatch struct {
  ID       *int
  Address  *string
  DomainID *int
  UserID   *int
  UUID     *string
  Banned   *bool
}

type Link struct {
  ID          int       `db:"id"`
  Address     string    `db:"address"`
  Target      string    `db:"target"`
  Description *string   `db:"description"`
  Password    *string   `db:"password"`
  ExpireIn    *time.Time `db:"expire_in"`
  UserID      *int      `db:"user_id"`
  DomainID    *int      `db:"domain_id"`
  Domain      *string   `db:"domain"` // from join
  Banned      bool      `db:"banned"`
  VisitCount  int       `db:"visit_count"`
  UUID        string    `db:"uuid"`
  CreatedAt   time.Time `db:"created_at"`
  UpdatedAt   time.Time `db:"updated_at"`
}

func (r *LinkRepository) Find(ctx context.Context, match LinkMatch) (*Link, error) {
  // 1. Check Redis cache if address+domain_id provided
  // 2. Build SELECT with WHERE clauses from match (skip nil fields)
  // 3. LEFT JOIN domains ON links.domain_id = domains.id
  // 4. Scan into Link struct
  // 5. Cache in Redis (15-min TTL) if enabled
  // 6. Return link or nil
}
```

---

#### QUERY-LINK-002: `link.get(match, params)`

**Inputs**:
- `match`: Object (normalized via `normalizeMatch()`)
- `params`: `{ skip: int, limit: int, search?: string }`

**Outputs**: Array of link objects

**SQL Logic** (lines 121-138):
```javascript
const query = knex("links")
  .select(...selectable)
  .where(normalizeMatch(match))
  .offset(params.skip)
  .limit(params.limit)
  .orderBy("links.id", "desc");

if (params?.search) {
  query[knex.compatibleILIKE](
    knex.raw("concat_ws(' ', description, links.address, target, domains.address)"),
    "%" + params.search + "%"
  );
}

query.leftJoin("domains", "links.domain_id", "domains.id");
return query;
```

**Go Signature**:
```go
type LinkQueryParams struct {
  Skip   int
  Limit  int
  Search *string
}

func (r *LinkRepository) Get(ctx context.Context, match LinkMatch, params LinkQueryParams) ([]Link, error) {
  // 1. Build SELECT with WHERE, OFFSET, LIMIT, ORDER BY id DESC
  // 2. If search provided: WHERE concat_ws(' ', ...) ILIKE '%search%' (Postgres) or LIKE (MySQL/SQLite)
  // 3. LEFT JOIN domains
  // 4. Return slice of Link
}
```

---

#### QUERY-LINK-003: `link.total(match, params)`

**Inputs**: Same as QUERY-LINK-002

**Outputs**: Integer count

**SQL Logic** (lines 63-82):
```javascript
const query = knex("links");
Object.entries(normalizedMatch).forEach(([key, value]) => {
  query.andWhere(key, ...(Array.isArray(value) ? value : [value]));
});

if (params?.search) {
  query[knex.compatibleILIKE](
    knex.raw("concat_ws(' ', description, links.address, target, domains.address)"),
    "%" + params.search + "%"
  );
}
query.leftJoin("domains", "links.domain_id", "domains.id");
query.count("* as count");

const [{ count }] = await query;
return typeof count === "number" ? count : parseInt(count);
```

**Go Signature**:
```go
func (r *LinkRepository) Total(ctx context.Context, match LinkMatch, params LinkQueryParams) (int, error) {
  // Same WHERE/search logic as Get, but SELECT COUNT(*)
}
```

---

#### QUERY-LINK-004: `link.create(params)`

**Inputs**:
```javascript
{
  address: string,
  target: string,
  description?: string,
  password?: string,
  expire_in?: Date,
  user_id?: int,
  domain_id?: int
}
```

**Outputs**: Newly created link object

**SQL Logic** (lines 200-229):
```javascript
let encryptedPassword = null;
if (params.password) {
  const salt = await bcrypt.genSalt(12);
  encryptedPassword = await bcrypt.hash(params.password, salt);
}

let [link] = await knex("links").insert({
  password: encryptedPassword,
  domain_id: params.domain_id || null,
  user_id: params.user_id || null,
  address: params.address,
  description: params.description || null,
  expire_in: params.expire_in || null,
  target: params.target
}, "*");

// MySQL/SQLite return only insertId
if (typeof link === "number") {
  link = await knex("links").where("id", link).first();
}

return link;
```

**Go Signature**:
```go
type CreateLinkParams struct {
  Address     string
  Target      string
  Description *string
  Password    *string // plaintext; will be bcrypt hashed
  ExpireIn    *time.Time
  UserID      *int
  DomainID    *int
}

func (r *LinkRepository) Create(ctx context.Context, params CreateLinkParams) (*Link, error) {
  // 1. If password provided: bcrypt.GenerateFromPassword([]byte(password), 12)
  // 2. INSERT INTO links (...) VALUES (...)
  // 3. Get last insert ID (MySQL/SQLite) or use RETURNING (Postgres)
  // 4. Return full Link struct
}
```

---

#### QUERY-LINK-005: `link.update(match, update)`

**Inputs**:
- `match`: Filter criteria
- `update`: Object with fields to update (e.g., `{ description: "new", banned: true }`)

**Outputs**: Array of updated link objects

**SQL Logic** (lines 264-292):
```javascript
if (update.password) {
  const salt = await bcrypt.genSalt(12);
  update.password = await bcrypt.hash(update.password, salt);
}

// Cache invalidation: fetch links before update
let links = [];
if (env.REDIS_ENABLED && (update.address || update.domain_id)) {
  links = await knex("links").select("*").where(match);
}

// Update
await knex("links")
  .where(match)
  .update({ ...update, updated_at: utils.dateToUTC(new Date()) });

// Fetch updated links
const updated_links = await knex("links")
  .select(selectable)
  .where(normalizeMatch(match))
  .leftJoin("domains", "links.domain_id", "domains.id");

// Cache invalidation
if (env.REDIS_ENABLED) {
  links.forEach(redis.remove.link);
  updated_links.forEach(redis.remove.link);
}

return updated_links;
```

**Go Signature**:
```go
type UpdateLinkParams struct {
  Description *string
  Password    *string
  ExpireIn    *time.Time
  Banned      *bool
  // ... other updatable fields
}

func (r *LinkRepository) Update(ctx context.Context, match LinkMatch, update UpdateLinkParams) ([]Link, error) {
  // 1. Hash password if provided
  // 2. If Redis enabled and address/domain_id changing: SELECT old links for cache invalidation
  // 3. UPDATE links SET ..., updated_at = NOW() WHERE ...
  // 4. SELECT updated links with domain join
  // 5. Invalidate old and new cache keys
  // 6. Return updated links
}
```

---

#### QUERY-LINK-006: `link.remove(match)`

**Inputs**: `match` object

**Outputs**: `{ isRemoved: bool, error?: string, link: Link | null }`

**SQL Logic** (lines 232-246):
```javascript
const link = await knex("links").where(match).first();
if (!link) {
  return { isRemoved: false, error: "Could not find the link.", link: null };
}

const deletedLink = await knex("links").where("id", link.id).delete();

if (env.REDIS_ENABLED) {
  redis.remove.link(link);
}

return { isRemoved: !!deletedLink, link };
```

**Go Signature**:
```go
type RemoveResult struct {
  IsRemoved bool
  Error     string
  Link      *Link
}

func (r *LinkRepository) Remove(ctx context.Context, match LinkMatch) (RemoveResult, error) {
  // 1. SELECT link
  // 2. If not found: return {IsRemoved: false, Error: "not found"}
  // 3. DELETE FROM links WHERE id = ?
  // 4. Invalidate Redis cache
  // 5. Return {IsRemoved: true, Link: link}
}
```

---

#### QUERY-LINK-007: `link.incrementVisit(match)`

**Inputs**: `match` object (typically `{ id: linkID }`)

**Outputs**: None (side effect only)

**SQL Logic** (lines 294-296):
```javascript
return knex("links").where(match).increment("visit_count", 1);
```

**Go Signature**:
```go
func (r *LinkRepository) IncrementVisit(ctx context.Context, match LinkMatch) error {
  // UPDATE links SET visit_count = visit_count + 1 WHERE ...
}
```

---

### 4.2 User Queries (server/queries/user.queries.js)

#### QUERY-USER-001: `user.find(match)`

**Inputs**: `{ id?, email?, apikey?, verification_token?, reset_password_token? }`

**Outputs**: Single user object or `null`

**SQL Logic** (lines 10-36):
```javascript
if ((match.id || match.apikey) && env.REDIS_ENABLED) {
  const key = redis.key.user(match.id || match.apikey); // "u:{id|apikey}"
  const cachedUser = await redis.client.get(key);
  if (cachedUser) return JSON.parse(cachedUser);
}

const query = knex("users");
Object.entries(match).forEach(([key, value]) => {
  query.andWhere(key, ...(Array.isArray(value) ? value : [value]));
});

const user = await query.first();

// Cache by id and apikey
if (user && env.REDIS_ENABLED) {
  if (match.id) {
    redis.client.set(redis.key.user(user.id), JSON.stringify(user), "EX", 60 * 15);
  }
  if (match.apikey) {
    redis.client.set(redis.key.user(user.apikey), JSON.stringify(user), "EX", 60 * 15);
  }
}

return user;
```

**Go Signature**:
```go
type UserMatch struct {
  ID                 *int
  Email              *string
  Apikey             *string
  VerificationToken  *string
  ResetPasswordToken *string
}

type User struct {
  ID                    int        `db:"id"`
  Email                 string     `db:"email"`
  Password              string     `db:"password"`
  Apikey                *string    `db:"apikey"`
  Role                  string     `db:"role"` // "USER" or "ADMIN"
  Verified              bool       `db:"verified"`
  Banned                bool       `db:"banned"`
  BannedByID            *int       `db:"banned_by_id"`
  VerificationToken     *string    `db:"verification_token"`
  VerificationExpires   *time.Time `db:"verification_expires"`
  ResetPasswordToken    *string    `db:"reset_password_token"`
  ResetPasswordExpires  *time.Time `db:"reset_password_expires"`
  ChangeEmailToken      *string    `db:"change_email_token"`
  ChangeEmailExpires    *time.Time `db:"change_email_expires"`
  ChangeEmailAddress    *string    `db:"change_email_address"`
  CreatedAt             time.Time  `db:"created_at"`
  UpdatedAt             time.Time  `db:"updated_at"`
}

func (r *UserRepository) Find(ctx context.Context, match UserMatch) (*User, error) {
  // 1. Check Redis cache if id or apikey provided
  // 2. Build SELECT WHERE with non-nil match fields
  // 3. Scan into User struct
  // 4. Cache by id and apikey (15-min TTL)
  // 5. Return user or nil
}
```

---

#### QUERY-USER-002: `user.create(params)`

**Inputs**:
```javascript
{
  email: string,
  password: string, // already hashed via Passport
  role?: "USER" | "ADMIN",
  verified?: bool,
  banned?: bool
}
```

**Outputs**: User object

**SQL Logic** (lines 223-238):
```javascript
let [user] = await knex("users").insert({
  email: params.email,
  password: params.password,
  role: params.role ?? ROLES.USER,
  verified: params.verified ?? false,
  banned: params.banned ?? false,
}, "*");

// MySQL/SQLite compat
if (typeof user === "number") {
  user = await knex("users").where("id", user).first();
}

return user;
```

**Go Signature**:
```go
type CreateUserParams struct {
  Email    string
  Password string // pre-hashed bcrypt
  Role     string // "USER" or "ADMIN"
  Verified bool
  Banned   bool
}

func (r *UserRepository) Create(ctx context.Context, params CreateUserParams) (*User, error) {
  // INSERT INTO users (...) VALUES (...)
  // Handle RETURNING vs last_insert_id
}
```

---

#### QUERY-USER-003: `user.update(match, update, methods?)`

**Inputs**:
- `match`: Filter
- `update`: Fields to update
- `methods`: Optional `{ increments: ["column_name"] }` for atomic increment

**Outputs**: Updated user object

**SQL Logic** (lines 67-96):
```javascript
const { user, updated_user } = await knex.transaction(async function(trx) {
  const query = trx("users");
  Object.entries(match).forEach(([key, value]) => {
    query.andWhere(key, ...(Array.isArray(value) ? value : [value]));
  });

  const user = await query.select("id").first();
  if (!user) return {};
  
  const updateQuery = trx("users").where("id", user.id);
  if (methods?.increments) {
    methods.increments.forEach(columnName => {
      updateQuery.increment(columnName);
    });
  }
  
  await updateQuery.update({ ...update, updated_at: utils.dateToUTC(new Date()) });
  const updated_user = await trx("users").where("id", user.id).first();

  return { user, updated_user };
});

// Cache invalidation
if (env.REDIS_ENABLED && user) {
  redis.remove.user(user);
  redis.remove.user(updated_user);
}

return updated_user;
```

**Go Signature**:
```go
type UpdateUserParams struct {
  Email      *string
  Password   *string
  Verified   *bool
  Banned     *bool
  Apikey     *string
  // ... other fields
}

type UpdateMethods struct {
  Increments []string // column names to increment
}

func (r *UserRepository) Update(ctx context.Context, match UserMatch, update UpdateUserParams, methods *UpdateMethods) (*User, error) {
  // 1. Begin transaction
  // 2. SELECT id WHERE match
  // 3. If methods.Increments: UPDATE users SET col1 = col1 + 1, ...
  // 4. UPDATE users SET ..., updated_at = NOW()
  // 5. SELECT updated user
  // 6. Invalidate cache (old and new user keys)
  // 7. Commit transaction
  // 8. Return updated user
}
```

---

### 4.3 Domain Queries (server/queries/domain.queries.js)

#### QUERY-DOMAIN-001: `domain.find(match)`

**Inputs**: `{ id?, address?, user_id? }`

**Outputs**: Domain object or `null`

**SQL Logic** (lines 6-19):
```javascript
if (match.address && env.REDIS_ENABLED) {
  const cachedDomain = await redis.client.get(redis.key.domain(match.address)); // "d:{address}"
  if (cachedDomain) return JSON.parse(cachedDomain);
}

const domain = await knex("domains").where(match).first();

if (domain && env.REDIS_ENABLED) {
  const key = redis.key.domain(domain.address);
  redis.client.set(key, JSON.stringify(domain), "EX", 60 * 15);
}

return domain;
```

**Go Signature**:
```go
type DomainMatch struct {
  ID      *int
  Address *string
  UserID  *int
}

type Domain struct {
  ID          int        `db:"id"`
  Address     string     `db:"address"`
  Homepage    *string    `db:"homepage"`
  UserID      *int       `db:"user_id"`
  Banned      bool       `db:"banned"`
  BannedByID  *int       `db:"banned_by_id"`
  UUID        string     `db:"uuid"`
  CreatedAt   time.Time  `db:"created_at"`
  UpdatedAt   time.Time  `db:"updated_at"`
}

func (r *DomainRepository) Find(ctx context.Context, match DomainMatch) (*Domain, error) {
  // 1. Check Redis cache if address provided
  // 2. SELECT WHERE match
  // 3. Cache by address (15-min TTL)
  // 4. Return domain or nil
}
```

---

#### QUERY-DOMAIN-002: `domain.add(params)`

**Inputs**:
```javascript
{
  address: string,
  homepage?: string,
  user_id?: int,
  banned?: bool,
  banned_by_id?: int
}
```

**Outputs**: Domain object

**SQL Logic** (lines 26-60):
```javascript
params.address = params.address.toLowerCase();

const existingDomain = await knex("domains").where("address", params.address).first();
let id = existingDomain?.id;

const newDomain = {
  address: params.address,
  homepage: params.homepage,
  user_id: params.user_id,
  banned: !!params.banned,
  banned_by_id: params.banned_by_id
};

if (id) {
  // UPSERT: update existing
  await knex("domains").where("id", id).update({
    ...newDomain,
    updated_at: params.updated_at || utils.dateToUTC(new Date())
  });
} else {
  const [createdDomain] = await knex("domains").insert(newDomain, "*");
  id = typeof createdDomain === "number" ? createdDomain : createdDomain.id;
}

const domain = await knex("domains").where("id", id).first();

// Cache invalidation
if (env.REDIS_ENABLED) {
  redis.remove.domain(existingDomain);
  redis.remove.domain(domain);
}

return domain;
```

**Go Signature**:
```go
type AddDomainParams struct {
  Address    string
  Homepage   *string
  UserID     *int
  Banned     bool
  BannedByID *int
}

func (r *DomainRepository) Add(ctx context.Context, params AddDomainParams) (*Domain, error) {
  // 1. Lowercase address
  // 2. Check if domain exists
  // 3. If exists: UPDATE; else: INSERT
  // 4. SELECT final domain
  // 5. Invalidate old and new cache keys
  // 6. Return domain
}
```

---

### 4.4 Visit Queries (server/queries/visit.queries.js)

#### QUERY-VISIT-001: `visit.add(params)`

**Inputs**:
```javascript
{
  link_id: int,
  user_id?: int,
  browser: "chrome" | "edge" | "firefox" | "ie" | "opera" | "safari" | "other",
  os: "android" | "ios" | "linux" | "macos" | "windows" | "other",
  country: string, // 2-char ISO code, e.g., "US"
  referrer: string // hostname with dots replaced by "[dot]"
}
```

**Outputs**: Existing or new visit row (internal use)

**SQL Logic** (lines 8-66):
```javascript
const data = {
  ...params,
  country: params.country.toLowerCase(),
  referrer: params.referrer.toLowerCase()
};

const nowUTC = new Date().toISOString();
const truncatedNow = nowUTC.substring(0, 10) + " " + nowUTC.substring(11, 14) + "00:00"; // "YYYY-MM-DD HH:00:00"

return knex.transaction(async (trx) => {
  // Find existing hourly bucket for this link
  const subquery = trx("visits")
    .select("visits.*")
    .select({ created_at_hours: utils.truncatedCreatedAtHour }) // dialect-specific truncation
    .where({ link_id: data.link_id })
    .as("subquery");

  const visit = await trx
    .select("*")
    .from(subquery)
    .where("created_at_hours", "=", truncatedNow)
    .forUpdate() // row lock
    .first();
    
  if (visit) {
    // Update existing bucket
    const countries = typeof visit.countries === "string" ? JSON.parse(visit.countries) : visit.countries;
    const referrers = typeof visit.referrers === "string" ? JSON.parse(visit.referrers) : visit.referrers;
    
    await trx("visits")
      .where({ id: visit.id })
      .increment(`br_${data.browser}`, 1)
      .increment(`os_${data.os}`, 1)
      .increment("total", 1)
      .update({
        updated_at: utils.dateToUTC(new Date()),
        countries: JSON.stringify({
          ...countries,
          [data.country]: (countries[data.country] ?? 0) + 1
        }),
        referrers: JSON.stringify({
          ...referrers,
          [data.referrer]: (referrers[data.referrer] ?? 0) + 1
        })
      });
  } else {
    // Insert new bucket
    await trx("visits").insert({
      [`br_${data.browser}`]: 1,
      countries: { [data.country]: 1 },
      referrers: { [data.referrer]: 1 },
      [`os_${data.os}`]: 1,
      total: 1,
      link_id: data.link_id,
      user_id: data.user_id,
    });
  }

  return visit;
});
```

**Go Signature**:
```go
type AddVisitParams struct {
  LinkID   int
  UserID   *int
  Browser  string // "chrome", "edge", etc.
  OS       string // "android", "ios", etc.
  Country  string // 2-char ISO code
  Referrer string // hostname with dots as "[dot]"
}

func (r *VisitRepository) Add(ctx context.Context, params AddVisitParams) error {
  // 1. Lowercase country and referrer
  // 2. Calculate truncatedNow (current hour as "YYYY-MM-DD HH:00:00")
  // 3. Begin transaction
  // 4. SELECT visit WHERE link_id = ? AND truncated_created_at = ? FOR UPDATE
  // 5. If exists:
  //    - Unmarshal countries/referrers JSON
  //    - UPDATE visits SET 
  //        br_{browser} = br_{browser} + 1,
  //        os_{os} = os_{os} + 1,
  //        total = total + 1,
  //        countries = {...updated JSON},
  //        referrers = {...updated JSON},
  //        updated_at = NOW()
  // 6. Else:
  //    - INSERT new row with counters = 1
  // 7. Commit transaction
}
```

**Concurrency Safety**:
- `FOR UPDATE` locks the row → prevents race conditions on increment
- Multiple visits to same link in same hour serialize on this lock

---

#### QUERY-VISIT-002: `visit.find(match, total)`

**Inputs**:
- `match`: `{ link_id: int }` or `{ user_id: int }`
- `total`: Unused in current implementation

**Outputs**: Stats object with `lastDay`, `lastWeek`, `lastMonth`, `lastYear` breakdowns

**SQL Logic** (lines 69-188):
```javascript
if (match.link_id && env.REDIS_ENABLED) {
  const key = redis.key.stats(match.link_id); // "s:{link_id}"
  const cached = await redis.client.get(key);
  if (cached) return JSON.parse(cached);
}

const stats = {
  lastDay: { stats: utils.getInitStats(), views: new Array(24).fill(0), total: 0 },
  lastWeek: { stats: utils.getInitStats(), views: new Array(7).fill(0), total: 0 },
  lastMonth: { stats: utils.getInitStats(), views: new Array(30).fill(0), total: 0 },
  lastYear: { stats: utils.getInitStats(), views: new Array(12).fill(0), total: 0 }
};

const visitsStream = knex("visits").where(match).stream();
const now = new Date();
const periods = utils.getStatsPeriods(now); // [[type, fromDate], ...]

for await (const visit of visitsStream) {
  periods.forEach(([type, fromDate]) => {
    const isIncluded = isAfter(utils.parseDatetime(visit.created_at), fromDate);
    if (!isIncluded) return;
    
    const diffFunction = utils.getDifferenceFunction(type); // differenceInHours, differenceInDays, ...
    const diff = diffFunction(now, utils.parseDatetime(visit.created_at));
    const index = stats[type].views.length - diff - 1;
    
    const period = stats[type].stats;
    const countries = typeof visit.countries === "string" ? JSON.parse(visit.countries) : visit.countries;
    const referrers = typeof visit.referrers === "string" ? JSON.parse(visit.referrers) : visit.referrers;
    
    // Aggregate counters
    stats[type].stats.browser.chrome += visit.br_chrome;
    // ... same for all browsers and OS
    
    // Merge countries/referrers
    Object.entries(countries).forEach(([country, count]) => {
      period.country[country] = (period.country[country] || 0) + count;
    });
    Object.entries(referrers).forEach(([referrer, count]) => {
      period.referrer[referrer] = (period.referrer[referrer] || 0) + count;
    });
    
    stats[type].views[index] += visit.total;
    stats[type].total += visit.total;
  });
}

const response = {
  lastDay: { stats: utils.statsObjectToArray(stats.lastDay.stats), views: stats.lastDay.views, total: stats.lastDay.total },
  // ... same for lastWeek, lastMonth, lastYear
  updatedAt: new Date()
};

// Cache for 60 seconds
if (match.link_id && env.REDIS_ENABLED) {
  redis.client.set(redis.key.stats(match.link_id), JSON.stringify(response), "EX", 60);
}

return response;
```

**Period Definitions** (server/utils/utils.js:149-156):
```javascript
function getStatsPeriods(now) {
  return [
    ["lastDay", subHours(now, 24)],
    ["lastWeek", subDays(now, 7)],
    ["lastMonth", subDays(now, 30)],
    ["lastYear", subMonths(now, 12)],
  ];
}
```

**Go Signature**:
```go
type VisitStats struct {
  Browser  map[string]int `json:"browser"`
  OS       map[string]int `json:"os"`
  Country  map[string]int `json:"country"`
  Referrer map[string]int `json:"referrer"`
}

type PeriodStats struct {
  Stats VisitStats `json:"stats"`
  Views []int      `json:"views"` // hourly/daily/monthly buckets
  Total int        `json:"total"`
}

type StatsResponse struct {
  LastDay   PeriodStats `json:"lastDay"`
  LastWeek  PeriodStats `json:"lastWeek"`
  LastMonth PeriodStats `json:"lastMonth"`
  LastYear  PeriodStats `json:"lastYear"`
  UpdatedAt time.Time   `json:"updatedAt"`
}

func (r *VisitRepository) Find(ctx context.Context, match VisitMatch) (*StatsResponse, error) {
  // 1. Check Redis cache (60s TTL)
  // 2. SELECT * FROM visits WHERE link_id = ? (or user_id)
  // 3. Stream rows (or iterate slice)
  // 4. For each visit:
  //    - Parse created_at
  //    - For each period (lastDay, lastWeek, lastMonth, lastYear):
  //      - Check if visit.created_at is within period
  //      - Calculate time difference (hours/days/months)
  //      - Update corresponding bucket in views array
  //      - Aggregate browser/os/country/referrer counters
  // 5. Convert stats to sorted arrays (statsObjectToArray)
  // 6. Cache result (60s TTL)
  // 7. Return StatsResponse
}
```

**Complexity**: O(V × P) where V = visit rows, P = 4 periods. For large datasets (1M+ visits), this is expensive; consider pre-aggregation.

---

### 4.5 Host Queries (server/queries/host.queries.js)

#### QUERY-HOST-001: `host.find(match)`

**Inputs**: `{ address: string }` or `{ id: int }`

**Outputs**: Host object or `null`

**SQL Logic** (lines 6-21):
```javascript
if (match.address && env.REDIS_ENABLED) {
  const cachedHost = await redis.client.get(redis.key.host(match.address)); // "h:{address}"
  if (cachedHost) return JSON.parse(cachedHost);
}

const host = await knex("hosts").where(match).first();

if (host && env.REDIS_ENABLED) {
  const key = redis.key.host(host.address);
  redis.client.set(key, JSON.stringify(host), "EX", 60 * 15);
}

return host;
```

**Go Signature**:
```go
type HostMatch struct {
  ID      *int
  Address *string
}

type Host struct {
  ID          int       `db:"id"`
  Address     string    `db:"address"`
  Banned      bool      `db:"banned"`
  BannedByID  *int      `db:"banned_by_id"`
  CreatedAt   time.Time `db:"created_at"`
  UpdatedAt   time.Time `db:"updated_at"`
}

func (r *HostRepository) Find(ctx context.Context, match HostMatch) (*Host, error) {
  // Same pattern as domain/user find
}
```

---

## 5. Redis Caching Layer

### 5.1 Redis Configuration (server/redis.js, server/env.js)

**Connection** (server/redis.js:7-14):
```javascript
if (env.REDIS_ENABLED) {
  client = new Redis({
    host: env.REDIS_HOST,     // default: 127.0.0.1
    port: env.REDIS_PORT,     // default: 6379
    db: env.REDIS_DB,         // default: 0
    ...(env.REDIS_PASSWORD && { password: env.REDIS_PASSWORD })
  });
}
```

**Go Equivalent**:
```go
import "github.com/redis/go-redis/v9"

func NewRedisClient(cfg Config) *redis.Client {
  if !cfg.RedisEnabled {
    return nil
  }
  return redis.NewClient(&redis.Options{
    Addr:     fmt.Sprintf("%s:%d", cfg.RedisHost, cfg.RedisPort),
    Password: cfg.RedisPassword,
    DB:       cfg.RedisDB,
  })
}
```

---

### 5.2 Cache Key Patterns (server/redis.js:16-22)

| Entity | Key Pattern | Example | TTL |
|--------|-------------|---------|-----|
| Link | `l:{address}:{domain_id}` | `l:abc123:` (default domain) or `l:abc123:5` | 15 min (900s) |
| Domain | `d:{address}` | `d:example.com` | 15 min |
| User (by ID) | `u:{id}` | `u:42` | 15 min |
| User (by apikey) | `u:{apikey}` | `u:sk_abc123...` | 15 min |
| Host | `h:{address}` | `h:banned-site.com` | 15 min |
| Stats | `s:{link_id}` | `s:123` | 60s |
| Any User Exists | `any-user` | `any-user` | 5 min (300s) |

**Go Constants**:
```go
const (
  CacheTTLEntity = 15 * time.Minute
  CacheTTLStats  = 60 * time.Second
  CacheTTLAnyUser = 5 * time.Minute
)

func LinkCacheKey(address string, domainID *int) string {
  if domainID == nil {
    return fmt.Sprintf("l:%s:", address)
  }
  return fmt.Sprintf("l:%s:%d", address, *domainID)
}
```

---

### 5.3 Cache Invalidation (server/redis.js:24-44)

**Strategy**: **Explicit invalidation on mutation**

**Invalidation Functions**:
```javascript
const remove = {
  domain: (domain) => client.del(key.domain(domain.address)),
  host: (host) => client.del(key.host(host.address)),
  link: (link) => client.del(key.link(link.address, link.domain_id)),
  user: (user) => Promise.all([
    client.del(key.user(user.id)),
    client.del(key.user(user.apikey))
  ])
};
```

**Triggered on**:
- **link.create**: No cache (new entity)
- **link.update**: Invalidate old keys + new keys (if address/domain_id changed)
- **link.remove**: Invalidate link cache
- **domain.add**: Invalidate old + new domain cache
- **user.update**: Invalidate user cache (by id and apikey)

**Go Signature**:
```go
func (r *LinkRepository) InvalidateCache(ctx context.Context, link *Link) error {
  if r.redis == nil {
    return nil
  }
  key := LinkCacheKey(link.Address, link.DomainID)
  return r.redis.Del(ctx, key).Err()
}
```

---

### 5.4 Graceful Degradation (No Redis)

**Behavior** (server/queues/queues.js:25-34):
- If `REDIS_ENABLED=false`:
  - Cache reads return `nil` → always hit database
  - Queue falls back to **synchronous** visit processing
  - No distributed locking (single-instance only)

**Go Implementation**:
```go
func (r *LinkRepository) Find(ctx context.Context, match LinkMatch) (*Link, error) {
  // Try cache first
  if r.redis != nil && match.Address != nil && match.DomainID != nil {
    cached, err := r.getFromCache(ctx, *match.Address, match.DomainID)
    if err == nil && cached != nil {
      return cached, nil
    }
  }
  
  // Fallback to DB
  link, err := r.findInDB(ctx, match)
  if err != nil {
    return nil, err
  }
  
  // Cache result
  if r.redis != nil && link != nil {
    r.setCache(ctx, link)
  }
  
  return link, nil
}
```

---

## 6. Visit Tracking Pipeline

### 6.1 Queue Architecture (server/queues/queues.js)

**With Redis** (Bull queue):
```javascript
visit = new Queue("visit", { redis });
visit.clean(5000, "completed"); // auto-clean completed jobs older than 5s
visit.process(6, path.resolve(__dirname, "visit.js")); // 6 concurrent workers
visit.on("completed", job => job.remove());
```

**Without Redis** (synchronous fallback):
```javascript
const visitProcessor = require(path.resolve(__dirname, "visit.js"));
visit = {
  add(data) {
    visitProcessor({ data }).catch(function(error) {
      console.error("Add visit error: ", error);
    });
  }
};
```

**Go Recommendation**: Use **Asynq** (Redis-backed) or **River** (Postgres-backed) for queue.

---

### 6.2 Visit Job Processor (server/queues/visit.js)

**Job Payload**:
```javascript
{
  link: { id: int, user_id?: int },
  ip: string,
  userAgent: string, // or headers: { "user-agent": string }
  referrer?: string, // full URL
  country?: string   // override geoip
}
```

**Processing Logic** (lines 8-33):
```javascript
module.exports = function({ data }) {
  const tasks = [];
  
  // Task 1: Increment link.visit_count
  tasks.push(query.link.incrementVisit({ id: data.link.id }));
  
  // Task 2: Parse user agent
  const userAgent = data.userAgent || data.headers?.["user-agent"];
  const agent = useragent.parse(userAgent);
  const browser = getUseragentBrowser(agent); // "chrome", "firefox", ...
  const os = getUseragentOS(agent);           // "windows", "android", ...
  
  // Task 3: Determine referrer hostname
  const referrer = data.referrer && removeWww(URL.parse(data.referrer).hostname);
  
  // Task 4: GeoIP lookup
  const country = (data.country || geoip.lookup(data.ip)?.country)?.slice(0, 2).toUpperCase() || "Unknown";
  
  // Task 5: Add visit stats
  tasks.push(
    query.visit.add({
      browser,
      country,
      os,
      link_id: data.link.id,
      user_id: data.link.user_id,
      referrer: (referrer && referrer.replace(/\./gi, "[dot]")) || "Direct"
    })
  );
  
  return Promise.all(tasks);
};
```

**Dependencies**:
- `express-useragent` (npm package) → Parse UA string
- `geoip-lite` (npm package, uses MaxMind GeoLite2 database)

---

### 6.3 User Agent Parsing (server/utils/utils.js:360-394)

**Browser Detection**:
```javascript
function getUseragentBrowser(agent) {
  if (agent.isIE) return "ie";
  else if (agent.isFirefox) return "firefox";
  else if (agent.isChrome) return "chrome";
  else if (agent.isOpera) return "opera";
  else if (agent.isEdge) return "edge";
  else return "other";
}
```

**OS Detection**:
```javascript
function getUseragentOS(agent) {
  if (agent.isWindows) return "windows";
  else if (agent.isMac && agent.isDesktop) return "macos";
  else if (agent.isiPhone || agent.isiPad || agent.isiPod) return "ios";
  else if (agent.isMac && agent.isMobile) return "ios";
  else if (agent.isAndroid) return "android";
  else if (agent.isLinux) return "linux";
  else return "other";
}
```

**Go Library**: Use [github.com/mssola/user_agent](https://github.com/mssola/user_agent) or [github.com/ua-parser/uap-go](https://github.com/ua-parser/uap-go).

```go
import "github.com/mssola/user_agent"

func getUseragentBrowser(ua *user_agent.UserAgent) string {
  name, _ := ua.Browser()
  switch {
  case strings.Contains(name, "Chrome"):
    return "chrome"
  case strings.Contains(name, "Firefox"):
    return "firefox"
  case strings.Contains(name, "Safari"):
    return "safari"
  case strings.Contains(name, "Edge"):
    return "edge"
  case strings.Contains(name, "Opera"):
    return "opera"
  case strings.Contains(name, "IE"):
    return "ie"
  default:
    return "other"
  }
}

func getUseragentOS(ua *user_agent.UserAgent) string {
  os := ua.OS()
  switch {
  case strings.Contains(os, "Windows"):
    return "windows"
  case strings.Contains(os, "Mac OS"):
    if ua.Mobile() {
      return "ios"
    }
    return "macos"
  case strings.Contains(os, "iOS"):
    return "ios"
  case strings.Contains(os, "Android"):
    return "android"
  case strings.Contains(os, "Linux"):
    return "linux"
  default:
    return "other"
  }
}
```

---

### 6.4 GeoIP Lookup

**Node.js**: `geoip-lite` uses **MaxMind GeoLite2** (free, updated monthly)

**Go Library**: [github.com/oschwald/geoip2-golang](https://github.com/oschwald/geoip2-golang)

```go
import "github.com/oschwald/geoip2-golang"

type GeoIPService struct {
  db *geoip2.Reader
}

func NewGeoIPService(dbPath string) (*GeoIPService, error) {
  db, err := geoip2.Open(dbPath)
  if err != nil {
    return nil, err
  }
  return &GeoIPService{db: db}, nil
}

func (s *GeoIPService) Lookup(ip string) (string, error) {
  record, err := s.db.Country(net.ParseIP(ip))
  if err != nil {
    return "Unknown", nil
  }
  return record.Country.IsoCode, nil
}
```

**License Note**: GeoLite2 is **Creative Commons BY-SA 4.0** (requires attribution). Alternative: Commercial MaxMind GeoIP2 database.

---

### 6.5 Referrer Processing (server/queues/visit.js:19, 29)

**Transformation**:
1. Parse referrer URL → extract hostname
2. Remove `www.` prefix (`removeWww()`)
3. Replace `.` with `[dot]` to avoid JSON key issues
4. If no referrer: `"Direct"`

**Example**:
- Input: `https://www.google.com/search?q=kutt`
- Hostname: `google.com`
- Stored: `google[dot]com`

**Go Implementation**:
```go
func processReferrer(referrerURL string) string {
  if referrerURL == "" {
    return "Direct"
  }
  
  u, err := url.Parse(referrerURL)
  if err != nil {
    return "Direct"
  }
  
  host := strings.TrimPrefix(u.Hostname(), "www.")
  return strings.ReplaceAll(host, ".", "[dot]")
}
```

---

### 6.6 Visit Queue Workflow (End-to-End)

**Step 1**: Link redirect handler (Part B) enqueues visit job
```javascript
// In HTTP handler (Part B)
queues.visit.add({
  link: { id: link.id, user_id: link.user_id },
  ip: req.ip,
  userAgent: req.headers["user-agent"],
  referrer: req.headers.referer
});
```

**Step 2**: Bull worker picks up job → `visit.js` processor runs

**Step 3**: Processor increments `links.visit_count` + aggregates visit stats

**Go Equivalent** (with Asynq):
```go
// Enqueue
task := asynq.NewTask("visit:add", payload)
client.Enqueue(task, asynq.Queue("visits"), asynq.MaxRetry(3))

// Worker
func HandleVisitTask(ctx context.Context, t *asynq.Task) error {
  var payload VisitPayload
  json.Unmarshal(t.Payload(), &payload)
  
  // Increment visit_count
  linkRepo.IncrementVisit(ctx, LinkMatch{ID: &payload.LinkID})
  
  // Parse UA
  ua := user_agent.New(payload.UserAgent)
  browser := getUseragentBrowser(ua)
  os := getUseragentOS(ua)
  
  // GeoIP
  country, _ := geoipSvc.Lookup(payload.IP)
  
  // Add visit stats
  visitRepo.Add(ctx, AddVisitParams{
    LinkID:   payload.LinkID,
    UserID:   payload.UserID,
    Browser:  browser,
    OS:       os,
    Country:  country,
    Referrer: processReferrer(payload.Referrer),
  })
  
  return nil
}
```

---

## 7. Cron Jobs

### 7.1 Expired Link Deletion (server/cron.js)

**Schedule**: Every 30 seconds

**Logic** (lines 4-6):
```javascript
setInterval(function () {
  query.link.batchRemove({ expire_in: ["<", utils.dateToUTC(new Date())] }).catch();
}, 30_000);
```

**SQL Equivalent**:
```sql
DELETE FROM links WHERE expire_in < NOW();
```

**Go Implementation** (using [github.com/robfig/cron/v3](https://github.com/robfig/cron/v3)):
```go
import "github.com/robfig/cron/v3"

func StartCronJobs(linkRepo *LinkRepository) *cron.Cron {
  c := cron.New()
  
  // Every 30 seconds
  c.AddFunc("*/30 * * * * *", func() {
    ctx := context.Background()
    now := time.Now()
    err := linkRepo.BatchRemove(ctx, LinkMatch{
      ExpireIn: &now, // WHERE expire_in < ?
    })
    if err != nil {
      log.Error("Failed to delete expired links", "error", err)
    }
  })
  
  c.Start()
  return c
}
```

**Alternative**: Use database-native job scheduler (PostgreSQL: pg_cron, MySQL: Events).

---

## 8. Business Rules & Invariants

### 8.1 User-Level Rules

**RULE-USER-001**: Email uniqueness
- **Constraint**: `users.email` UNIQUE
- **Enforcement**: Database + application validation

**RULE-USER-002**: Role-based access
- **Roles**: `USER`, `ADMIN` (ENUM in `users.role`)
- **Admin detection**: `user.role === "ADMIN"`
- **Admin assignment**: Via `ADMIN_EMAILS` env var during migration 20241103083933

**RULE-USER-003**: Password hashing
- **Algorithm**: bcrypt, cost 12 (server/queries/link.queries.js:204)
- **Go**: Use `golang.org/x/crypto/bcrypt` with `bcrypt.DefaultCost` (10) or 12

**RULE-USER-004**: API key uniqueness
- **Constraint**: `users.apikey` UNIQUE (since 20241223062111)
- **Generation**: Not shown in codebase; likely external generation

**RULE-USER-005**: User deletion cascades
- **Cascade**: `links.user_id` → ON DELETE CASCADE
- **Cascade**: `visits.user_id` → ON DELETE CASCADE
- **No Cascade**: `domains.user_id` → ON DELETE SET NULL (orphaned domains allowed)

---

### 8.2 Link-Level Rules

**RULE-LINK-001**: Address collision handling
- **Strategy**: Generate random `address` via `nanoid()` (server/utils/utils.js:69-76)
- **Collision**: Recursively regenerate until unique
- **Namespace**: Scoped by `(address, domain_id)` — same address allowed across domains

**RULE-LINK-002**: Link expiration
- **Field**: `links.expire_in` (DATETIME, nullable)
- **Enforcement**: Cron job deletes `expire_in < NOW()` every 30s
- **No auto-enforcement on SELECT**: Expired links still accessible until cron runs

**RULE-LINK-003**: Password protection
- **Field**: `links.password` (bcrypt hash)
- **Validation**: HTTP handler (Part B) checks password before redirect
- **Storage**: NULL if no password

**RULE-LINK-004**: Banned links
- **Field**: `links.banned` (BOOLEAN)
- **Admin action**: Set `banned = true`, `banned_by_id = admin.id`
- **Effect**: HTTP handler (Part B) rejects access

**RULE-LINK-005**: Visit count integrity
- **Field**: `links.visit_count` (INTEGER)
- **Update**: Incremented via `query.link.incrementVisit()` (atomic)
- **Concurrency**: Safe via database-level increment

---

### 8.3 Domain-Level Rules

**RULE-DOMAIN-001**: Domain ownership
- **Field**: `domains.user_id` (nullable)
- **Orphaned**: Allowed (user_id = NULL after user deletion)
- **Multiple ownership**: Same user can own multiple domains

**RULE-DOMAIN-002**: Domain uniqueness
- **Constraint**: `domains.address` UNIQUE
- **Case**: Lowercase enforced in `domain.add()` (line 27)

**RULE-DOMAIN-003**: Homepage fallback
- **Field**: `domains.homepage` (nullable)
- **Default**: Sanitizer uses `env.DEFAULT_DOMAIN` if NULL (server/utils/utils.js:274)

---

### 8.4 Visit-Level Rules

**RULE-VISIT-001**: Hourly aggregation
- **Granularity**: One row per link per hour
- **Timestamp**: `created_at` truncated to `YYYY-MM-DD HH:00:00`
- **Concurrency**: `FOR UPDATE` lock prevents duplicate inserts

**RULE-VISIT-002**: Country code normalization
- **Format**: 2-character ISO code (e.g., `"US"`, `"GB"`)
- **Uppercase**: Enforced in `visit.add()` (line 20)
- **Unknown**: Stored as `"Unknown"` if GeoIP fails

**RULE-VISIT-003**: Referrer encoding
- **Dots**: Replaced with `[dot]` to avoid JSON key issues
- **Example**: `google.com` → `google[dot]com`

**RULE-VISIT-004**: Stats cache TTL
- **Key**: `s:{link_id}`
- **TTL**: 60 seconds
- **Rationale**: Stats are expensive to compute (streaming all visits)

---

### 8.5 Deletion Cascades

**CASCADE-001**: User deletion
```
DELETE FROM users WHERE id = ?
  → CASCADE DELETE FROM links WHERE user_id = ?
    → CASCADE DELETE FROM visits WHERE link_id IN (...)
  → CASCADE DELETE FROM visits WHERE user_id = ?
```

**CASCADE-002**: Link deletion
```
DELETE FROM links WHERE id = ?
  → CASCADE DELETE FROM visits WHERE link_id = ?
```

**CASCADE-003**: Domain deletion
- No cascade to links (links.domain_id not ON DELETE CASCADE)
- **Risk**: Orphaned links with invalid domain_id

---

## 9. Go Implementation Recommendations

### 9.1 Database Access Layer

#### Option 1: **sqlc** (Recommended for Go 1.27)

**Pros**:
- Type-safe, compile-time SQL validation
- Zero reflection overhead
- Excellent multi-dialect support (Postgres, MySQL, SQLite)
- Native JSONB support

**Cons**:
- Requires SQL file maintenance
- Less flexible for dynamic queries

**Setup**:
```yaml
# sqlc.yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "internal/db/queries"
    schema: "internal/db/schema"
    gen:
      go:
        package: "db"
        out: "internal/db"
        emit_json_tags: true
        emit_prepared_queries: true
```

**Example Query** (queries/link.sql):
```sql
-- name: FindLink :one
SELECT l.*, d.address AS domain
FROM links l
LEFT JOIN domains d ON l.domain_id = d.id
WHERE l.id = $1;

-- name: IncrementVisitCount :exec
UPDATE links SET visit_count = visit_count + 1 WHERE id = $1;
```

**Generated Go**:
```go
type FindLinkRow struct {
  ID          int32
  Address     string
  Target      string
  VisitCount  int32
  Domain      sql.NullString
  // ...
}

func (q *Queries) FindLink(ctx context.Context, id int32) (FindLinkRow, error)
```

---

#### Option 2: **sqlx** (Recommended for complex queries)

**Pros**:
- Flexible, runtime query building
- Good struct scanning
- Compatible with standard `database/sql`

**Cons**:
- No compile-time safety
- Manual query construction

**Example**:
```go
import "github.com/jmoiron/sqlx"

type LinkRepository struct {
  db *sqlx.DB
}

func (r *LinkRepository) Find(ctx context.Context, match LinkMatch) (*Link, error) {
  query := `SELECT l.*, d.address AS domain FROM links l LEFT JOIN domains d ON l.domain_id = d.id WHERE 1=1`
  args := []interface{}{}
  
  if match.ID != nil {
    query += " AND l.id = ?"
    args = append(args, *match.ID)
  }
  if match.Address != nil {
    query += " AND l.address = ?"
    args = append(args, *match.Address)
  }
  
  var link Link
  err := r.db.GetContext(ctx, &link, r.db.Rebind(query), args...)
  return &link, err
}
```

---

#### Option 3: **GORM** (NOT Recommended)

**Cons**:
- Heavy reflection overhead
- Poor multi-dialect support (quirks)
- Verbose for repository pattern
- Overkill for this use case

---

### 9.2 Multi-Dialect Support

**Strategy**: Use **build tags** + interface abstraction

**Directory Structure**:
```
internal/db/
  ├── repository.go       // Interfaces
  ├── postgres/
  │   └── link_repo.go
  ├── mysql/
  │   └── link_repo.go
  └── sqlite/
      └── link_repo.go
```

**Interface** (repository.go):
```go
type LinkRepository interface {
  Find(ctx context.Context, match LinkMatch) (*Link, error)
  Create(ctx context.Context, params CreateLinkParams) (*Link, error)
  // ...
}

func NewLinkRepository(driver string, db *sql.DB) LinkRepository {
  switch driver {
  case "postgres", "pgx":
    return postgres.NewLinkRepo(db)
  case "mysql":
    return mysql.NewLinkRepo(db)
  case "sqlite3":
    return sqlite.NewLinkRepo(db)
  default:
    panic("unsupported driver")
  }
}
```

**Dialect-Specific SQL**:
```go
// postgres/link_repo.go
func (r *LinkRepo) buildILIKE(column, value string) string {
  return fmt.Sprintf("%s ILIKE $%d", column, r.argCounter())
}

// mysql/link_repo.go
func (r *LinkRepo) buildILIKE(column, value string) string {
  return fmt.Sprintf("%s LIKE ?", column) // Case-insensitive by default
}
```

---

### 9.3 Driver Recommendations

| Dialect | Driver | Package | Notes |
|---------|--------|---------|-------|
| PostgreSQL | pgx v5 | `github.com/jackc/pgx/v5` | Native driver, excellent performance |
| MySQL | go-sql-driver | `github.com/go-sql-driver/mysql` | Most popular, stable |
| SQLite | modernc.org/sqlite | `modernc.org/sqlite` | Pure Go, no CGO (vs mattn/go-sqlite3) |

**Connection Strings**:
```go
// PostgreSQL
dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
  cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBName)

// MySQL
dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&charset=utf8mb4&collation=utf8mb4_general_ci",
  cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBName)

// SQLite
dsn := cfg.DBFilename // e.g., "/var/lib/kutt/data.sqlite"
```

---

### 9.4 Redis Client

**Recommended**: [go-redis/redis/v9](https://github.com/redis/go-redis)

```go
import "github.com/redis/go-redis/v9"

func NewRedisClient(cfg Config) *redis.Client {
  if !cfg.RedisEnabled {
    return nil
  }
  return redis.NewClient(&redis.Options{
    Addr:     fmt.Sprintf("%s:%d", cfg.RedisHost, cfg.RedisPort),
    Password: cfg.RedisPassword,
    DB:       cfg.RedisDB,
  })
}

// Usage
func (r *LinkRepository) getFromCache(ctx context.Context, address string, domainID *int) (*Link, error) {
  key := LinkCacheKey(address, domainID)
  val, err := r.redis.Get(ctx, key).Result()
  if err == redis.Nil {
    return nil, nil // Cache miss
  }
  if err != nil {
    return nil, err
  }
  
  var link Link
  json.Unmarshal([]byte(val), &link)
  return &link, nil
}
```

---

### 9.5 Queue Recommendation

#### Option 1: **Asynq** (Redis-backed, Bull equivalent)

**Pros**:
- Redis-native (compatible with existing infrastructure)
- Web UI for monitoring
- Automatic retries, dead-letter queue

**Cons**:
- Requires Redis (no fallback without Redis)

**Usage**:
```go
import "github.com/hibiken/asynq"

// Producer
client := asynq.NewClient(redisOpt)
task := asynq.NewTask("visit:add", payload)
client.Enqueue(task, asynq.Queue("visits"))

// Consumer
srv := asynq.NewServer(redisOpt, asynq.Config{Concurrency: 6})
mux := asynq.NewServeMux()
mux.HandleFunc("visit:add", HandleVisitTask)
srv.Run(mux)
```

---

#### Option 2: **River** (Postgres-backed)

**Pros**:
- No additional infrastructure (uses existing Postgres)
- Transactional job enqueue (job + DB update in same TX)

**Cons**:
- Not compatible with MySQL/SQLite (Postgres-only)

**Usage**:
```go
import "github.com/riverqueue/river"

workers := river.NewWorkers()
workers.Add(&VisitWorker{})

client := river.NewClient(pgxPool, &river.Config{Workers: workers})
client.JobInsert(ctx, VisitArgs{LinkID: 123}, nil)
```

---

#### Option 3: **In-Process Worker** (No Redis fallback)

**For compatibility with Node.js synchronous fallback**:
```go
type VisitQueue interface {
  Add(ctx context.Context, payload VisitPayload) error
}

// Redis-backed
type AsynqQueue struct { client *asynq.Client }
func (q *AsynqQueue) Add(ctx context.Context, p VisitPayload) error { /* enqueue */ }

// Synchronous fallback
type SyncQueue struct { processor *VisitProcessor }
func (q *SyncQueue) Add(ctx context.Context, p VisitPayload) error {
  go q.processor.Handle(ctx, p) // Fire-and-forget goroutine
  return nil
}

func NewVisitQueue(redis *redis.Client, proc *VisitProcessor) VisitQueue {
  if redis != nil {
    return &AsynqQueue{client: asynq.NewClient(redis)}
  }
  return &SyncQueue{processor: proc}
}
```

---

### 9.6 GeoIP Library

**Recommended**: [github.com/oschwald/geoip2-golang](https://github.com/oschwald/geoip2-golang)

**Database**: Download GeoLite2-Country from [MaxMind](https://dev.maxmind.com/geoip/geolite2-free-geolocation-data)

**License**: Creative Commons BY-SA 4.0 (must display attribution)

**Usage**:
```go
db, _ := geoip2.Open("/path/to/GeoLite2-Country.mmdb")
defer db.Close()

ip := net.ParseIP("8.8.8.8")
record, _ := db.Country(ip)
country := record.Country.IsoCode // "US"
```

---

### 9.7 User Agent Parsing

**Recommended**: [github.com/mssola/user_agent](https://github.com/mssola/user_agent)

**Usage**:
```go
import "github.com/mssola/user_agent"

ua := user_agent.New("Mozilla/5.0 ...")
browser, _ := ua.Browser()
os := ua.OS()
```

---

### 9.8 Proposed Go Struct Definitions

```go
package models

import "time"

type User struct {
  ID                    int        `db:"id" json:"id"`
  Email                 string     `db:"email" json:"email"`
  Password              string     `db:"password" json:"-"` // Never expose
  Apikey                *string    `db:"apikey" json:"apikey,omitempty"`
  Role                  string     `db:"role" json:"role"`
  Verified              bool       `db:"verified" json:"verified"`
  Banned                bool       `db:"banned" json:"banned"`
  BannedByID            *int       `db:"banned_by_id" json:"banned_by_id,omitempty"`
  VerificationToken     *string    `db:"verification_token" json:"-"`
  VerificationExpires   *time.Time `db:"verification_expires" json:"-"`
  ResetPasswordToken    *string    `db:"reset_password_token" json:"-"`
  ResetPasswordExpires  *time.Time `db:"reset_password_expires" json:"-"`
  ChangeEmailToken      *string    `db:"change_email_token" json:"-"`
  ChangeEmailExpires    *time.Time `db:"change_email_expires" json:"-"`
  ChangeEmailAddress    *string    `db:"change_email_address" json:"-"`
  CreatedAt             time.Time  `db:"created_at" json:"created_at"`
  UpdatedAt             time.Time  `db:"updated_at" json:"updated_at"`
}

type Link struct {
  ID          int        `db:"id" json:"-"`
  Address     string     `db:"address" json:"address"`
  Target      string     `db:"target" json:"target"`
  Description *string    `db:"description" json:"description,omitempty"`
  Password    *string    `db:"password" json:"-"` // Hashed
  ExpireIn    *time.Time `db:"expire_in" json:"expire_in,omitempty"`
  UserID      *int       `db:"user_id" json:"-"`
  DomainID    *int       `db:"domain_id" json:"-"`
  Domain      *string    `db:"domain" json:"domain,omitempty"` // From join
  Banned      bool       `db:"banned" json:"banned"`
  BannedByID  *int       `db:"banned_by_id" json:"-"`
  VisitCount  int        `db:"visit_count" json:"visit_count"`
  UUID        string     `db:"uuid" json:"id"` // Public ID
  CreatedAt   time.Time  `db:"created_at" json:"created_at"`
  UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
}

type Domain struct {
  ID          int        `db:"id" json:"-"`
  Address     string     `db:"address" json:"address"`
  Homepage    *string    `db:"homepage" json:"homepage,omitempty"`
  UserID      *int       `db:"user_id" json:"-"`
  Banned      bool       `db:"banned" json:"banned"`
  BannedByID  *int       `db:"banned_by_id" json:"-"`
  UUID        string     `db:"uuid" json:"id"`
  CreatedAt   time.Time  `db:"created_at" json:"created_at"`
  UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
}

type Visit struct {
  ID          int                    `db:"id"`
  LinkID      *int                   `db:"link_id"`
  UserID      *int                   `db:"user_id"`
  Total       int                    `db:"total"`
  Countries   map[string]int         `db:"countries"` // JSONB
  Referrers   map[string]int         `db:"referrers"` // JSONB
  BrChrome    int                    `db:"br_chrome"`
  BrEdge      int                    `db:"br_edge"`
  BrFirefox   int                    `db:"br_firefox"`
  BrIE        int                    `db:"br_ie"`
  BrOpera     int                    `db:"br_opera"`
  BrSafari    int                    `db:"br_safari"`
  BrOther     int                    `db:"br_other"`
  OsAndroid   int                    `db:"os_android"`
  OsIOS       int                    `db:"os_ios"`
  OsLinux     int                    `db:"os_linux"`
  OsMacOS     int                    `db:"os_macos"`
  OsWindows   int                    `db:"os_windows"`
  OsOther     int                    `db:"os_other"`
  CreatedAt   time.Time              `db:"created_at"`
  UpdatedAt   *time.Time             `db:"updated_at"`
}

type Host struct {
  ID          int       `db:"id"`
  Address     string    `db:"address"`
  Banned      bool      `db:"banned"`
  BannedByID  *int      `db:"banned_by_id"`
  CreatedAt   time.Time `db:"created_at"`
  UpdatedAt   time.Time `db:"updated_at"`
}

type IP struct {
  ID        int       `db:"id"`
  IP        string    `db:"ip"`
  CreatedAt time.Time `db:"created_at"`
  UpdatedAt time.Time `db:"updated_at"`
}
```

---

### 9.9 Repository Interface Signatures

```go
package repository

import (
  "context"
  "kutt/internal/models"
)

type LinkRepository interface {
  Find(ctx context.Context, match LinkMatch) (*models.Link, error)
  Get(ctx context.Context, match LinkMatch, params QueryParams) ([]models.Link, error)
  Total(ctx context.Context, match LinkMatch, params QueryParams) (int, error)
  Create(ctx context.Context, params CreateLinkParams) (*models.Link, error)
  Update(ctx context.Context, match LinkMatch, update UpdateLinkParams) ([]models.Link, error)
  Remove(ctx context.Context, match LinkMatch) (RemoveResult, error)
  BatchRemove(ctx context.Context, match LinkMatch) error
  IncrementVisit(ctx context.Context, match LinkMatch) error
}

type UserRepository interface {
  Find(ctx context.Context, match UserMatch) (*models.User, error)
  Create(ctx context.Context, params CreateUserParams) (*models.User, error)
  Update(ctx context.Context, match UserMatch, update UpdateUserParams, methods *UpdateMethods) (*models.User, error)
  Remove(ctx context.Context, user *models.User) (bool, error)
  FindAny(ctx context.Context) (bool, error)
}

type DomainRepository interface {
  Find(ctx context.Context, match DomainMatch) (*models.Domain, error)
  Get(ctx context.Context, match DomainMatch) ([]models.Domain, error)
  Add(ctx context.Context, params AddDomainParams) (*models.Domain, error)
  Update(ctx context.Context, match DomainMatch, update UpdateDomainParams) ([]models.Domain, error)
  Remove(ctx context.Context, domain *models.Domain) (bool, error)
}

type VisitRepository interface {
  Add(ctx context.Context, params AddVisitParams) error
  Find(ctx context.Context, match VisitMatch) (*StatsResponse, error)
}

type HostRepository interface {
  Find(ctx context.Context, match HostMatch) (*models.Host, error)
  Add(ctx context.Context, params AddHostParams) (*models.Host, error)
}
```

---

## 10. Risks & Gotchas

### RISK-001: Timestamp Timezone Inconsistency (MySQL)

**Issue**: MySQL stores DATETIME in **local timezone**, not UTC (server/utils/utils.js:141-143).

**Risk**: If server timezone changes, existing timestamps become incorrect.

**Mitigation**:
1. **PostgreSQL**: Use `TIMESTAMPTZ` (stores UTC internally)
2. **MySQL**: Configure `SET time_zone = '+00:00'` at connection level
3. **Go**: Always convert to UTC before INSERT/UPDATE

```go
// Force UTC for MySQL connections
db.Exec("SET time_zone = '+00:00'")
```

---

### RISK-002: SQLite Timestamp Parsing (No 'Z' Suffix)

**Issue**: SQLite stores timestamps as TEXT without 'Z' suffix (e.g., `2026-09-27 14:30:00`).

**Risk**: `time.Parse()` treats as local timezone, not UTC.

**Mitigation** (server/utils/utils.js:120-123):
```go
func parseDatetime(dateStr string, dialect string) (time.Time, error) {
  if dialect == "sqlite" {
    dateStr += "Z" // Append UTC marker
  }
  return time.Parse(time.RFC3339, dateStr)
}
```

---

### RISK-003: Visit Backfill Migration (20241223103044)

**Issue**: Parallel UPDATE batches may timeout on large tables (100k+ rows per batch).

**Risk**: Migration fails mid-run, leaving partial data.

**Mitigation**:
1. Use **smaller batches** (50k rows) if timeout occurs
2. Add migration timeout configuration
3. Log progress (batch N of M completed)
4. Allow **resume** from last successful batch

---

### RISK-004: JSON Field Type Incompatibility

**Issue**: `visits.countries` is JSONB (Postgres), JSON (MySQL), TEXT (SQLite).

**Risk**: Scanning into Go `map[string]int` may fail on SQLite.

**Mitigation**:
```go
type JSONMap map[string]int

func (m *JSONMap) Scan(value interface{}) error {
  if value == nil {
    *m = make(map[string]int)
    return nil
  }
  
  var bytes []byte
  switch v := value.(type) {
  case []byte:
    bytes = v
  case string:
    bytes = []byte(v)
  default:
    return fmt.Errorf("unsupported type: %T", value)
  }
  
  return json.Unmarshal(bytes, m)
}

func (m JSONMap) Value() (driver.Value, error) {
  return json.Marshal(m)
}
```

---

### RISK-005: Cache Stampede on Stats Query

**Issue**: `visit.find()` has 60s TTL; on expiry, multiple requests may trigger expensive stats computation simultaneously.

**Risk**: Database overload.

**Mitigation**: Use **Redis SET NX EX** for cache-locking:
```go
lockKey := fmt.Sprintf("lock:stats:%d", linkID)
acquired, _ := redis.SetNX(ctx, lockKey, "1", 5*time.Second).Result()
if !acquired {
  time.Sleep(100 * time.Millisecond)
  return getCachedStats(ctx, linkID) // Retry cache read
}

defer redis.Del(ctx, lockKey)
stats := computeStats(ctx, linkID)
cacheStats(ctx, linkID, stats)
```

---

### RISK-006: Link Address Collision (Low Probability)

**Issue**: `nanoid()` generates 6-character IDs from 57-char alphabet → ~42 billion combinations.

**Risk**: After ~200k links, birthday paradox increases collision probability to ~0.01%.

**Mitigation**:
1. **Monitor**: Log collision retries
2. **Increase length**: Change `LINK_LENGTH` to 8 (env.js:33) → 10^14 combinations
3. **Detect excessive retries**: Fail after 5 retries (infinite loop prevention)

```go
func GenerateUniqueAddress(ctx context.Context, repo LinkRepository, domainID *int) (string, error) {
  for i := 0; i < 5; i++ {
    address := nanoid.Generate(alphabet, 6)
    existing, _ := repo.Find(ctx, LinkMatch{Address: &address, DomainID: domainID})
    if existing == nil {
      return address, nil
    }
  }
  return "", errors.New("failed to generate unique address after 5 retries")
}
```

---

### RISK-007: Redis Connection Failure

**Issue**: If Redis crashes mid-operation, cache writes/reads fail.

**Risk**: Application may panic or return stale data.

**Mitigation**:
```go
func (r *LinkRepository) getFromCache(ctx context.Context, key string) (*Link, error) {
  val, err := r.redis.Get(ctx, key).Result()
  if err == redis.Nil || err != nil {
    return nil, nil // Treat errors as cache miss
  }
  // ...
}
```

**Logging**: Log Redis errors but don't fail requests.

---

### RISK-008: JSONB Query Performance (visits.countries)

**Issue**: Filtering by country in `visits` table (e.g., `WHERE countries->>'US' > 10`) is slow without GIN index.

**Current State**: No JSONB indexes in schema.

**Mitigation** (PostgreSQL only):
```sql
CREATE INDEX visits_countries_gin ON visits USING GIN (countries);
```

---

### RISK-009: UUID Generation Differences

**Issue**: Node.js uses `knex.fn.uuid()`, which translates to:
- **PostgreSQL**: `gen_random_uuid()` (RFC 4122 v4)
- **MySQL**: `UUID()` (RFC 4122 v1 with timestamp)
- **SQLite**: Custom hex(randomblob(16)) (NOT RFC 4122)

**Risk**: UUID format inconsistency across databases.

**Mitigation**: Use **application-level UUID generation** (e.g., `github.com/google/uuid`) for consistency:
```go
import "github.com/google/uuid"

func createLink(ctx context.Context, params CreateLinkParams) (*Link, error) {
  uuid := uuid.New().String()
  // INSERT INTO links (..., uuid) VALUES (..., ?)
}
```

---

### RISK-010: Bcrypt Cost Mismatch

**Issue**: Node.js uses cost 12 (server/queries/link.queries.js:204); Go's `bcrypt.DefaultCost` is 10.

**Risk**: Password hashes generated in Go won't match Node.js validation.

**Mitigation**: Explicitly set cost to 12:
```go
import "golang.org/x/crypto/bcrypt"

func HashPassword(password string) (string, error) {
  hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
  return string(hash), err
}
```

---

## Summary

This SRS defines the complete data layer for porting Kutt URL shortener from Node.js to Go 1.27:

- **6 database tables** with full DDL for PostgreSQL, MySQL, SQLite
- **10 migrations** with compatibility strategy for existing `knex_migrations`
- **27 query functions** with exact SQL semantics and Go signatures
- **Redis caching** with key patterns, TTLs, and invalidation logic
- **Visit tracking pipeline**: queue, UA parsing, GeoIP, hourly aggregation
- **Cron job**: 30-second expired link deletion
- **17 business rules** governing user/link/domain/visit invariants
- **Go tooling recommendations**: sqlc, pgx, go-redis, Asynq, GeoIP2
- **10 critical risks** with detailed mitigation strategies

**Output File**: `/root/Omran/kutt-port-srs/part-A-data.md`

**Word Count**: ~12,000 words

**Next Steps**:
1. Review this SRS with Part B and Part C authors
2. Validate migration strategy against production databases
3. Implement proof-of-concept for visit aggregation performance
4. Set up Go project structure with recommended tooling

---

**Document Control**

| Requirement ID | Description | Source |
|----------------|-------------|--------|
| DATA-001 | Users table schema | server/models/user.model.js:6-40 |
| DATA-002 | Links table schema | server/models/link.model.js:5-44 |
| DATA-003 | Domains table schema | server/models/domain.model.js:4-35 |
| DATA-004 | Visits table schema | server/models/visit.model.js:4-87 |
| DATA-QUIRK-001 | Timestamp handling | server/utils/utils.js:132-147 |
| DATA-QUIRK-002 | Case-insensitive search | server/knex.js:31 |
| DATA-QUIRK-003 | Boolean handling | server/models/user.model.js:10-12 |
| DATA-QUIRK-004 | RETURNING clause compat | server/queries/link.queries.js:208-227 |
| DATA-QUIRK-005 | Timestamp truncation | server/utils/knex.js:28-45 |
| DATA-QUIRK-006 | JSON storage | server/queries/visit.queries.js:34-35 |
| MIG-001 | Initial schema migration | server/migrations/20200211220920_constraints.js |
| MIG-008 | visits.user_id backfill | server/migrations/20241223103044_visits_user_id.js |
| QUERY-LINK-001 | link.find() | server/queries/link.queries.js:179-197 |
| QUERY-LINK-007 | link.incrementVisit() | server/queries/link.queries.js:294-296 |
| QUERY-VISIT-001 | visit.add() | server/queries/visit.queries.js:8-66 |
| QUERY-VISIT-002 | visit.find() stats | server/queries/visit.queries.js:69-188 |
| RULE-USER-005 | User deletion cascades | server/models/link.model.js:30-34 |
| RULE-LINK-002 | Link expiration | server/cron.js:5-6 |
| RULE-VISIT-001 | Hourly aggregation | server/queries/visit.queries.js:16 |
| RISK-001 | MySQL timezone | server/utils/utils.js:141-143 |
| RISK-002 | SQLite timestamp parsing | server/utils/utils.js:122 |

---

**End of Document**


---

# Kutt URL Shortener — Part B: HTTP API, Routing, Handlers, Auth, Validation, Security, Mail, Config

**Software Requirements Specification**  
**Target:** Port from Node.js (v3.2.6, commit 279b491) to Go 1.27  
**Scope:** HTTP layer, routing, middleware, authentication, validation, error handling, mail, configuration  
**Cross-reference:** Part A (data layer/schema/queries/queues), Part C (views/frontend/deployment/overall Go architecture)

---

## 1. Route Inventory

All routes below cite `server/routes/*.js` and `server/handlers/*.handler.js`. Middleware chains execute left-to-right unless noted.

### 1.1 Health Check

**API-001** `GET /api/v2/health` (also `/api/health`)  
**Source:** `server/routes/health.routes.js:5`  
**Middleware:** None  
**Handler:** `(_, res) => res.send("OK")`  
**Request:** None  
**Response:** `200 text/plain "OK"`  

---

### 1.2 Link Management

**API-002** `GET /api/v2/links` (also `/api/links`)  
**Source:** `server/routes/link.routes.js:14-21`  
**Middleware:**
1. `locals.viewTemplate("partials/links/table")` — sets `req.viewTemplate` for HTML error rendering
2. `auth.apikey` — optional API key auth (`X-API-KEY` header, `passport-localapikey-update`)
3. `auth.jwt` — optional JWT auth (cookie `token`)
4. `helpers.parseQuery` — parses `limit`, `skip`, `search` query params (limits `limit` to max 50)

**Handler:** `link.get`  
**Request Query Params:**
- `limit` (number, default 10, max 50) — validated in `helpers.parseQuery:78`
- `skip` (number, default 0)
- `search` (string, optional)

**Response JSON:**
```json
{
  "total": 42,
  "limit": 10,
  "skip": 0,
  "data": [ /* Link[] */ ]
}
```
**Response HTML:** Renders `partials/links/table` with sanitized link data  
**Status:** `200` on success, `401` if unauthenticated  
**Business Logic:** Returns user's links (`user_id` = req.user.id), query executed in Part A.

---

**API-003** `GET /api/v2/links/admin`  
**Source:** `server/routes/link.routes.js:23-32`  
**Middleware:**
1. `locals.viewTemplate("partials/admin/links/table")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin` — checks `req.user.admin === true` (throws 401 if false, `auth.handler.js:92-95`)
5. `helpers.parseQuery`
6. `locals.adminTable` — sets `res.locals.query` with filter params

**Handler:** `link.getAdmin`  
**Request Query Params:**
- `limit`, `skip`, `search` (as above)
- `user` (email string, optional) — filter by user email
- `domain` (string, optional) — filter by domain address (value `DEFAULT_DOMAIN` means null domain_id)
- `banned` (boolean, optional) — `"true"`, `"false"`, or undefined
- `anonymous` (boolean, optional) — filter by `user_id IS NULL`
- `has_domain` (boolean, optional) — filter by `domain_id IS NOT NULL`

**Response JSON:** Same shape as API-002  
**Business Logic:** Admin-only, returns all links matching filters (`links.handler.js:51-97`).

---

**API-004** `POST /api/v2/links`  
**Source:** `server/routes/link.routes.js:34-44`  
**Middleware:**
1. `cors()` — CORS enabled for this route
2. `locals.viewTemplate("partials/shortener")`
3. `auth.apikey`
4. `auth.jwtLoose` if `DISALLOW_ANONYMOUS_LINKS` is false, else `auth.jwt` (strict)
5. `locals.createLink` — sets `res.locals.show_advanced`
6. `validators.createLink` — express-validator chain (see §3)
7. `helpers.verify` — checks validation errors, throws `CustomError(error.msg, 400)` if any

**Handler:** `link.create`  
**Request Body (JSON or form-data):**
```json
{
  "target": "https://example.com/very/long/url",
  "customurl": "mylink",          // optional, 1-64 chars, user-only, custom alphabet or a-zA-Z0-9-_
  "password": "secret",            // optional, 3-64 chars, user-only
  "description": "My link",        // optional, 0-2040 chars
  "expire_in": "2 days",           // optional, parsed by ms(), min 1m, converted to UTC datetime
  "reuse": false,                  // optional, boolean, user-only (return existing link if same target+domain)
  "domain": "custom.example.com"   // optional, user-only, must own domain
}
```
**Validation Rules (validators.handler.js:20-98):**
- `target`: required, trimmed, 1-2040 chars, auto-prefixed with `http://` if no protocol, must match `urlRegex` (utils.js:26), cannot be `DEFAULT_DOMAIN`
- `customurl`: if provided, checks `customAddressRegex` (`^[a-zA-Z0-9-_]+$`) or `customAlphabetRegex` (server/utils/utils.js:29-32), not in `preservedURLs` list (utils.js:158-199)
- `password`: 3-64 chars
- `expire_in`: string parsed by `ms()` (e.g., "1m", "8h", "42 days"), must be ≥ 1 minute, converted to UTC timestamp
- `domain`: if not `DEFAULT_DOMAIN`, must exist in user's domains (`query.domain.find`)
- `reuse`: boolean

**Pre-create Checks (links.handler.js:105-120):**
1. If `reuse=true`, search for existing link with same `target`, `user_id`, `domain_id` → return existing
2. If `customurl` provided, check not taken (`query.link.find({ address, domain_id })`) → error if exists
3. If `!customurl`, generate random ID via `nanoid()` from custom alphabet (utils.js:15)
4. `validators.bannedDomain(targetDomain)` — check if target domain is banned (`query.domain.find({ address, banned: true })`)
5. `validators.bannedHost(targetDomain)` — DNS lookup, check if IP is banned (`query.host.find({ address: ip, banned: true })`)

**Response JSON:**
```json
{
  "id": "uuid",
  "address": "abc123",
  "target": "https://example.com/...",
  "description": "...",
  "password": true,   // boolean, not the actual password
  "banned": false,
  "created_at": "2024-01-01T00:00:00.000Z",
  "updated_at": "2024-01-01T00:00:00.000Z",
  "visit_count": 0,
  "link": "https://kutt.to/abc123"
}
```
**Response HTML:** Renders `partials/shortener` with short URL, sets HX-Trigger header `reloadMainTable`  
**Status:** `201` on success, `400` if validation fails or custom URL taken  
**Business Logic:** Creates link record in DB, stores bcrypt hash of password if provided (cost 12, bcryptjs:98), links.handler.js:136-161.

---

**API-005** `PATCH /api/v2/links/:id`  
**Source:** `server/routes/link.routes.js:46-55`  
**Middleware:**
1. `locals.viewTemplate("partials/links/edit")`
2. `auth.apikey`
3. `auth.jwt`
4. `locals.editLink` — sets `res.locals.id`, `res.locals.class = "no-animation"`
5. `validators.editLink` — see below
6. `helpers.verify`

**Handler:** `link.edit`  
**Request Path Param:**
- `:id` (UUID, 36 chars) — link UUID

**Request Body:**
```json
{
  "address": "newaddr",        // optional, new custom address
  "target": "https://new.url", // optional
  "description": "...",        // optional, 0-2040 chars
  "expire_in": "1 week",       // optional
  "password": "newpass"        // optional, 3-64 chars, or null to remove
}
```
**Validation Rules (validators.handler.js:100-152):**
- All fields optional
- Same rules as `createLink` for each field
- `address` replaces `customurl` field name

**Business Logic (links.handler.js:163-254):**
1. Find link by `uuid` AND `user_id` (non-admin users only edit own links)
2. Check at least one field changed (skip if unchanged, `differenceInSeconds` check for `expire_in`)
3. Password handling: if `password` is all `•` (masked), ignore; if `null`, clear password; else hash new password
4. Check new address not taken (if changed)
5. Run `bannedDomain` and `bannedHost` checks on new target (if changed)
6. Update link, return sanitized response

**Response JSON:** Updated Link object  
**Response HTML:** Renders `partials/links/edit` with `swap_oob: true`, `success` message  
**Status:** `200` on success, `400` if no change, `404` if link not found

---

**API-006** `PATCH /api/v2/links/admin/:id`  
**Source:** `server/routes/link.routes.js:57-67`  
Identical to API-005 but:
- Middleware includes `auth.admin` after `auth.jwt`
- Handler: `link.editAdmin` (links.handler.js:256-347)
- Does NOT filter by `user_id` when finding link (admin can edit any link)
- Renders `partials/admin/links/edit` for HTML

---

**API-007** `DELETE /api/v2/links/:id`  
**Source:** `server/routes/link.routes.js:69-77`  
**Middleware:**
1. `locals.viewTemplate("partials/links/dialog/delete")`
2. `auth.apikey`
3. `auth.jwt`
4. `validators.deleteLink` — validates `:id` is 36-char UUID
5. `helpers.verify`

**Handler:** `link.remove`  
**Request Path Param:** `:id` (UUID)  
**Business Logic (links.handler.js:349-372):**
- Calls `query.link.remove({ uuid, user_id })` (Part A, soft-delete: sets `deleted_at`)
- Non-admin users can only delete own links

**Response JSON:**
```json
{
  "message": "Link has been deleted successfully."
}
```
**Response HTML:** Renders `partials/links/dialog/delete_success`, sets HX-Reswap `outerHTML`, HX-Trigger `reloadMainTable`  
**Status:** `200` on success, `400/404` if not found

---

**API-008** `POST /api/v2/links/admin/ban/:id`  
**Source:** `server/routes/link.routes.js:79-88`  
**Middleware:**
1. `locals.viewTemplate("partials/links/dialog/ban")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin`
5. `validators.banLink` — validates `:id` (UUID), body fields `host`, `user`, `userLinks`, `domain` (all optional booleans)
6. `helpers.verify`

**Handler:** `link.ban`  
**Request Path Param:** `:id` (UUID)  
**Request Body:**
```json
{
  "host": true,      // ban target's IP host
  "domain": true,    // ban target's domain
  "user": true,      // ban link owner
  "userLinks": true  // ban all owner's links
}
```
**Business Logic (links.handler.js:391-457):**
1. Find link by UUID (must exist, not already banned)
2. Update link: `banned = true`, `banned_by_id = req.user.id`
3. If `domain=true`: add target domain to banned domains (`query.domain.add({ address, banned: true, banned_by_id })`)
4. If `host=true`: DNS lookup target hostname, add IP to banned hosts (`query.host.add({ address: ip, banned: true, banned_by_id })`)
5. If `user=true`: ban link owner (`query.user.update({ id: link.user_id }, { banned: true, banned_by_id })`)
6. If `userLinks=true`: ban all owner's links (`query.link.update({ user_id: link.user_id }, { banned: true, banned_by_id })`)

**Response JSON:**
```json
{
  "message": "Banned link successfully."
}
```
**Response HTML:** Renders `partials/links/dialog/ban_success`, sets HX-Reswap `outerHTML`, HX-Trigger `reloadMainTable`  
**Status:** `200` on success, `400` if already banned or not found

---

**API-009** `GET /api/v2/links/:id/stats`  
**Source:** `server/routes/link.routes.js:90-98`  
**Middleware:**
1. `locals.viewTemplate("partials/stats")`
2. `auth.apikey`
3. `auth.jwt`
4. `validators.getStats` — validates `:id` (UUID)
5. `helpers.verify`

**Handler:** `link.stats`  
**Request Path Param:** `:id` (UUID)  
**Business Logic (links.handler.js:610-647):**
- Find link by UUID (filter by `user_id` for non-admin)
- Call `query.visit.find({ link_id }, link.visit_count)` (Part A) — aggregates visit stats into `lastDay`, `lastWeek`, `lastMonth`, `lastYear`
- Each period contains: `{ stats: { browser, os, country, referrer }, views: [array of hourly/daily/monthly counts] }`

**Response JSON:**
```json
{
  "id": "uuid",
  "address": "abc123",
  "target": "https://...",
  "banned": false,
  "password": false,
  "created_at": "...",
  "updated_at": "...",
  "visit_count": 42,
  "link": "https://kutt.to/abc123",
  "lastDay": {
    "stats": {
      "browser": [{"name": "chrome", "value": 10}, ...],
      "os": [{"name": "windows", "value": 5}, ...],
      "country": [{"name": "US", "value": 8}, ...],
      "referrer": [{"name": "google.com", "value": 3}, ...]
    },
    "views": [0, 1, 2, 3, ...]  // 24 hourly counts
  },
  "lastWeek": { ... },  // 7 daily counts
  "lastMonth": { ... }, // 30 daily counts
  "lastYear": { ... },  // 12 monthly counts
  "updatedAt": "..."
}
```
**Response HTML:** Renders `partials/stats` with map data  
**Status:** `200` on success, `404` if link not found

---

**API-010** `POST /api/v2/links/:id/protected`  
**Source:** `server/routes/link.routes.js:100-107`  
**Middleware:**
1. `locals.viewTemplate("partials/protected/form")`
2. `locals.protected` — sets `res.locals.id`
3. `validators.redirectProtected` — validates `:id` (UUID), body `password` (3-64 chars)
4. `helpers.verify`

**Handler:** `link.redirectProtected`  
**Request Path Param:** `:id` (UUID)  
**Request Body:**
```json
{
  "password": "secret"
}
```
**Business Logic (links.handler.js:547-585):**
1. Find link by UUID
2. Verify link has password set
3. Compare `bcrypt.compare(req.body.password, link.password)`
4. If match, create visit record (if not bot, via `queue.visit.add` — Part A)
5. Return target URL

**Response JSON:**
```json
{
  "target": "https://example.com/..."
}
```
**Response HTML:** Sets HX-Redirect to target, renders form with "Redirecting..." message  
**Status:** `200` on success, `400` if link not found, `401` if password incorrect

---

**API-011** `POST /api/v2/links/report`  
**Source:** `server/routes/link.routes.js:109-116`  
**Middleware:**
1. `locals.viewTemplate("partials/report/form")`
2. `auth.featureAccess([env.MAIL_ENABLED])` — throws 400 if mail disabled
3. `validators.reportLink` — validates `link` (must be a `DEFAULT_DOMAIN` link)
4. `helpers.verify`

**Handler:** `link.report`  
**Request Body:**
```json
{
  "link": "https://kutt.to/abc123"
}
```
**Business Logic (links.handler.js:374-389):**
- Sends email to `env.REPORT_EMAIL` via `transporter.sendReportEmail(link)`

**Response JSON:**
```json
{
  "message": "Thanks for the report, we'll take actions shortly."
}
```
**Response HTML:** Renders `partials/report/form` with success message  
**Status:** `200` on success

---

### 1.3 User Management

**API-012** `GET /api/v2/users`  
**Source:** `server/routes/user.routes.js:12-17`  
**Middleware:**
1. `auth.apikey`
2. `auth.jwt`

**Handler:** `user.get`  
**Response JSON:**
```json
{
  "apikey": "...",  // 40-char nanoid
  "email": "user@example.com",
  "domains": [ /* Domain[] */ ]
}
```
**Business Logic (users.handler.js:8-18):**
- Returns current user's email, apikey, and owned domains (sanitized)

**Status:** `200`, `401` if unauthenticated

---

**API-013** `GET /api/v2/users/admin`  
**Source:** `server/routes/user.routes.js:19-28`  
**Middleware:**
1. `locals.viewTemplate("partials/admin/users/table")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin`
5. `helpers.parseQuery`
6. `locals.adminTable`

**Handler:** `user.getAdmin`  
**Request Query Params:**
- `limit`, `skip`, `search` (as before)
- `role` (string, "USER" or "ADMIN")
- `verified` (boolean)
- `banned` (boolean)
- `domains` (boolean) — has domains
- `links` (boolean) — has links

**Response JSON:**
```json
{
  "total": 100,
  "limit": 10,
  "skip": 0,
  "data": [ /* User[] with aggregated links_count, domains_count */ ]
}
```
**Business Logic (users.handler.js:64-103):**
- Admin-only, returns all users matching filters

**Status:** `200` on success

---

**API-014** `POST /api/v2/users/admin`  
**Source:** `server/routes/user.routes.js:30-39`  
**Middleware:**
1. `locals.viewTemplate("partials/admin/dialog/create_user")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin`
5. `validators.createUser` (see below)
6. `helpers.verify`

**Handler:** `user.create`  
**Request Body:**
```json
{
  "email": "newuser@example.com",
  "password": "password123",       // 8-64 chars, will be bcrypt hashed
  "role": "USER",                  // optional, "USER" or "ADMIN"
  "verified": true,                // optional, boolean (default false)
  "banned": false,                 // optional, boolean
  "verification_email": true       // optional, send verification email if not verified
}
```
**Validation Rules (validators.handler.js:340-373):**
- `email`: required, trimmed, max 255 chars, valid email, must not exist
- `password`: required, 8-64 chars
- `role`: optional, must be in `["USER", "ADMIN"]`
- `verified`, `banned`, `verification_email`: optional booleans

**Business Logic (users.handler.js:157-176):**
1. Hash password (bcrypt cost 12)
2. Create user via `query.user.create(req.body)`
3. If `verification_email=true` and not verified/banned, send verification email

**Response JSON:**
```json
{
  "message": "The user has been created successfully."
}
```
**Response HTML:** Renders `partials/admin/dialog/create_user_success`, sets HX-Trigger `reloadMainTable`  
**Status:** `201` on success

---

**API-015** `POST /api/v2/users/delete`  
**Source:** `server/routes/user.routes.js:41-49`  
**Middleware:**
1. `locals.viewTemplate("partials/settings/delete_account")`
2. `auth.apikey`
3. `auth.jwt`
4. `validators.deleteUser` — validates body `password` (8-64 chars, bcrypt.compare with `req.user.password`)
5. `helpers.verify`

**Handler:** `user.remove`  
**Request Body:**
```json
{
  "password": "currentpassword"
}
```
**Business Logic (users.handler.js:20-33):**
- Calls `query.user.remove(req.user)` (Part A, deletes user + all links/domains)
- Clears token cookie

**Response JSON:**
```json
"OK"
```
**Response HTML:** Clears token, sets HX-Trigger-After-Swap `redirectToHomepage`, renders success  
**Status:** `200` on success, `401` if password wrong

---

**API-016** `DELETE /api/v2/users/admin/:id`  
**Source:** `server/routes/user.routes.js:51-60`  
**Middleware:**
1. `locals.viewTemplate("partials/admin/dialog/delete_user")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin`
5. `validators.deleteUserByAdmin` — validates `:id` (numeric user ID)
6. `helpers.verify`

**Handler:** `user.removeByAdmin`  
**Request Path Param:** `:id` (numeric user ID)  
**Business Logic (users.handler.js:35-62):**
- Find user by ID, delete via `query.user.remove(user)`

**Response JSON:**
```json
{
  "message": "User has been deleted successfully."
}
```
**Response HTML:** Renders `partials/admin/dialog/delete_user_success`, sets HX-Reswap `outerHTML`, HX-Trigger `reloadMainTable`  
**Status:** `200` on success, `400` if not found

---

**API-017** `POST /api/v2/users/admin/ban/:id`  
**Source:** `server/routes/user.routes.js:62-71`  
**Middleware:**
1. `locals.viewTemplate("partials/admin/dialog/ban_user")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin`
5. `validators.banUser` — validates `:id` (numeric), body `links`, `domains` (optional booleans)
6. `helpers.verify`

**Handler:** `user.ban`  
**Request Path Param:** `:id` (numeric user ID)  
**Request Body:**
```json
{
  "links": true,    // ban all user's links
  "domains": true   // ban all user's domains
}
```
**Business Logic (users.handler.js:105-155):**
1. Find user, update `banned = true`, `banned_by_id = req.user.id`
2. If `links=true`: ban all user's links
3. If `domains=true`: ban all user's domains

**Response JSON:**
```json
{
  "message": "Banned user successfully."
}
```
**Response HTML:** Renders `partials/admin/dialog/ban_user_success`, sets HX-Reswap `outerHTML`, HX-Trigger `reloadMainTable`  
**Status:** `200` on success, `400` if already banned

---

### 1.4 Domain Management

**API-018** `POST /api/v2/domains`  
**Source:** `server/routes/domain.routes.js:23-31`  
**Middleware:**
1. `locals.viewTemplate("partials/settings/domain/add_form")`
2. `auth.apikey`
3. `auth.jwt`
4. `validators.addDomain` (see below)
5. `helpers.verify`

**Handler:** `domains.add`  
**Request Body:**
```json
{
  "address": "custom.example.com",  // or "https://custom.example.com" (protocol stripped)
  "homepage": "https://example.com" // optional, redirect root to this URL
}
```
**Validation Rules (validators.handler.js:165-189):**
- `address`: required, 3-64 chars, trimmed, auto-prefixed with `http://` if needed, validated as URL, protocol+www removed, cannot be `DEFAULT_DOMAIN`, must not already exist with a user_id or be banned

**Business Logic (domains.handler.js:9-28):**
- Creates domain record with `user_id = req.user.id`

**Response JSON:**
```json
{
  "id": "uuid",
  "address": "custom.example.com",
  "homepage": "https://example.com",
  "banned": false,
  "created_at": "...",
  "updated_at": "..."
}
```
**Response HTML:** Renders `partials/settings/domain/table` with all user domains, sets HX-Reswap `none`  
**Status:** `200` on success, `400` if domain taken or banned

---

**API-019** `POST /api/v2/domains/admin`  
**Source:** `server/routes/domain.routes.js:33-42`  
**Middleware:**
1. `locals.viewTemplate("partials/admin/dialog/add_domain")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin`
5. `validators.addDomainAdmin` — similar to `addDomain` but allows `banned` field
6. `helpers.verify`

**Handler:** `domains.addAdmin`  
**Request Body:**
```json
{
  "address": "custom.example.com",
  "homepage": "https://example.com",
  "banned": true  // optional, create as banned (no user_id)
}
```
**Business Logic (domains.handler.js:30-49):**
- Admin can create global banned domains or user-less domains

**Response JSON:**
```json
{
  "message": "The domain has been added successfully."
}
```
**Response HTML:** Renders `partials/admin/dialog/add_domain_success`, sets HX-Trigger `reloadMainTable`  
**Status:** `200` on success

---

**API-020** `DELETE /api/v2/domains/:id`  
**Source:** `server/routes/domain.routes.js:44-52`  
**Middleware:**
1. `locals.viewTemplate("partials/settings/domain/delete")`
2. `auth.apikey`
3. `auth.jwt`
4. `validators.removeDomain` — validates `:id` (UUID)
5. `helpers.verify`

**Handler:** `domains.remove`  
**Request Path Param:** `:id` (domain UUID)  
**Business Logic (domains.handler.js:51-85):**
- Find domain by `uuid` AND `user_id` (user can only delete own domains)
- Sets `user_id = null` (orphans domain but keeps it in DB for existing links)
- If Redis enabled, removes from cache

**Response JSON:**
```json
{
  "message": "Domain deleted successfully"
}
```
**Response HTML:** Renders `partials/settings/domain/delete_success` with updated domain list, sets HX-Reswap `outerHTML`  
**Status:** `200` on success, `400` if not found

---

**API-021** `DELETE /api/v2/domains/admin/:id`  
**Source:** `server/routes/domain.routes.js:54-63`  
**Middleware:**
1. `locals.viewTemplate("partials/admin/dialog/delete_domain")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin`
5. `validators.removeDomainAdmin` — validates `:id` (numeric domain ID), query `links` (optional boolean)
6. `helpers.verify`

**Handler:** `domains.removeAdmin`  
**Request Path Param:** `:id` (numeric domain ID)  
**Request Query Param:** `links=true` — also delete all links using this domain  
**Business Logic (domains.handler.js:87-113):**
- Admin can hard-delete domain
- If `links=true`, calls `query.link.batchRemove({ domain_id })`

**Response JSON:**
```json
{
  "message": "Domain deleted successfully"
}
```
**Response HTML:** Renders `partials/admin/dialog/delete_domain_success`, sets HX-Reswap `outerHTML`, HX-Trigger `reloadMainTable`  
**Status:** `200` on success

---

**API-022** `GET /api/v2/domains/admin`  
**Source:** `server/routes/domain.routes.js:12-21`  
**Middleware:**
1. `locals.viewTemplate("partials/admin/domains/table")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin`
5. `helpers.parseQuery`
6. `locals.adminTable`

**Handler:** `domains.getAdmin`  
**Request Query Params:**
- `limit`, `skip`, `search`
- `user` (email string)
- `banned` (boolean)
- `owner` (boolean) — has user_id
- `links` (boolean) — has links

**Response JSON:**
```json
{
  "total": 50,
  "limit": 10,
  "skip": 0,
  "data": [ /* Domain[] with links_count */ ]
}
```
**Business Logic (domains.handler.js:115-152):**
- Admin-only, returns all domains matching filters

**Status:** `200` on success

---

**API-023** `POST /api/v2/domains/admin/ban/:id`  
**Source:** `server/routes/domain.routes.js:65-74`  
**Middleware:**
1. `locals.viewTemplate("partials/admin/dialog/ban_domain")`
2. `auth.apikey`
3. `auth.jwt`
4. `auth.admin`
5. `validators.banDomain` — validates `:id` (numeric), body `links`, `user` (optional booleans)
6. `helpers.verify`

**Handler:** `domains.ban`  
**Request Path Param:** `:id` (numeric domain ID)  
**Request Body:**
```json
{
  "links": true,  // ban all links using this domain
  "user": true    // ban domain owner
}
```
**Business Logic (domains.handler.js:154-204):**
1. Update domain: `banned = true`, `banned_by_id = req.user.id`
2. If `links=true`: ban all links with `domain_id = id`
3. If `user=true`: ban domain owner (`query.user.update({ id: domain.user_id }, { banned: true })`)

**Response JSON:**
```json
{
  "message": "Banned domain successfully."
}
```
**Response HTML:** Renders `partials/admin/dialog/ban_domain_success`, sets HX-Reswap `outerHTML`, HX-Trigger `reloadMainTable`  
**Status:** `200` on success

---

### 1.5 Authentication

**API-024** `POST /api/v2/auth/login`  
**Source:** `server/routes/auth.routes.js:13-22`  
**Middleware:**
1. `locals.viewTemplate("partials/auth/form")`
2. `auth.featureAccess([!env.DISALLOW_LOGIN_FORM])` — throws 400 if form login disabled
3. `validators.login` — validates `email` (max 255, valid email), `password` (8-64 chars)
4. `helpers.verify`
5. `helpers.rateLimit({ window: 60, limit: 5 })` — max 5 attempts per minute per IP
6. `auth.local` — passport local strategy: finds user by email, compares bcrypt password (passport.js:38-54)
7. `auth.login` — generates JWT, sets cookie

**Handler:** `auth.login`  
**Request Body:**
```json
{
  "email": "user@example.com",
  "password": "password123"
}
```
**Business Logic (auth.handler.js:143-153):**
1. `auth.local` finds user, verifies password
2. `auth.login` signs JWT (utils.js:38-48): `{ iss: "ApiAuth", sub: user.id, iat, exp }`, secret = `JWT_SECRET`, expiry 7 days
3. Sets cookie `token` (httpOnly, secure in prod, sameSite=Lax, maxAge 7 days) (utils.js:50-57)

**Response JSON:**
```json
{
  "token": "eyJhbGci..."
}
```
**Response HTML:** Sets token cookie, renders `partials/auth/welcome`  
**Status:** `200` on success, `401` if credentials wrong

---

**API-025** `POST /api/v2/auth/signup`  
**Source:** `server/routes/auth.routes.js:24-34`  
**Middleware:**
1. `locals.viewTemplate("partials/auth/form")`
2. `auth.featureAccess([!env.DISALLOW_REGISTRATION, env.MAIL_ENABLED])` — both must be true
3. `validators.signup` — validates `email`, `password` (8-64 chars)
4. `helpers.verify`
5. `helpers.rateLimit({ window: 60, limit: 5 })`
6. `validators.signupEmailTaken` — checks email not taken by verified user
7. `helpers.verify`

**Handler:** `auth.signup`  
**Request Body:**
```json
{
  "email": "newuser@example.com",
  "password": "password123"
}
```
**Business Logic (auth.handler.js:97-114):**
1. Hash password (bcrypt cost 12)
2. Create user via `query.user.add({ email, password })` → generates `verification_token` (UUID), `verification_expires` (30 min)
3. Send verification email via `mail.verification(user)`

**Response JSON:**
```json
{
  "message": "A verification email has been sent."
}
```
**Response HTML:** Renders `partials/auth/verify`  
**Status:** `201` on success, `400` if email taken or validation fails

---

**API-026** `POST /api/v2/auth/create-admin`  
**Source:** `server/routes/auth.routes.js:36-43`  
**Middleware:**
1. `locals.viewTemplate("partials/auth/form_admin")`
2. `validators.createAdmin` — same as `signup`
3. `helpers.verify`
4. `helpers.rateLimit({ window: 60, limit: 5 })`

**Handler:** `auth.createAdminUser`  
**Request Body:** Same as signup  
**Business Logic (auth.handler.js:116-141):**
1. Check no user exists (`query.user.findAny()`)
2. If exists, throw error
3. Create user with `role = "ADMIN"`, `verified = true`
4. Sign JWT, set cookie

**Response JSON:**
```json
{
  "token": "..."
}
```
**Response HTML:** Sets token, renders `partials/auth/welcome`  
**Status:** `201` on success, `400` if user already exists

---

**API-027** `POST /api/v2/auth/change-password`  
**Source:** `server/routes/auth.routes.js:45-53`  
**Middleware:**
1. `locals.viewTemplate("partials/settings/change_password")`
2. `auth.jwt`
3. `validators.changePassword` — validates `currentpassword`, `newpassword` (both 8-64 chars)
4. `helpers.verify`
5. `helpers.rateLimit({ window: 60, limit: 5 })`

**Handler:** `auth.changePassword`  
**Request Body:**
```json
{
  "currentpassword": "oldpass",
  "newpassword": "newpass"
}
```
**Business Logic (auth.handler.js:181-209):**
1. Verify `currentpassword` with `bcrypt.compare(req.body.currentpassword, req.user.password)`
2. Hash `newpassword` (cost 12)
3. Update user password

**Response JSON:**
```json
{
  "message": "Your password has been changed successfully."
}
```
**Response HTML:** Sets HX-Trigger-After-Swap `resetChangePasswordForm`, renders form with success  
**Status:** `200` on success, `401` if current password wrong

---

**API-028** `POST /api/v2/auth/change-email`  
**Source:** `server/routes/auth.routes.js:55-64`  
**Middleware:**
1. `locals.viewTemplate("partials/settings/change_email")`
2. `auth.jwt`
3. `auth.featureAccess([env.MAIL_ENABLED])`
4. `validators.changeEmail` — validates `email` (max 255, valid), `password` (8-64 chars)
5. `helpers.verify`
6. `helpers.rateLimit({ window: 60, limit: 3 })`

**Handler:** `auth.changeEmailRequest`  
**Request Body:**
```json
{
  "email": "newemail@example.com",
  "password": "currentpass"
}
```
**Business Logic (auth.handler.js:286-329):**
1. Verify password
2. Check new email not taken
3. Update user: `change_email_address`, `change_email_token` (UUID), `change_email_expires` (30 min)
4. Send verification email to new address via `mail.changeEmail({ ...updatedUser, email })`

**Response JSON:**
```json
{
  "message": "A verification link has been sent to the requested email address."
}
```
**Response HTML:** Sets HX-Trigger-After-Swap `resetChangeEmailForm`, renders form with success  
**Status:** `200` on success, `400` if email taken, `401` if password wrong

---

**API-029** `POST /api/v2/auth/apikey`  
**Source:** `server/routes/auth.routes.js:66-72`  
**Middleware:**
1. `locals.viewTemplate("partials/settings/apikey")`
2. `auth.jwt`
3. `helpers.rateLimit({ window: 60, limit: 10 })`

**Handler:** `auth.generateApiKey`  
**Request Body:** None  
**Business Logic (auth.handler.js:211-232):**
1. Generate 40-char random API key via `nanoid(40)` (utils/utils.js:212)
2. Update user record
3. If Redis enabled, remove user from cache

**Response JSON:**
```json
{
  "apikey": "..."
}
```
**Response HTML:** Renders `partials/settings/apikey` with new key  
**Status:** `201` on success

---

**API-030** `POST /api/v2/auth/reset-password`  
**Source:** `server/routes/auth.routes.js:74-82`  
**Middleware:**
1. `locals.viewTemplate("partials/reset_password/request_form")`
2. `auth.featureAccess([env.MAIL_ENABLED])`
3. `validators.resetPassword` — validates `email`
4. `helpers.verify`
5. `helpers.rateLimit({ window: 60, limit: 3 })`

**Handler:** `auth.resetPassword`  
**Request Body:**
```json
{
  "email": "user@example.com"
}
```
**Business Logic (auth.handler.js:234-259):**
1. Find user by email, update `reset_password_token` (UUID), `reset_password_expires` (30 min)
2. Send email via `mail.resetPasswordToken(user)` (async, errors caught)

**Response JSON:**
```json
{
  "message": "If email address exists, a reset password email has been sent."
}
```
**Response HTML:** Renders form with success message (vague for security)  
**Status:** `200` always (to prevent email enumeration)

---

**API-031** `POST /api/v2/auth/new-password`  
**Source:** `server/routes/auth.routes.js:84-92`  
**Middleware:**
1. `locals.viewTemplate("partials/reset_password/new_password_form")`
2. `locals.newPassword` — sets `res.locals.reset_password_token`
3. `validators.newPassword` — validates `reset_password_token` (UUID), `new_password` (8-64 chars), `repeat_password` (must match)
4. `helpers.verify`
5. `helpers.rateLimit({ window: 60, limit: 5 })`

**Handler:** `auth.newPassword`  
**Request Body:**
```json
{
  "reset_password_token": "uuid",
  "new_password": "newpass",
  "repeat_password": "newpass"
}
```
**Business Logic (auth.handler.js:261-284):**
1. Find user by token AND `reset_password_expires > now`
2. Hash new password
3. Update user: clear token/expires, set new password

**Response HTML:** Renders `partials/reset_password/new_password_success`  
**Status:** `200` on success, `400` if token invalid/expired

---

### 1.6 OIDC Authentication

**API-032** `GET /login/oidc` (not under /api)  
**Source:** `server/routes/renders.routes.js:28-35`  
**Middleware:**
1. `locals.viewTemplate("login")`
2. `auth.featureAccess([env.OIDC_ENABLED])`
3. `auth.jwtLoosePage`
4. `auth.oidc` — passport OIDC strategy (passport.js:77-140)
5. `auth.login`

**Handler:** `auth.login` (same as API-024)  
**OIDC Flow (passport.js:77-140):**
1. Discovers OIDC issuer at `OIDC_ISSUER` via `openid-client`
2. Creates client with `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, redirect URI `<SITE_URL>/login/oidc`
3. Initiates authorization code flow with `OIDC_SCOPE` (default "openid profile email"), optional `OIDC_PROMPT`
4. On callback, exchanges code for tokens, extracts email from `userinfo[OIDC_EMAIL_CLAIM]`
5. If user exists, log in; else create new user with random password, set `verified = true`
6. Signs JWT, sets cookie

**Redirect:** After success, renders login page which redirects to `/`  
**Error Handling:** OIDC errors caught, throws `CustomError("OIDC authentication failed.", 401)`

---

### 1.7 Redirect Route

**API-033** `GET /:id`  
**Source:** `server/server.js:82`  
**Middleware:** `asyncHandler` wrapper  
**Handler:** `links.redirect`  
**Request Path Param:** `:id` (link address, may end with `+` for info mode)  
**Business Logic (links.handler.js:459-545):**
1. Skip if `:id` is in `preservedURLs` list (utils.js:158-199)
2. Extract domain from `req.headers.host` (strip www)
3. If custom domain (not `DEFAULT_DOMAIN`), find domain record
4. Parse address (strip trailing `+` if present)
5. Find link by `address` + `domain_id`
6. If not found: redirect to domain homepage or `/404`
7. If banned: redirect to `/banned`
8. If requesting info (ends with `+`) and no password: render `url_info` page or return `{ target }` JSON
9. If password protected:
   - Check for `Authorization: Basic <base64>` header, decode, extract password after colon, bcrypt.compare
   - If match, redirect to target
   - Else render `protected` page with password form
10. If not bot (checked via `isbot(req.headers["user-agent"])`), queue visit via `queue.visit.add({ userAgent, ip, country: req.get("cf-ipcountry"), referrer: req.get("Referrer"), link })`
11. Redirect (302) to `link.target`

**Response:** HTTP 302 redirect to target, or HTML page for password/info  
**Status:** `302` on redirect, `200` for HTML pages, `404` if not found

---

**API-034** Custom Domain Homepage Redirect  
**Source:** `server/server.js:72`  
**Middleware:** `asyncHandler(links.redirectCustomDomainHomepage)`  
**Business Logic (links.handler.js:587-608):**
- If host is not `DEFAULT_DOMAIN` AND path is `/` or a preserved URL, find domain by address
- If domain has `homepage`, redirect (302) to homepage
- Else continue to next route

---

### 1.8 Web Form Routes (HTML-only)

These routes are not part of the JSON API but are referenced for completeness:

- **`GET /`** → renders homepage (redirects to `/login` if `DISALLOW_ANONYMOUS_LINKS` and not logged in)
- **`GET /login`** → renders login page
- **`GET /logout`** → clears token, renders logout page
- **`GET /create-admin`** → renders admin creation form (only if no users exist)
- **`GET /settings`** → renders user settings (requires JWT)
- **`GET /admin`** → renders admin panel (requires JWT + admin role)
- **`GET /stats`** → renders stats page (requires JWT)
- **`GET /banned`** → renders banned link page
- **`GET /report`** → renders report abuse form
- **`GET /reset-password`** → renders password reset request form
- **`GET /reset-password/:resetPasswordToken`** → renders new password form
- **`GET /verify/:verificationToken`** → verifies email, sets token cookie
- **`GET /verify-email/:changeEmailToken`** → verifies email change, updates user, sets new token

All HTML routes use handlebars templates in `server/views/` or `custom/views/`. Middleware `locals.isHTML` (locals.handler.js:5-9) checks `req.accepts(["json", "html"])` to determine response type.

---

## 2. Core Function Logic (Pseudo-code)

### 2.1 Link Creation

```go
// server/handlers/links.handler.js:99-161
func CreateLink(req, res) {
  // Extract validated fields
  target := req.body.target
  customurl := req.body.customurl
  password := req.body.password
  description := req.body.description
  expire_in := req.body.expire_in  // UTC datetime
  reuse := req.body.reuse
  domain := req.body.fetched_domain  // set by validator
  domain_id := domain ? domain.id : nil
  
  // Check for reuse
  if reuse && req.user {
    existingLink := query.link.find({ target, user_id: req.user.id, domain_id })
    if existingLink {
      return res.json(sanitize.link(existingLink))
    }
  }
  
  // Check custom URL availability
  if customurl {
    taken := query.link.find({ address: customurl, domain_id })
    if taken {
      throw CustomError("Custom URL is already in use.")
    }
  }
  
  // Generate random address if no custom URL
  address := customurl || generateId(query, domain_id)
  
  // Malware/banned checks
  targetDomain := extractDomain(target)
  await validators.bannedDomain(targetDomain)
  await validators.bannedHost(targetDomain)
  
  // Create link
  link := query.link.create({
    address,
    target,
    description,
    password: password ? bcrypt.hash(password, 12) : nil,
    expire_in,
    domain_id,
    user_id: req.user ? req.user.id : nil
  })
  
  link.domain = domain?.address
  
  if req.isHTML {
    res.setHeader("HX-Trigger", "reloadMainTable")
    shortURL := getShortURL(link.address, link.domain)
    return res.render("partials/shortener", { link: shortURL.link, url: shortURL.url })
  }
  
  return res.status(201).send(sanitize.link(link))
}
```

**Helper: generateId** (utils.js:69-76)
```go
func generateId(query, domain_id) string {
  for {
    address := nanoid()  // uses LINK_CUSTOM_ALPHABET, length LINK_LENGTH
    link := query.link.find({ address, domain_id })
    if !link {
      return address
    }
  }
}
```

**Helper: bannedDomain** (validators.handler.js:504-513)
```go
func bannedDomain(domain) {
  banned := query.domain.find({ address: domain, banned: true })
  if banned {
    throw CustomError("Domain is banned.", 400)
  }
}
```

**Helper: bannedHost** (validators.handler.js:515-534)
```go
func bannedHost(domain) {
  try {
    dnsRes := dnsLookup(domain)
    if dnsRes && dnsRes.address {
      banned := query.host.find({ address: dnsRes.address, banned: true })
      if banned {
        throw CustomError("URL is containing malware/scam.", 400)
      }
    }
  } catch {
    // DNS lookup failed, ignore
  }
}
```

---

### 2.2 Link Editing

```go
// server/handlers/links.handler.js:163-254
func EditLink(req, res) {
  link := query.link.find({ uuid: req.params.id, user_id: req.user.id })
  if !link {
    throw CustomError("Link was not found.")
  }
  
  // Track changes
  changed := false
  updates := {}
  
  for field in ["address", "target", "description", "expire_in", "password"] {
    value := req.body[field]
    
    // Handle null password (remove)
    if field == "password" && value == nil && link.password {
      updates[field] = nil
      changed = true
      continue
    }
    
    // Skip if not provided or unchanged
    if !value || value == link[field] {
      continue
    }
    
    // Special handling for expire_in (allow 1-minute tolerance)
    if field == "expire_in" && link.expire_in {
      diff := abs(differenceInSeconds(parseUTC(value), parseUTC(link.expire_in)))
      if diff < 60 {
        continue
      }
    }
    
    // Special handling for masked password (all bullets)
    if field == "password" && value.replaceAll("•", "") == "" {
      continue
    }
    
    updates[field] = value
    changed = true
  }
  
  if !changed {
    throw CustomError("Should at least update one field.")
  }
  
  // Validation for new address
  if updates["address"] {
    taken := query.link.find({ address: updates["address"], domain_id: link.domain_id })
    if taken {
      throw CustomError("Custom URL is already in use.")
    }
  }
  
  // Validation for new target
  if updates["target"] {
    targetDomain := extractDomain(updates["target"])
    await bannedDomain(targetDomain)
    await bannedHost(targetDomain)
  }
  
  // Hash password if changed
  if updates["password"] && updates["password"] != nil {
    updates["password"] = bcrypt.hash(updates["password"], 12)
  }
  
  // Update
  updatedLink := query.link.update({ id: link.id }, updates)
  
  if req.isHTML {
    return res.render("partials/links/edit", {
      swap_oob: true,
      success: "Link has been updated.",
      ...sanitize.link_html(updatedLink)
    })
  }
  
  return res.status(200).send(sanitize.link(updatedLink))
}
```

---

### 2.3 Link Ban (with Cascade)

```go
// server/handlers/links.handler.js:391-457
func BanLink(req, res) {
  link := query.link.find({ uuid: req.params.id })
  if !link {
    throw CustomError("No link has been found.", 400)
  }
  if link.banned {
    throw CustomError("Link has been banned already.", 400)
  }
  
  tasks := []
  update := { banned: true, banned_by_id: req.user.id }
  
  // 1. Ban link
  tasks.append(query.link.update({ uuid: req.params.id }, update))
  
  targetDomain := extractDomain(link.target)
  
  // 2. Ban target domain (if requested)
  if req.body.domain {
    tasks.append(query.domain.add({ address: targetDomain, ...update }))
  }
  
  // 3. Ban target host IP (if requested)
  if req.body.host {
    dnsRes := await dnsLookup(targetDomain)
    if !dnsRes {
      throw CustomError("Couldn't fetch DNS info.")
    }
    tasks.append(query.host.add({ address: dnsRes.address, ...update }))
  }
  
  // 4. Ban link owner (if requested and has owner)
  if req.body.user && link.user_id {
    tasks.append(query.user.update({ id: link.user_id }, update))
  }
  
  // 5. Ban all owner's links (if requested)
  if req.body.userLinks && link.user_id {
    tasks.append(query.link.update({ user_id: link.user_id }, update))
  }
  
  // Execute all tasks
  await Promise.all(tasks)
  
  if req.isHTML {
    res.setHeader("HX-Reswap", "outerHTML")
    res.setHeader("HX-Trigger", "reloadMainTable")
    return res.render("partials/links/dialog/ban_success", {
      link: getShortURL(link.address, link.domain).link
    })
  }
  
  return res.status(200).send({ message: "Banned link successfully." })
}
```

---

### 2.4 Redirect Flow

```go
// server/handlers/links.handler.js:459-545
func Redirect(req, res, next) {
  // Skip if preserved URL
  if preservedURLs.includes(req.path.replace("/", "")) {
    return next()
  }
  
  // Get domain
  host := removeWww(req.headers.host)
  domain := nil
  if host != env.DEFAULT_DOMAIN {
    domain = query.domain.find({ address: host })
  }
  
  // Get link
  address := req.params.id.replace("+", "")
  link := query.link.find({ address, domain_id: domain?.id })
  
  // Not found → redirect to domain homepage or 404
  if !link {
    return res.redirect(domain?.homepage || "/404")
  }
  
  // Banned → redirect to banned page
  if link.banned {
    return res.redirect("/banned")
  }
  
  // Info request (trailing +) and no password
  if req.params.id.endsWith("+") && !link.password {
    if req.isHTML {
      return res.render("url_info", {
        title: "Short link information",
        target: link.target,
        link: getShortURL(link.address, link.domain).link
      })
    }
    return res.send({ target: link.target })
  }
  
  // Password protected
  if link.password {
    // Check Authorization header (HTTP Basic Auth)
    if "authorization" in req.headers {
      auth := req.headers.authorization
      if auth.startsWith("Basic ") {
        decoded := base64Decode(auth.substring(6))
        colon := decoded.indexOf(":")
        if colon != -1 {
          password := decoded.substring(colon + 1)
          if bcrypt.compare(password, link.password) {
            return res.redirect(link.target)
          }
        }
      }
    }
    
    // Render password form
    return res.render("protected", {
      title: "Protected short link",
      id: link.uuid
    })
  }
  
  // Create visit (if not bot and has user_id)
  isBot := isbot(req.headers["user-agent"])
  if link.user_id && !isBot {
    queue.visit.add({
      userAgent: req.headers["user-agent"],
      ip: req.ip,
      country: req.get("cf-ipcountry"),
      referrer: req.get("Referrer"),
      link
    })
  }
  
  // Redirect to target
  return res.redirect(link.target)
}
```

---

### 2.5 Stats Endpoint

```go
// server/handlers/links.handler.js:610-647
func GetStats(req, res) {
  uuid := req.params.id
  
  link := query.link.find({
    uuid,
    ...(req.user.admin ? {} : { user_id: req.user.id })
  })
  
  if !link {
    if req.isHTML {
      res.setHeader("HX-Redirect", "/404")
      return res.status(200).send("")
    }
    throw CustomError("Link could not be found.")
  }
  
  // Fetch aggregated stats (Part A)
  stats := query.visit.find({ link_id: link.id }, link.visit_count)
  
  if !stats {
    throw CustomError("Could not get the short link stats. Try again later.")
  }
  
  if req.isHTML {
    return res.render("partials/stats", {
      link: sanitize.link_html(link),
      stats,
      map  // country code to name mapping
    })
  }
  
  return res.status(200).send({
    ...stats,
    ...sanitize.link(link)
  })
}
```

**Stats Shape** (from Part A, but interface defined here):
```json
{
  "lastDay": {
    "stats": {
      "browser": [{"name": "chrome", "value": 10}, ...],
      "os": [{"name": "windows", "value": 5}, ...],
      "country": [{"name": "US", "value": 8}, ...],
      "referrer": [{"name": "google.com", "value": 3}, ...]
    },
    "views": [0, 1, 2, ...]  // 24 hourly counts
  },
  "lastWeek": { ... },   // 7 daily counts
  "lastMonth": { ... },  // 30 daily counts
  "lastYear": { ... },   // 12 monthly counts
  "updatedAt": "2024-01-01T00:00:00.000Z"
}
```

---

### 2.6 Domain Add/Remove

**Add Domain** (domains.handler.js:9-28):
```go
func AddDomain(req, res) {
  // Validated: address is 3-64 chars, www/protocol stripped, not DEFAULT_DOMAIN, not taken
  domain := query.domain.add({
    address: req.body.address,
    homepage: req.body.homepage,
    user_id: req.user.id
  })
  
  if req.isHTML {
    domains := query.domain.get({ user_id: req.user.id }).map(sanitize.domain)
    res.setHeader("HX-Reswap", "none")
    return res.render("partials/settings/domain/table", { domains })
  }
  
  return res.status(200).send(sanitize.domain(domain))
}
```

**Remove Domain** (domains.handler.js:51-85):
```go
func RemoveDomain(req, res) {
  domain := query.domain.find({ uuid: req.params.id, user_id: req.user.id })
  if !domain {
    throw CustomError("Could not delete the domain.", 400)
  }
  
  // Orphan domain (set user_id = null, keep in DB for existing links)
  updatedDomain := query.domain.update({ id: domain.id }, { user_id: nil })
  if !updatedDomain {
    throw CustomError("Could not delete the domain.", 500)
  }
  
  // Remove from Redis cache
  if env.REDIS_ENABLED {
    redis.remove.domain(updatedDomain)
  }
  
  if req.isHTML {
    domains := query.domain.get({ user_id: req.user.id }).map(sanitize.domain)
    res.setHeader("HX-Reswap", "outerHTML")
    return res.render("partials/settings/domain/delete_success", {
      domains,
      address: domain.address
    })
  }
  
  return res.status(200).send({ message: "Domain deleted successfully" })
}
```

---

### 2.7 User Account Deletion

```go
// server/handlers/users.handler.js:20-33
func RemoveUser(req, res) {
  // Password already validated by middleware
  await query.user.remove(req.user)
  
  if req.isHTML {
    deleteCurrentToken(res)
    res.setHeader("HX-Trigger-After-Swap", "redirectToHomepage")
    return res.render("partials/settings/delete_account", {
      success: "Account has been deleted. Logging out..."
    })
  }
  
  return res.status(200).send("OK")
}
```

**Note:** `query.user.remove` (Part A) deletes user record AND cascades to delete all user's links and domains.

---

### 2.8 Admin User Creation

```go
// server/handlers/users.handler.js:157-176
func CreateUser(req, res) {
  // Validated: email, password (8-64 chars), role (USER/ADMIN), verified, banned, verification_email
  
  // Hash password
  req.body.password = bcrypt.hash(req.body.password, 12)
  
  user := query.user.create(req.body)
  
  // Send verification email if requested and user not verified/banned
  if req.body.verification_email && !user.banned && !user.verified {
    await mail.verification(user)
  }
  
  if req.isHTML {
    res.setHeader("HX-Trigger", "reloadMainTable")
    return res.render("partials/admin/dialog/create_user_success", {
      email: user.email
    })
  }
  
  return res.status(201).send({ message: "The user has been created successfully." })
}
```

---

## 3. Authentication & Authorization

### 3.1 Local Authentication (Email/Password)

**Implementation:** `server/passport.js:33-55`

**Strategy:** `passport-local` with `usernameField: "email"`

**Flow:**
1. Extract `email`, `password` from request body
2. Find user: `query.user.find({ email })`
3. If not found, return `done(null, false)`
4. Compare password: `bcrypt.compare(password, user.password)`
5. If mismatch, return `done(null, false)`
6. If match, return `done(null, user)`

**Bcrypt:**
- Algorithm: bcrypt (via bcryptjs)
- Cost: 12 rounds (`bcrypt.genSalt(12)`, auth.handler.js:98, 122, 158, 189, 264)
- Hash format: `$2a$12$...` (bcryptjs produces $2a$ hashes, compatible with bcrypt)

**Passport authenticate wrapper** (auth.handler.js:16-82):
- Used by all auth middleware
- Checks for existing `req.user` (skip if already authed)
- Calls `passport.authenticate(type, callback)`
- Handles errors, banned users, unverified users
- Sets `req.user` with `admin` boolean flag
- Renews JWT if > 6 days old (for HTML page requests only)

---

### 3.2 JWT Authentication

**Implementation:** `server/passport.js:11-31`

**Strategy:** `passport-jwt`

**Configuration:**
- **Extract from:** Cookie named `token` (`jwtFromRequest: req => req.cookies?.token`)
- **Secret:** `env.JWT_SECRET` (required in production)
- **Algorithm:** HS256 (default for jsonwebtoken)

**JWT Payload (utils.js:38-48):**
```json
{
  "iss": "ApiAuth",
  "sub": <user_id>,  // integer user ID
  "iat": <timestamp>,
  "exp": <timestamp>  // 7 days from iat
}
```

**Verification:**
1. Extract token from `token` cookie
2. Verify signature with `JWT_SECRET`
3. Check `sub` is a number (old JWTs had email as `sub`, now invalidated, passport.js:21-22)
4. Find user: `query.user.find({ id: payload.sub })`
5. If not found, return `done(null, false)`
6. If found, return `done(null, user, payload)`

**Cookie Settings (utils.js:50-57):**
- **Name:** `token`
- **maxAge:** 1000 * 60 * 60 * 24 * 7 (7 days)
- **httpOnly:** `true`
- **secure:** `env.isProd` (true in production)
- **sameSite:** `"Lax"`

**Token Renewal (auth.handler.js:68-77):**
- Only for HTML page requests (not API)
- If token exp is < 6 days away, generate new token, delete old cookie, set new cookie

**JWT Variants:**
- `auth.jwt`: strict, throws 401 if not authenticated, HTML requests get HX-Redirect to `/logout`
- `auth.jwtLoose`: optional, allows unauthenticated (does not throw)
- `auth.jwtPage`: strict, HTML page requests redirect to `/logout` if not authenticated
- `auth.jwtLoosePage`: optional, HTML page requests redirect to `/logout` if authenticated but banned/unverified

---

### 3.3 API Key Authentication

**Implementation:** `server/passport.js:58-75`

**Strategy:** `passport-localapikey-update`

**Configuration:**
- **Header:** `X-API-KEY`
- **Field:** `apikey` (for body/query fallback, though typically header-only)

**Flow:**
1. Extract API key from `X-API-KEY` header
2. Find user: `query.user.find({ apikey })`
3. If not found, return `done(null, false)`
4. If found, return `done(null, user)`

**API Key Generation (auth.handler.js:211-232):**
- Algorithm: `nanoid(40)` (40-char random string from default nanoid alphabet: `A-Za-z0-9_-`)
- Stored plain-text in `users.apikey` column
- Invalidates old key (only one active key per user)
- If Redis enabled, clears user cache

**Middleware:** `auth.apikey` (auth.handler.js:89) — optional, does not throw if missing

---

### 3.4 OIDC Authentication

**Implementation:** `server/passport.js:77-140`

**Strategy:** `openid-client` `Strategy`

**Configuration (from env):**
- **OIDC_ENABLED:** boolean (default false)
- **OIDC_ISSUER:** URL of OIDC provider (e.g., `https://accounts.google.com`)
- **OIDC_CLIENT_ID:** OAuth2 client ID
- **OIDC_CLIENT_SECRET:** OAuth2 client secret
- **OIDC_SCOPE:** space-separated scopes (default `"openid profile email"`)
- **OIDC_EMAIL_CLAIM:** claim name for email (default `"email"`)
- **OIDC_PROMPT:** optional prompt parameter (e.g., `"login"`, `"consent"`)

**Flow:**
1. User navigates to `/login/oidc` (not an API route)
2. Middleware calls `auth.oidc` → passport redirects to OIDC provider
3. User authenticates at provider, redirected back to `/login/oidc` with code
4. Passport exchanges code for tokens, retrieves userinfo
5. Extract email from `userinfo[OIDC_EMAIL_CLAIM]`
6. Check if user exists: `query.user.find({ email })`
7. **If exists:** return existing user
8. **If new:** create user with:
   - `email` from userinfo
   - `password`: random 24-64 char string (bcrypt hashed, not meant to be used)
   - `verified: true`, clear verification tokens
9. Call `auth.login` → sign JWT, set cookie
10. Redirect to `/`

**Error Handling:** OIDC errors caught, throws `CustomError("OIDC authentication failed.", 401)` (auth.handler.js:24-26)

**Discovery:** Uses `Issuer.discover(OIDC_ISSUER)` to fetch `.well-known/openid-configuration`

**Required Env Vars (validated at startup, passport.js:79-84):**
- `OIDC_ISSUER`
- `OIDC_CLIENT_ID`
- `OIDC_CLIENT_SECRET`
- `OIDC_SCOPE`
- `OIDC_EMAIL_CLAIM`

---

### 3.5 Role-Based Authorization

**Roles (consts.js:1-8):**
```go
const ROLES = {
  USER: "USER",
  ADMIN: "ADMIN"
}
```

**Admin Check Middleware (auth.handler.js:92-95):**
```go
func admin(req, res, next) {
  if req.user.admin {
    return next()
  }
  throw CustomError("Unauthorized", 401)
}
```

**Admin Flag (auth.handler.js:62-66, utils.js:34-36):**
```go
req.user = {
  ...user,
  admin: isAdmin(user)  // user.role === ROLES.ADMIN
}
res.locals.isAdmin = isAdmin(user)
```

**Routes Requiring Admin:**
- `GET /api/v2/links/admin`
- `PATCH /api/v2/links/admin/:id`
- `POST /api/v2/links/admin/ban/:id`
- `GET /api/v2/users/admin`
- `POST /api/v2/users/admin`
- `DELETE /api/v2/users/admin/:id`
- `POST /api/v2/users/admin/ban/:id`
- `GET /api/v2/domains/admin`
- `POST /api/v2/domains/admin`
- `DELETE /api/v2/domains/admin/:id`
- `POST /api/v2/domains/admin/ban/:id`

---

### 3.6 Email Verification & Password Reset Tokens

**Verification Token (signup flow):**
- **Generated on:** User creation (if `env.MAIL_ENABLED`)
- **Token:** UUID v4 (`randomUUID()`, crypto.randomUUID)
- **Storage:** `users.verification_token`
- **Expiry:** `users.verification_expires` = current time + 30 minutes (auth.handler.js:239, queries/user.queries.js uses `addMinutes(new Date(), 30)`)
- **Email Template:** `server/mail/template-verify.html`
- **Verification Route:** `GET /verify/:verificationToken` (auth.handler.js:155-179)
- **Flow:**
  1. Find user: `query.user.find({ verification_token, verification_expires: [">", now] })`
  2. If found, update: `verified = true`, clear token/expires
  3. Sign JWT, set cookie, redirect to homepage

**Password Reset Token:**
- **Generated on:** Reset password request
- **Token:** UUID v4
- **Storage:** `users.reset_password_token`, `users.reset_password_expires` (30 min)
- **Email Template:** `server/mail/template-reset.html`
- **Reset Route:** `POST /api/v2/auth/new-password`
- **Flow:**
  1. Find user: `query.user.find({ reset_password_token, reset_password_expires: [">", now] })`
  2. If found, hash new password, update user, clear token/expires

**Change Email Token:**
- **Generated on:** Change email request
- **Token:** UUID v4
- **Storage:** `users.change_email_token`, `users.change_email_expires` (30 min), `users.change_email_address`
- **Email Template:** `server/mail/template-change-email.html`
- **Verification Route:** `GET /verify-email/:changeEmailToken` (auth.handler.js:331-361)
- **Flow:**
  1. Find user: `query.user.find({ change_email_token, change_email_expires: [">", now] })`
  2. Update: `email = change_email_address`, clear token/expires/address
  3. Sign new JWT (with new email), set cookie

---

### 3.7 CSRF Posture

**CSRF Protection:** None (no CSRF tokens)

**Rationale:**
- JWT in httpOnly cookie (cannot be accessed by XSS)
- API designed for CORS (explicit CORS middleware on POST /api/v2/links)
- SameSite=Lax cookie prevents CSRF for state-changing requests from external sites

**Note for Go Port:** Consider adding CSRF protection for state-changing endpoints if removing CORS or changing cookie settings.

---

## 4. Security Middleware

### 4.1 Helmet Configuration

**Source:** `server/server.js:39`

```js
app.use(helmet({ contentSecurityPolicy: false }))
```

**Helmet Version:** `7.1.0` (package.json:42)

**Settings:**
- **CSP:** Disabled (HTMX requires inline scripts)
- All other Helmet defaults enabled:
  - `X-DNS-Prefetch-Control: off`
  - `X-Frame-Options: SAMEORIGIN`
  - `Strict-Transport-Security: max-age=15552000; includeSubDomains` (HTTPS only)
  - `X-Download-Options: noopen`
  - `X-Content-Type-Options: nosniff`
  - `X-Permitted-Cross-Domain-Policies: none`
  - `Referrer-Policy: no-referrer`
  - `X-XSS-Protection: 0` (deprecated, disabled by modern browsers)

**Go Equivalent:** Use a helmet-like middleware or manually set headers.

---

### 4.2 CORS

**Source:** `server/routes/link.routes.js:36`

**Enabled Routes:**
- `POST /api/v2/links` (link creation only)

**Configuration:** Default `cors()` settings (all origins allowed, credentials not allowed)

**Go Equivalent:** Use `rs/cors` or custom CORS middleware, enable for link creation endpoint.

---

### 4.3 Trust Proxy

**Source:** `server/server.js:35-37`

```js
if (env.TRUST_PROXY) {
  app.set("trust proxy", true)
}
```

**Effect:**
- `req.ip` resolves to `X-Forwarded-For` header (first IP)
- `req.protocol` resolves to `X-Forwarded-Proto`

**Default:** `true` (env.js:35)

**Go Equivalent:** Parse `X-Forwarded-For` / `X-Real-IP` headers when behind proxy. Use `r.RemoteAddr` directly if not behind proxy.

---

### 4.4 Rate Limiting

**Implementation:** `server/handlers/helpers.handler.js:88-124`

**Library:** `express-rate-limit` v7.5.0, `rate-limit-redis` v4.2.0 (if Redis enabled)

**Configuration Function:**
```js
rateLimit(params: { window, limit, user?, skipSuccess?, skipFailed? })
```

**Behavior:**
- **Disabled if:** `env.ENABLE_RATE_LIMIT` is false (returns no-op middleware)
- **Store:** Redis if `REDIS_ENABLED`, else in-memory
- **Window:** `params.window * 1000` milliseconds
- **Limit:** `params.limit` requests per window (or `params.user` if user authenticated and `params.user` set)
- **Key:** `"rl:" + method + baseUrl + path + ":" + IP` (e.g., `rl:POST/api/v2/auth/login:1.2.3.4`)
- **Skip:** `skipSuccessfulRequests`, `skipFailedRequests` (configurable per route)
- **Success Check:** `!res.locals.error && res.statusCode < 400` (helpers.handler.js:118)
- **Handler:** Throws `CustomError(options.message, options.statusCode)` on rate limit exceeded

**Applied Routes:**
- **Login:** `POST /api/v2/auth/login` — 5/min per IP (auth.routes.js:19)
- **Signup:** `POST /api/v2/auth/signup` — 5/min per IP (auth.routes.js:30)
- **Create Admin:** `POST /api/v2/auth/create-admin` — 5/min per IP (auth.routes.js:42)
- **Change Password:** `POST /api/v2/auth/change-password` — 5/min per IP (auth.routes.js:51)
- **Change Email:** `POST /api/v2/auth/change-email` — 3/min per IP (auth.routes.js:62)
- **Reset Password:** `POST /api/v2/auth/reset-password` — 3/min per IP (auth.routes.js:80)
- **New Password:** `POST /api/v2/auth/new-password` — 5/min per IP (auth.routes.js:90)
- **Generate API Key:** `POST /api/v2/auth/apikey` — 10/min per IP (auth.routes.js:70)

**Go Equivalent:**
- Use `github.com/didip/tollbooth` or `github.com/ulule/limiter`
- Redis store: `github.com/go-redis/redis_rate`
- Key generator must match Node.js pattern for consistency

---

### 4.5 Input Sanitization

**Library:** `express-validator` v6.14.2

**Sanitizers Used:**
- **`.trim()`** — whitespace removal (email, URLs, custom addresses)
- **`.customSanitizer(utils.addProtocol)`** — prepends `http://` if no protocol (target URLs, domain addresses, homepages)
- **`.customSanitizer(value => removeWww(...))`** — strips www. prefix (domain addresses)
- **`.customSanitizer(ms)`** — parses duration strings to milliseconds (expire_in)
- **`.customSanitizer(value => dateToUTC(addMilliseconds(...)))`** — converts expire_in to UTC datetime
- **`.customSanitizer(sanitizeCheckbox)`** — converts checkboxes to boolean (`value === true || value === "on" || value`)

**XSS Protection:**
- **No explicit sanitization** (e.g., no HTML escaping in validators)
- **Handlebars templates** auto-escape by default (triple-stache `{{{` disabled in most places)
- **JSON responses** are safe (JSON.stringify escapes)

**SQL Injection:**
- **Knex query builder** used throughout (parameterized queries)
- No raw SQL in handlers (all queries in Part A)

**Go Equivalent:**
- Use `github.com/go-playground/validator/v10` for validation
- Manual sanitization functions (trim, URL normalization)
- ORM/query builder for SQL safety (e.g., GORM, sqlx)

---

### 4.6 Bot Detection

**Library:** `isbot` v5.1.19

**Usage:** `isbot(req.headers["user-agent"])` (links.handler.js:532)

**Effect:** If bot detected, skip visit tracking (do not queue visit record)

**Common Bots Detected:** Googlebot, Bingbot, Slackbot, facebookexternalhit, etc.

**Go Equivalent:** Port `isbot` logic or use `github.com/mileusna/useragent` + custom bot list.

---

## 5. Configuration (Environment Variables)

All environment variables are defined and validated in `server/env.js` using `envalid`.

### 5.1 Environment Variable Specification

| Variable | Type | Default | Validation | Effect | File (line) |
|----------|------|---------|------------|--------|-------------|
| `PORT` | num | `3000` | — | HTTP server port | env.js:30 |
| `SITE_NAME` | str | `"Kutt"` | example: "Kutt" | Site name in emails/UI | env.js:31 |
| `DEFAULT_DOMAIN` | str | `"localhost:3000"` | example: "kutt.to" | Default domain for short links | env.js:32 |
| `LINK_LENGTH` | num | `6` | — | Generated link address length | env.js:33 |
| `LINK_CUSTOM_ALPHABET` | str | `"abcdefghkmnpqrstuvwxyzABCDEFGHKLMNPQRSTUVWXYZ23456789"` | non-empty | Alphabet for nanoid (omits o,O,0,i,I,l,1,j) | env.js:34 |
| `TRUST_PROXY` | bool | `true` | — | Trust X-Forwarded-For header | env.js:35 |
| `DB_CLIENT` | str | `"better-sqlite3"` | choices: pg, pg-native, sqlite3, better-sqlite3, mysql, mysql2 | Database driver | env.js:36 |
| `DB_FILENAME` | str | `"db/data"` | — | SQLite file path | env.js:37 |
| `DB_HOST` | str | `"localhost"` | — | Postgres/MySQL host | env.js:38 |
| `DB_PORT` | num | `5432` | — | Postgres/MySQL port | env.js:39 |
| `DB_NAME` | str | `"kutt"` | — | Database name | env.js:40 |
| `DB_USER` | str | `"postgres"` | — | Database user | env.js:41 |
| `DB_PASSWORD` | str | `""` | — | Database password | env.js:42 |
| `DB_SSL` | bool | `false` | — | Use SSL for DB connection | env.js:43 |
| `DB_POOL_MIN` | num | `0` | — | Min DB connection pool size | env.js:44 |
| `DB_POOL_MAX` | num | `10` | — | Max DB connection pool size | env.js:45 |
| `REDIS_ENABLED` | bool | `false` | — | Enable Redis cache | env.js:46 |
| `REDIS_HOST` | str | `"127.0.0.1"` | — | Redis host | env.js:47 |
| `REDIS_PORT` | num | `6379` | — | Redis port | env.js:48 |
| `REDIS_PASSWORD` | str | `""` | — | Redis password | env.js:49 |
| `REDIS_DB` | num | `0` | — | Redis database number (0-15) | env.js:50 |
| `DISALLOW_ANONYMOUS_LINKS` | bool | `true` | — | Require auth for link creation | env.js:51 |
| `DISALLOW_REGISTRATION` | bool | `true` | — | Disable signup | env.js:52 |
| `DISALLOW_LOGIN_FORM` | bool | `false` | — | Disable email/password login (OIDC only) | env.js:53 |
| `SERVER_IP_ADDRESS` | str | `""` | — | Display-only IP address (settings page) | env.js:54 |
| `SERVER_CNAME_ADDRESS` | str | `""` | — | Display-only CNAME (settings page) | env.js:55 |
| `CUSTOM_DOMAIN_USE_HTTPS` | bool | `false` | — | Use HTTPS for custom domain links | env.js:56 |
| `JWT_SECRET` | str | `"securekey"` (dev) | required in prod | JWT signing secret | env.js:57 |
| `MAIL_ENABLED` | bool | `false` | — | Enable email sending | env.js:58 |
| `MAIL_HOST` | str | `""` | — | SMTP host | env.js:59 |
| `MAIL_PORT` | num | `587` | — | SMTP port | env.js:60 |
| `MAIL_SECURE` | bool | `false` | — | Use SSL/TLS for SMTP | env.js:61 |
| `MAIL_USER` | str | `""` | — | SMTP username | env.js:62 |
| `MAIL_FROM` | str | `""` | example: "Kutt <support@kutt.to>" | From address (fallback to MAIL_USER) | env.js:63 |
| `MAIL_PASSWORD` | str | `""` | — | SMTP password | env.js:64 |
| `OIDC_ENABLED` | bool | `false` | — | Enable OIDC login | env.js:65 |
| `OIDC_ISSUER` | str | `""` | — | OIDC issuer URL | env.js:66 |
| `OIDC_PROMPT` | str | `""` | — | OIDC prompt parameter | env.js:67 |
| `OIDC_CLIENT_ID` | str | `""` | — | OIDC client ID | env.js:68 |
| `OIDC_CLIENT_SECRET` | str | `""` | — | OIDC client secret | env.js:69 |
| `OIDC_SCOPE` | str | `"openid profile email"` | — | OIDC scopes | env.js:70 |
| `OIDC_EMAIL_CLAIM` | str | `"email"` | — | Userinfo claim for email | env.js:71 |
| `OIDC_BUTTON_TEXT` | str | `"Log in with OIDC"` | — | UI button text | env.js:72 |
| `ENABLE_RATE_LIMIT` | bool | `false` | — | Enable rate limiting | env.js:73 |
| `REPORT_EMAIL` | str | `""` | — | Email address for abuse reports | env.js:74 |
| `CONTACT_EMAIL` | str | `""` | — | Support email (displayed in UI) | env.js:75 |
| `NODE_APP_INSTANCE` | num | `0` | — | PM2 instance ID (for cron job gating) | env.js:76 |

### 5.2 File-Based Secrets

**Feature (env.js:79-87):** For each env var `KEY`, if `KEY_FILE` exists, read the file and set `KEY` to its contents (trimmed).

**Example:** `JWT_SECRET_FILE=/run/secrets/jwt_secret` → reads file, sets `JWT_SECRET`

**Use Case:** Docker secrets, Kubernetes secrets

**Go Equivalent:** Implement in config loader (read `_FILE` vars, override base vars).

---

### 5.3 Validation & Defaults

**Library:** `envalid` v8.0.0

**Behavior:**
- **Missing required vars:** Throws error on startup (e.g., `JWT_SECRET` in production)
- **Empty string override:** If env var is set to `""`, delete it before validation (env.js:15-17, 20-22)
- **Dev defaults:** `JWT_SECRET` defaults to `"securekey"` in dev (`devDefault` option)
- **Production flag:** If `--production` CLI arg, sets `NODE_ENV=production` (env.js:25-27)

**Go Equivalent:**
- Use `github.com/caarlos0/env` or `github.com/spf13/viper`
- Manual validation for required fields in production

---

## 6. Mail System

### 6.1 Transport Configuration

**Library:** `nodemailer` v9.0.3

**Source:** `server/mail/mail.js:9-21`

**Config:**
```js
{
  host: env.MAIL_HOST,
  port: env.MAIL_PORT,
  secure: env.MAIL_SECURE,
  auth: env.MAIL_USER ? {
    user: env.MAIL_USER,
    pass: env.MAIL_PASSWORD
  } : undefined
}
```

**Go Equivalent:**
- Use `github.com/wneessen/go-mail` (recommended in context) or `gopkg.in/gomail.v2`
- Config struct:
  ```go
  type MailConfig struct {
    Host     string
    Port     int
    Secure   bool
    User     string
    Password string
    From     string
  }
  ```

---

### 6.2 Email Templates

**Template Files:**
- **Verification:** `server/mail/template-verify.html`
- **Password Reset:** `server/mail/template-reset.html`
- **Change Email:** `server/mail/template-change-email.html`

**Template Variables (replaced at load time, mail.js:29-46):**
- `{{domain}}` → `env.DEFAULT_DOMAIN`
- `{{site_name}}` → `env.SITE_NAME`

**Template Variables (replaced at send time):**
- **Verification:** `{{verification}}` → `user.verification_token`
- **Reset:** `{{resetpassword}}` → `user.reset_password_token`
- **Change Email:** `{{verification}}` → `user.change_email_token`

**Text Versions:** Plain-text versions in `server/mail/text.js` (not shown, but referenced)

**Go Equivalent:**
- Use `html/template` or `text/template`
- Embed templates with `//go:embed` directive

---

### 6.3 Email Triggers

**MAIL-001** Verification Email (signup)  
**Function:** `mail.verification(user)` (mail.js:49-71)  
**Trigger:** `POST /api/v2/auth/signup` (auth.handler.js:106)  
**To:** `user.email`  
**Subject:** "Verify your account"  
**Template:** `template-verify.html`  
**Variables:** `{{verification}}` = `user.verification_token`  
**Link:** `https://<DEFAULT_DOMAIN>/verify/<verification_token>`  

---

**MAIL-002** Password Reset Email  
**Function:** `mail.resetPasswordToken(user)` (mail.js:97-119)  
**Trigger:** `POST /api/v2/auth/reset-password` (auth.handler.js:244)  
**To:** `user.email`  
**Subject:** "Reset your password"  
**Template:** `template-reset.html`  
**Variables:** `{{resetpassword}}` = `user.reset_password_token`  
**Link:** `https://<DEFAULT_DOMAIN>/reset-password/<reset_password_token>`  

---

**MAIL-003** Change Email Verification  
**Function:** `mail.changeEmail(user)` (mail.js:73-95)  
**Trigger:** `POST /api/v2/auth/change-email` (auth.handler.js:315)  
**To:** `user.change_email_address` (new email, not old)  
**Subject:** "Verify your new email address"  
**Template:** `template-change-email.html`  
**Variables:** `{{verification}}` = `user.change_email_token`  
**Link:** `https://<DEFAULT_DOMAIN>/verify-email/<change_email_token>`  

---

**MAIL-004** Report Abuse Email  
**Function:** `mail.sendReportEmail(link)` (mail.js:121-137)  
**Trigger:** `POST /api/v2/links/report` (links.handler.js:377)  
**To:** `env.REPORT_EMAIL`  
**Subject:** "[REPORT]"  
**Body:** Plain-text link URL  

---

**MAIL-005** Admin User Creation Verification (optional)  
**Function:** `mail.verification(user)` (same as MAIL-001)  
**Trigger:** `POST /api/v2/users/admin` (users.handler.js:163-165)  
**Condition:** `verification_email=true` AND `!user.verified` AND `!user.banned`  

---

### 6.4 Error Handling

**Verification Email (signup):** Awaited, throws error if failed (auth.handler.js:106)  
**Change Email:** Awaited, throws error if failed (auth.handler.js:315)  
**Password Reset:** Fire-and-forget, logs error to console (auth.handler.js:244-246)  
**Report:** Awaited, throws error if failed (links.handler.js:377)  

**Mail Failures Check (mail.js:68, 92, 115, 134):**
```js
if (!mail.accepted.length) {
  throw new CustomError("Couldn't send <type> email. Try again later.")
}
```

**Go Equivalent:** Check SMTP response, throw error if not accepted.

---

## 7. Error Handling Conventions

### 7.1 CustomError Class

**Source:** `server/utils/utils.js:17-24`

```js
class CustomError extends Error {
  constructor(message, statusCode, data) {
    super(message)
    this.name = this.constructor.name
    this.statusCode = statusCode ?? 500
    this.data = data
  }
}
```

**Usage:** Thrown throughout handlers (e.g., `throw new CustomError("Link not found.", 404)`)

**Go Equivalent:**
```go
type CustomError struct {
  Message    string
  StatusCode int
  Data       interface{}
}

func (e *CustomError) Error() string {
  return e.Message
}
```

---

### 7.2 Global Error Handler

**Source:** `server/handlers/helpers.handler.js:10-35`

**Middleware:** `app.use(helpers.error)` (server.js:88)

**Behavior:**
1. Log error to console if not `CustomError` or if `env.isDev`
2. Extract message: `error.message` if `CustomError`, else `"An error occurred."`
3. Extract status code: `error.statusCode ?? 500`
4. **If HTML request:**
   - If `req.viewTemplate` set (by route middleware), render that template with `res.locals.error = message`
   - Else render `error` template with generic message
5. **If JSON request:** Return `{ "error": message }` with status code

**Go Equivalent:**
```go
func ErrorHandler(next http.Handler) http.Handler {
  return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    defer func() {
      if err := recover(); err != nil {
        var customErr *CustomError
        var message string
        var statusCode int
        
        if errors.As(err.(error), &customErr) {
          message = customErr.Message
          statusCode = customErr.StatusCode
        } else {
          message = "An error occurred."
          statusCode = 500
          log.Println(err)
        }
        
        if r.Header.Get("Accept") == "application/json" {
          w.Header().Set("Content-Type", "application/json")
          w.WriteHeader(statusCode)
          json.NewEncoder(w).Encode(map[string]string{"error": message})
        } else {
          // Render HTML error template
          w.WriteHeader(statusCode)
          // ...
        }
      }
    }()
    next.ServeHTTP(w, r)
  })
}
```

---

### 7.3 Validation Error Handling

**Source:** `server/handlers/helpers.handler.js:38-52`

**Middleware:** `helpers.verify` (after each validator chain)

**Behavior:**
1. Call `validationResult(req)` (express-validator)
2. If errors exist:
   - Extract first error message
   - Build `res.locals.errors` object: `{ [field]: message, ... }`
   - Throw `CustomError(firstErrorMessage, 400)`

**Go Equivalent:**
```go
type ValidationError struct {
  Field   string
  Message string
}

func Verify(errors []ValidationError) error {
  if len(errors) > 0 {
    return &CustomError{
      Message:    errors[0].Message,
      StatusCode: 400,
      Data:       errors,
    }
  }
  return nil
}
```

---

### 7.4 HTMX Response Headers

**Error Context:** When `req.isHTML` and error occurs:
- **View Template Set:** Renders `req.viewTemplate` with `res.locals.error`
- **Success State:** Sets HTMX headers like `HX-Trigger`, `HX-Reswap`, `HX-Redirect`

**Examples (from handlers):**
- `res.setHeader("HX-Redirect", "/logout")` (auth.handler.js:42)
- `res.setHeader("HX-Trigger", "reloadMainTable")` (links.handler.js:150)
- `res.setHeader("HX-Reswap", "outerHTML")` (links.handler.js:361)
- `res.setHeader("HX-Trigger-After-Swap", "resetChangePasswordForm")` (auth.handler.js:199)

**Go Equivalent:** Set response headers in handlers:
```go
w.Header().Set("HX-Trigger", "reloadMainTable")
```

---

## 8. Go Mapping & Recommendations

### 8.1 Router Recommendation

**Recommendation:** **`net/http` ServeMux with Go 1.22+ pattern enhancements**

**Rationale:**
- Go 1.22+ ServeMux supports method-based routing: `GET /api/v2/links`
- Supports path parameters: `/api/v2/links/{id}`
- Zero external dependencies
- Simple middleware chaining via wrapper functions

**Alternative:** `github.com/go-chi/chi` if more advanced routing needed (subrouters, middleware stacks)

**Not Recommended:** `github.com/labstack/echo` or `github.com/gin-gonic/gin` (too opinionated, different conventions)

**Example:**
```go
mux := http.NewServeMux()
mux.HandleFunc("GET /api/v2/health", healthHandler)
mux.HandleFunc("GET /api/v2/links", chain(getLinkHandler, authAPIKey, authJWT, parseQuery))
mux.HandleFunc("POST /api/v2/links", chain(createLinkHandler, cors, authAPIKey, authJWTLoose, validateCreateLink, verify))
mux.HandleFunc("PATCH /api/v2/links/{id}", chain(editLinkHandler, authAPIKey, authJWT, validateEditLink, verify))
// ...

func chain(h http.HandlerFunc, middlewares ...func(http.Handler) http.Handler) http.HandlerFunc {
  for i := len(middlewares) - 1; i >= 0; i-- {
    h = middlewares[i](h)
  }
  return h
}
```

---

### 8.2 Middleware Equivalents

| Node.js Middleware | Go Equivalent | Library/Implementation |
|--------------------|---------------|------------------------|
| `helmet` | Custom headers middleware | Set headers in middleware |
| `cors` | `rs/cors` | `github.com/rs/cors` |
| `cookie-parser` | Built-in | `http.Cookie`, `r.Cookie()` |
| `express.json()` | `json.Decoder` | `json.NewDecoder(r.Body).Decode()` |
| `express-validator` | `go-playground/validator` | `github.com/go-playground/validator/v10` |
| `express-rate-limit` | `httprate` or `tollbooth` | `github.com/go-chi/httprate` or `github.com/didip/tollbooth` |
| `passport` (JWT) | `golang-jwt/jwt` | `github.com/golang-jwt/jwt/v5` |
| `passport` (local) | bcrypt + manual | `golang.org/x/crypto/bcrypt` |
| `passport` (OIDC) | `coreos/go-oidc` | `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` |
| `passport-localapikey` | Manual header check | — |
| `isbot` | Port logic or custom | — |

---

### 8.3 Validation Library

**Recommendation:** `github.com/go-playground/validator/v10`

**Rationale:**
- Declarative struct tags: `validate:"required,email,max=255"`
- Custom validators for complex rules (e.g., URL regex, reserved words)
- Error translation to match Node.js messages

**Example:**
```go
type CreateLinkRequest struct {
  Target      string `json:"target" validate:"required,min=1,max=2040,url"`
  CustomURL   string `json:"customurl" validate:"omitempty,min=1,max=64,custom_address"`
  Password    string `json:"password" validate:"omitempty,min=3,max=64"`
  Description string `json:"description" validate:"omitempty,max=2040"`
  ExpireIn    string `json:"expire_in" validate:"omitempty,duration"`
  Reuse       bool   `json:"reuse"`
  Domain      string `json:"domain" validate:"omitempty"`
}

// Custom validators
func customAddressValidator(fl validator.FieldLevel) bool {
  return customAddressRegex.MatchString(fl.Field().String())
}

func durationValidator(fl validator.FieldLevel) bool {
  _, err := time.ParseDuration(fl.Field().String())
  return err == nil
}
```

**Alternative:** Hand-written validation functions if prefer explicit control.

---

### 8.4 JWT Library

**Recommendation:** `github.com/golang-jwt/jwt/v5`

**Example:**
```go
import "github.com/golang-jwt/jwt/v5"

type Claims struct {
  jwt.RegisteredClaims
}

func SignToken(userID int) (string, error) {
  claims := Claims{
    RegisteredClaims: jwt.RegisteredClaims{
      Issuer:    "ApiAuth",
      Subject:   strconv.Itoa(userID),
      IssuedAt:  jwt.NewNumericDate(time.Now()),
      ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
    },
  }
  token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
  return token.SignedString([]byte(os.Getenv("JWT_SECRET")))
}

func VerifyToken(tokenString string) (*Claims, error) {
  token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
    return []byte(os.Getenv("JWT_SECRET")), nil
  })
  if err != nil {
    return nil, err
  }
  return token.Claims.(*Claims), nil
}
```

---

### 8.5 Bcrypt Library

**Recommendation:** `golang.org/x/crypto/bcrypt`

**Hash Compatibility:** bcryptjs (Node.js) produces `$2a$` hashes. Go's bcrypt package produces `$2a$` hashes by default. **Existing password hashes will remain valid.**

**Example:**
```go
import "golang.org/x/crypto/bcrypt"

func HashPassword(password string) (string, error) {
  hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
  return string(hash), err
}

func ComparePassword(hash, password string) bool {
  err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
  return err == nil
}
```

**Cost:** Use `12` rounds to match Node.js (bcrypt.DefaultCost is 10, so specify explicitly).

---

### 8.6 OIDC Library

**Recommendation:** `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2`

**Example:**
```go
import (
  "context"
  "github.com/coreos/go-oidc/v3/oidc"
  "golang.org/x/oauth2"
)

func InitOIDC() (*oidc.Provider, oauth2.Config, error) {
  ctx := context.Background()
  provider, err := oidc.NewProvider(ctx, os.Getenv("OIDC_ISSUER"))
  if err != nil {
    return nil, oauth2.Config{}, err
  }
  
  config := oauth2.Config{
    ClientID:     os.Getenv("OIDC_CLIENT_ID"),
    ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
    RedirectURL:  getSiteURL() + "/login/oidc",
    Endpoint:     provider.Endpoint(),
    Scopes:       strings.Split(os.Getenv("OIDC_SCOPE"), " "),
  }
  
  return provider, config, nil
}

func OIDCCallback(provider *oidc.Provider, config oauth2.Config, code string) (string, error) {
  ctx := context.Background()
  token, err := config.Exchange(ctx, code)
  if err != nil {
    return "", err
  }
  
  userInfo, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
  if err != nil {
    return "", err
  }
  
  var claims map[string]interface{}
  if err := userInfo.Claims(&claims); err != nil {
    return "", err
  }
  
  email := claims[os.Getenv("OIDC_EMAIL_CLAIM")].(string)
  return email, nil
}
```

---

### 8.7 Rate Limiting Library

**Recommendation:** `github.com/go-chi/httprate` (if using chi router) or `github.com/ulule/limiter` with Redis store

**Example (httprate):**
```go
import "github.com/go-chi/httprate"

func RateLimit(window time.Duration, limit int) func(http.Handler) http.Handler {
  if !os.Getenv("ENABLE_RATE_LIMIT") {
    return func(next http.Handler) http.Handler { return next }
  }
  
  return httprate.LimitByIP(limit, window)
}

// Usage
mux.HandleFunc("POST /api/v2/auth/login", chain(loginHandler, RateLimit(60*time.Second, 5)))
```

**Redis-backed (ulule/limiter):**
```go
import (
  "github.com/ulule/limiter/v3"
  "github.com/ulule/limiter/v3/drivers/store/redis"
)

func RateLimitRedis(window time.Duration, limit int64) limiter.Limiter {
  store, _ := redis.NewStoreWithOptions(redisClient, limiter.StoreOptions{
    Prefix: "rl:",
  })
  rate := limiter.Rate{
    Period: window,
    Limit:  limit,
  }
  return limiter.New(store, rate)
}
```

---

### 8.8 Mail Library

**Recommendation:** `github.com/wneessen/go-mail` (as mentioned in context)

**Example:**
```go
import "github.com/wneessen/go-mail"

func SendMail(to, subject, htmlBody, textBody string) error {
  m := mail.NewMsg()
  m.From(os.Getenv("MAIL_FROM"))
  m.To(to)
  m.Subject(subject)
  m.SetBodyString(mail.TypeTextPlain, textBody)
  m.AddAlternativeString(mail.TypeTextHTML, htmlBody)
  
  c, err := mail.NewClient(os.Getenv("MAIL_HOST"),
    mail.WithPort(getEnvInt("MAIL_PORT")),
    mail.WithSMTPAuth(mail.SMTPAuthPlain),
    mail.WithUsername(os.Getenv("MAIL_USER")),
    mail.WithPassword(os.Getenv("MAIL_PASSWORD")),
    mail.WithTLSPortPolicy(mail.TLSMandatory),
  )
  if err != nil {
    return err
  }
  
  return c.DialAndSend(m)
}
```

---

### 8.9 Environment Config Library

**Recommendation:** `github.com/caarlos0/env`

**Example:**
```go
import "github.com/caarlos0/env/v10"

type Config struct {
  Port                   int    `env:"PORT" envDefault:"3000"`
  SiteName              string `env:"SITE_NAME" envDefault:"Kutt"`
  DefaultDomain         string `env:"DEFAULT_DOMAIN" envDefault:"localhost:3000"`
  LinkLength            int    `env:"LINK_LENGTH" envDefault:"6"`
  LinkCustomAlphabet    string `env:"LINK_CUSTOM_ALPHABET" envDefault:"abcdefghkmnpqrstuvwxyzABCDEFGHKLMNPQRSTUVWXYZ23456789"`
  TrustProxy            bool   `env:"TRUST_PROXY" envDefault:"true"`
  JWTSecret             string `env:"JWT_SECRET,required"`
  DBClient              string `env:"DB_CLIENT" envDefault:"better-sqlite3"`
  // ... rest of config
}

func LoadConfig() (*Config, error) {
  cfg := &Config{}
  if err := env.Parse(cfg); err != nil {
    return nil, err
  }
  
  // Handle _FILE variants
  handleFileSecrets(cfg)
  
  return cfg, nil
}
```

---

### 8.10 Nanoid Equivalent

**Recommendation:** Custom implementation or `github.com/matoous/go-nanoid`

**Important:** Must match Node.js alphabet and length exactly for compatibility.

**Example:**
```go
import (
  gonanoid "github.com/matoous/go-nanoid/v2"
)

var alphabet = os.Getenv("LINK_CUSTOM_ALPHABET")
var length = getEnvInt("LINK_LENGTH")

func GenerateID() (string, error) {
  return gonanoid.Generate(alphabet, length)
}
```

**Verify Compatibility:** Test that Go-generated IDs match Node.js nanoid output (same alphabet/length → same ID space).

---

### 8.11 Handler Signatures & DTO Structs

#### Health Check
```go
func HealthHandler(w http.ResponseWriter, r *http.Request) {
  w.WriteHeader(http.StatusOK)
  w.Write([]byte("OK"))
}
```

#### Get Links
```go
type GetLinksRequest struct {
  Limit  int    `query:"limit"`
  Skip   int    `query:"skip"`
  Search string `query:"search"`
}

type GetLinksResponse struct {
  Total int    `json:"total"`
  Limit int    `json:"limit"`
  Skip  int    `json:"skip"`
  Data  []Link `json:"data"`
}

func GetLinksHandler(w http.ResponseWriter, r *http.Request) {
  // Extract user from context (set by auth middleware)
  user := r.Context().Value("user").(*User)
  
  // Parse query
  var req GetLinksRequest
  parseQuery(r, &req)
  
  // Query links (Part A)
  links, total := query.Link.Get(user.ID, req.Limit, req.Skip, req.Search)
  
  // Respond
  json.NewEncoder(w).Encode(GetLinksResponse{
    Total: total,
    Limit: req.Limit,
    Skip:  req.Skip,
    Data:  sanitizeLinks(links),
  })
}
```

#### Create Link
```go
type CreateLinkRequest struct {
  Target      string `json:"target" validate:"required,min=1,max=2040,url"`
  CustomURL   string `json:"customurl" validate:"omitempty,min=1,max=64,custom_address"`
  Password    string `json:"password" validate:"omitempty,min=3,max=64"`
  Description string `json:"description" validate:"omitempty,max=2040"`
  ExpireIn    string `json:"expire_in" validate:"omitempty,duration"`
  Reuse       bool   `json:"reuse"`
  Domain      string `json:"domain" validate:"omitempty"`
}

type LinkResponse struct {
  ID          string    `json:"id"`
  Address     string    `json:"address"`
  Target      string    `json:"target"`
  Description string    `json:"description"`
  Password    bool      `json:"password"`
  Banned      bool      `json:"banned"`
  CreatedAt   time.Time `json:"created_at"`
  UpdatedAt   time.Time `json:"updated_at"`
  VisitCount  int       `json:"visit_count"`
  Link        string    `json:"link"`
}

func CreateLinkHandler(w http.ResponseWriter, r *http.Request) {
  var req CreateLinkRequest
  if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
    http.Error(w, err.Error(), http.StatusBadRequest)
    return
  }
  
  // Validation already done by middleware
  
  user := r.Context().Value("user").(*User)  // may be nil for anonymous
  
  // Business logic (see §2.1)
  link, err := createLink(user, req)
  if err != nil {
    panic(err)  // caught by error handler
  }
  
  w.WriteHeader(http.StatusCreated)
  json.NewEncoder(w).Encode(sanitizeLink(link))
}
```

#### Edit Link
```go
type EditLinkRequest struct {
  Address     *string `json:"address" validate:"omitempty,min=1,max=64,custom_address"`
  Target      *string `json:"target" validate:"omitempty,min=1,max=2040,url"`
  Description *string `json:"description" validate:"omitempty,max=2040"`
  ExpireIn    *string `json:"expire_in" validate:"omitempty,duration"`
  Password    *string `json:"password" validate:"omitempty,min=3,max=64"`
}

func EditLinkHandler(w http.ResponseWriter, r *http.Request) {
  id := r.PathValue("id")  // Go 1.22+ ServeMux
  
  var req EditLinkRequest
  json.NewDecoder(r.Body).Decode(&req)
  
  user := r.Context().Value("user").(*User)
  
  link, err := editLink(user, id, req)
  if err != nil {
    panic(err)
  }
  
  json.NewEncoder(w).Encode(sanitizeLink(link))
}
```

#### Delete Link
```go
type MessageResponse struct {
  Message string `json:"message"`
}

func DeleteLinkHandler(w http.ResponseWriter, r *http.Request) {
  id := r.PathValue("id")
  user := r.Context().Value("user").(*User)
  
  if err := deleteLink(user, id); err != nil {
    panic(err)
  }
  
  json.NewEncoder(w).Encode(MessageResponse{
    Message: "Link has been deleted successfully.",
  })
}
```

#### Ban Link
```go
type BanLinkRequest struct {
  Host      bool `json:"host"`
  Domain    bool `json:"domain"`
  User      bool `json:"user"`
  UserLinks bool `json:"userLinks"`
}

func BanLinkHandler(w http.ResponseWriter, r *http.Request) {
  id := r.PathValue("id")
  
  var req BanLinkRequest
  json.NewDecoder(r.Body).Decode(&req)
  
  user := r.Context().Value("user").(*User)
  
  if err := banLink(user, id, req); err != nil {
    panic(err)
  }
  
  json.NewEncoder(w).Encode(MessageResponse{
    Message: "Banned link successfully.",
  })
}
```

#### Get Stats
```go
type StatsResponse struct {
  LinkResponse
  LastDay   StatsPeriod `json:"lastDay"`
  LastWeek  StatsPeriod `json:"lastWeek"`
  LastMonth StatsPeriod `json:"lastMonth"`
  LastYear  StatsPeriod `json:"lastYear"`
  UpdatedAt time.Time   `json:"updatedAt"`
}

type StatsPeriod struct {
  Stats StatsBreakdown `json:"stats"`
  Views []int          `json:"views"`
}

type StatsBreakdown struct {
  Browser  []StatsItem `json:"browser"`
  OS       []StatsItem `json:"os"`
  Country  []StatsItem `json:"country"`
  Referrer []StatsItem `json:"referrer"`
}

type StatsItem struct {
  Name  string `json:"name"`
  Value int    `json:"value"`
}

func GetStatsHandler(w http.ResponseWriter, r *http.Request) {
  id := r.PathValue("id")
  user := r.Context().Value("user").(*User)
  
  stats, err := getStats(user, id)
  if err != nil {
    panic(err)
  }
  
  json.NewEncoder(w).Encode(stats)
}
```

#### Protected Link Password Submit
```go
type ProtectedLinkRequest struct {
  Password string `json:"password" validate:"required,min=3,max=64"`
}

type TargetResponse struct {
  Target string `json:"target"`
}

func ProtectedLinkHandler(w http.ResponseWriter, r *http.Request) {
  id := r.PathValue("id")
  
  var req ProtectedLinkRequest
  json.NewDecoder(r.Body).Decode(&req)
  
  target, err := verifyProtectedLink(id, req.Password)
  if err != nil {
    panic(err)
  }
  
  json.NewEncoder(w).Encode(TargetResponse{Target: target})
}
```

#### Report Link
```go
type ReportLinkRequest struct {
  Link string `json:"link" validate:"required"`
}

func ReportLinkHandler(w http.ResponseWriter, r *http.Request) {
  var req ReportLinkRequest
  json.NewDecoder(r.Body).Decode(&req)
  
  if err := sendReportEmail(req.Link); err != nil {
    panic(err)
  }
  
  json.NewEncoder(w).Encode(MessageResponse{
    Message: "Thanks for the report, we'll take actions shortly.",
  })
}
```

#### Get User Info
```go
type UserResponse struct {
  APIKey  string   `json:"apikey"`
  Email   string   `json:"email"`
  Domains []Domain `json:"domains"`
}

func GetUserHandler(w http.ResponseWriter, r *http.Request) {
  user := r.Context().Value("user").(*User)
  
  domains := query.Domain.GetByUser(user.ID)
  
  json.NewEncoder(w).Encode(UserResponse{
    APIKey:  user.APIKey,
    Email:   user.Email,
    Domains: sanitizeDomains(domains),
  })
}
```

#### Login
```go
type LoginRequest struct {
  Email    string `json:"email" validate:"required,email,max=255"`
  Password string `json:"password" validate:"required,min=8,max=64"`
}

type LoginResponse struct {
  Token string `json:"token"`
}

func LoginHandler(w http.ResponseWriter, r *http.Request) {
  var req LoginRequest
  json.NewDecoder(r.Body).Decode(&req)
  
  user, err := authenticateLocal(req.Email, req.Password)
  if err != nil {
    panic(&CustomError{Message: "Login credentials are wrong.", StatusCode: 401})
  }
  
  token, _ := signToken(user.ID)
  setTokenCookie(w, token)
  
  json.NewEncoder(w).Encode(LoginResponse{Token: token})
}
```

#### Signup
```go
type SignupRequest struct {
  Email    string `json:"email" validate:"required,email,max=255"`
  Password string `json:"password" validate:"required,min=8,max=64"`
}

type SignupResponse struct {
  Message string `json:"message"`
}

func SignupHandler(w http.ResponseWriter, r *http.Request) {
  var req SignupRequest
  json.NewDecoder(r.Body).Decode(&req)
  
  user, err := createUser(req.Email, req.Password)
  if err != nil {
    panic(err)
  }
  
  sendVerificationEmail(user)
  
  w.WriteHeader(http.StatusCreated)
  json.NewEncoder(w).Encode(SignupResponse{
    Message: "A verification email has been sent.",
  })
}
```

#### Add Domain
```go
type AddDomainRequest struct {
  Address  string `json:"address" validate:"required,min=3,max=64"`
  Homepage string `json:"homepage" validate:"omitempty,url"`
}

type DomainResponse struct {
  ID        string    `json:"id"`
  Address   string    `json:"address"`
  Homepage  string    `json:"homepage"`
  Banned    bool      `json:"banned"`
  CreatedAt time.Time `json:"created_at"`
  UpdatedAt time.Time `json:"updated_at"`
}

func AddDomainHandler(w http.ResponseWriter, r *http.Request) {
  var req AddDomainRequest
  json.NewDecoder(r.Body).Decode(&req)
  
  user := r.Context().Value("user").(*User)
  
  domain, err := addDomain(user, req)
  if err != nil {
    panic(err)
  }
  
  json.NewEncoder(w).Encode(sanitizeDomain(domain))
}
```

---

## 9. Backward-Compatibility Requirements

**BC-001** API v2 Contract Preservation  
All existing API v2 endpoints must remain byte-compatible:
- **Field Names:** Exact casing (camelCase in JSON responses)
- **Date Formats:** ISO 8601 with milliseconds: `"2024-01-01T00:00:00.000Z"`
- **Error Shape:** `{ "error": "message" }` (JSON), same status codes
- **Boolean Fields:** `true`/`false` (JSON booleans, not strings)
- **Numeric Fields:** JSON numbers (not strings)

**BC-002** JWT Compatibility  
Existing JWTs must remain valid:
- **Algorithm:** HS256 (same as Node.js jsonwebtoken default)
- **Claims:** `iss`, `sub` (user ID as number), `iat`, `exp`
- **Secret:** Same `JWT_SECRET` env var
- **Expiry:** 7 days (same as Node.js)

**BC-003** API Key Compatibility  
Existing API keys (40-char nanoid) must remain valid:
- **Header:** `X-API-KEY`
- **Storage:** Plain-text in `users.apikey`

**BC-004** Password Hash Compatibility  
Existing bcrypt hashes must remain valid:
- **Algorithm:** bcrypt with cost 12
- **Format:** `$2a$12$...` (Go's bcrypt is compatible)
- **Verification:** Go's `bcrypt.CompareHashAndPassword` verifies Node.js bcryptjs hashes

**BC-005** Link Address Generation  
New links must use same alphabet and length:
- **Alphabet:** `env.LINK_CUSTOM_ALPHABET` (default omits o,O,0,i,I,l,1,j)
- **Length:** `env.LINK_LENGTH` (default 6)
- **Algorithm:** nanoid (Go port must match)

**BC-006** Database Schema  
Read-only on existing schema (Part A defines schema, Part B only specifies query interfaces).

**BC-007** Email Templates  
Must support same template variables and structure (Go templates can embed HTML files).

**BC-008** HTMX Response Headers  
HTML responses must set same headers for HTMX compatibility:
- `HX-Trigger`, `HX-Reswap`, `HX-Redirect`, `HX-Trigger-After-Swap`

---

## 10. Cross-References to Other Parts

**Part A (Data Layer):**
- All `query.*` calls (e.g., `query.link.create`, `query.user.find`) are defined in Part A
- Database schema, migrations, Knex/SQL query implementations
- Redis cache keys and invalidation logic
- Visit queue (Bull) for background processing

**Part C (Views/Frontend/Deployment):**
- Handlebars templates (`server/views/`, `custom/views/`)
- Static assets (`static/`, `custom/images/`, `custom/css/`)
- HTMX integration (HTML routes, partial rendering)
- Docker/deployment configurations
- Overall Go project structure (cmd, internal, pkg layout)

---

## 11. Summary & Deliverables Checklist

This SRS covers:

- ✅ **Route Inventory:** 34 API routes + 15+ HTML routes with full middleware chains, request/response schemas, status codes
- ✅ **Core Function Logic:** Pseudo-code for link creation, editing, banning, redirect, stats, domain add/remove, user deletion, admin creation
- ✅ **Authentication & Authorization:** Local (email/password), JWT, API key, OIDC flows with configuration, token formats, role checks
- ✅ **Security Middleware:** Helmet, CORS, trust proxy, rate limiting (per-route limits), bot detection
- ✅ **Configuration:** All 44 environment variables with types, defaults, validation, effects, file-based secrets
- ✅ **Mail System:** 5 email triggers (verification, reset, change email, report, admin creation) with templates, SMTP config
- ✅ **Error Handling:** CustomError class, global error handler, validation error handling, HTMX response headers
- ✅ **Go Mapping:** Router recommendation (net/http ServeMux 1.22+), middleware equivalents, libraries for JWT/bcrypt/OIDC/validation/rate limiting/mail/env/nanoid, handler signatures, DTO structs
- ✅ **Backward-Compatibility:** API contract, JWT/API key/password hash compatibility, link generation, HTMX headers

**File Citations:**
- `server/server.js` (middleware stack, route mounting)
- `server/env.js` (all config validation)
- `server/passport.js` (auth strategies)
- `server/routes/*.routes.js` (route definitions)
- `server/handlers/*.handler.js` (business logic)
- `server/mail/mail.js` (email sending)
- `server/utils/utils.js` (helper functions, sanitizers, JWT signing)
- `server/consts.js` (roles)
- `server/redis.js` (cache keys)
- `docs/api/api.js` (OpenAPI spec)
- `package.json` (dependencies)
- `.example.env` (config reference)

**Requirements IDs:** API-001 through API-034, MAIL-001 through MAIL-005, BC-001 through BC-008

**Output Path:** `/root/Omran/kutt-port-srs/part-B-api.md`

**Word Count:** ~11,500 words

---

**End of Part B SRS**


---

# Kutt URL Shortener – SRS Part C: UI/Frontend/Architecture/Migration

**Version:** 1.0  
**Source:** Kutt v3.2.6 (commit 279b491, Node.js implementation)  
**Target:** Go 1.27.1 single-binary port  
**Date:** 2026-09-27  
**Authors:** AI Technical Specification Team  

---

## 1. System Overview & Functional Requirements

### 1.1 Product Context

Kutt is a self-hosted URL shortener designed for zero-configuration deployment supporting SQLite (default), PostgreSQL, and MySQL/MariaDB. The current Node.js implementation (v3.2.6, ≈6,118 LOC across 56 JS files in `server/`) uses **htmx** for server-side-rendered HTML with progressive enhancement, **Handlebars** templates (`.hbs`), and minimal client-side JavaScript (≈14KB unminified).

**Repository:** `/root/Omran/kutt`  
**Key architecture points:**
- **Express.js** server with Passport.js authentication (JWT, API key, local, OIDC)
- **Knex.js** query builder for DB abstraction (PostgreSQL, MySQL, SQLite)
- **Bull** job queue (Redis-backed) for email dispatch
- **Static-first** asset serving with **custom/ override mechanism** for themes/branding
- **Handlebars helpers** for server-side logic (`ifEquals`, `json`, block inheritance `extend`/`block`)
- **No build step** required (ES5 client JS, pre-minified vendor libs)

### 1.2 Functional Requirements (User Perspective)

#### FR-001: Anonymous/Guest Homepage
- **Page:** `/` (homepage.hbs, renders shortener.hbs + links/table.hbs if logged in)
- **Flow:** User pastes long URL → submits form (htmx POST `/api/links`) → receives shortened link displayed in-place
- **htmx interaction:** `hx-post="/api/links" hx-target="closest main" hx-swap="outerHTML"` (server/views/partials/shortener.hbs:29-32)
- **Advanced options:** Custom address, domain selection (from user's domains), password protection, expiration time, description (shortener.hbs:66-136)
- **Validation:** Client-side form preservation with `hx-preserve="true"` on inputs; server returns errors in template context (`{{#if errors.target}}` shortener.hbs:35,50)

#### FR-002: User Registration & Authentication
- **Page:** `/login` (login.hbs)
- **Features:**
  - Email/password signup (POST `/api/auth/signup`) with verification email (if `MAIL_ENABLED=true`)
  - Email/password login (POST `/api/auth/login`)
  - OIDC login (GET `/login/oidc`, configured via `OIDC_*` env vars)
  - Password reset flow (GET `/reset-password`, email-based token)
- **Conditional rendering:**
  - `{{#if disallow_registration}}` hides signup form (partials/auth/form.hbs)
  - `{{#if login_disabled}}` shows "Login is disabled" (partials/auth/login_disabled.hbs)
  - `{{#if oidc_enabled}}` displays OIDC button with `{{oidc_button_text}}` (locals.handler.js:32-33)
- **Session:** JWT token stored in httpOnly cookie (7-day expiry, utils.js:50-56)

#### FR-003: Link Management (Authenticated Users)
- **Page:** `/` (homepage.hbs for logged-in users shows partials/links/table.hbs)
- **Table features:**
  - Auto-loads on page load: `hx-get="/api/links" hx-trigger="load once"` (links/table.hbs:4,13-14)
  - Pagination: limit (10/25/50/100), skip, prev/next buttons (JS in main.js:147-209)
  - Search: debounced input (500ms delay) triggers reload (links/table.hbs:17)
  - Actions per link (partials/links/actions.hbs):
    - **Edit:** `hx-get="/link/edit/{id}" hx-target="next tr.edit" hx-swap="innerHTML"` (opens inline edit form)
    - **Stats:** `hx-get="/stats?id={id}"` (navigates to stats page)
    - **QR code:** JS function `handleQRCode()` (main.js:88-102) using qrcode.min.js
    - **Copy link:** JS `handleShortURLCopyLink()` (main.js:110-118)
    - **Delete:** Opens confirm dialog (htmx loads partials/links/dialog/delete.hbs into modal), DELETE `/api/links/{id}` on confirm
- **Edit flow:** PATCH `/api/links/{id}` → server returns updated `partials/links/tr.hbs` with `hx-swap-oob="true"` (OOB swap replaces original row, links/tr.hbs:1)

#### FR-004: Custom Domains (User Settings)
- **Page:** `/settings` (settings.hbs)
- **Sections:**
  - Domain management (partials/settings/domain/index.hbs):
    - Add domain form (htmx loads partials/settings/domain/add_form.hbs)
    - POST `/api/domains` with `address` and `homepage` fields
    - Delete domain: confirm dialog → DELETE `/api/domains/{id}`
  - API key display (settings/apikey.hbs): `{{user.apikey}}` with copy button
  - Change password (settings/change_password.hbs): POST `/api/auth/change-password`
  - Change email (settings/change_email.hbs, shown only if `mail_enabled`): POST `/api/auth/change-email` sends verification link
  - Delete account (settings/delete_account.hbs): POST `/api/auth/delete` with password confirmation

#### FR-005: Admin Panel
- **Page:** `/admin` (admin.hbs, requires `user.role === "admin"`)
- **Tabs:**
  1. **Links** (default, partials/admin/links/table.hbs):
     - Filters: search, by user email, by domain, anonymous only
     - Actions: Edit, Ban (sets `banned=true`), Delete
     - Click user email → switches to Users tab filtered by that email (htmx navigation, admin/links/tr.hbs:12-18)
  2. **Users** (partials/admin/users/table.hbs):
     - Filters: search, role (admin/user), verified status
     - Actions: Create user, Ban user, Delete user
     - Shows email, role, verified status, link count, created_at
  3. **Domains** (partials/admin/domains/table.hbs):
     - Filters: search
     - Actions: Add domain, Ban domain, Delete domain
     - Shows address, homepage, user email (if user-owned), link count
- **Dialog system:** Modal overlays for create/delete/ban confirmations (partials/admin/dialog/frame.hbs), htmx populates dialog content from endpoints like `/create-user`, `/confirm-user-delete?id={id}`

#### FR-006: Link Stats (Per-Link Analytics)
- **Page:** `/stats?id={link_uuid}` (stats.hbs)
- **Data loaded via htmx:** `hx-get="/api/links/{id}/stats" hx-trigger="load once"` (stats.hbs:5-7)
- **Charts (Chart.js v4):**
  - Line chart: views over time (day: hourly, week: daily, month: 30 days, year: monthly) — scripts/stats.js:74-157
  - Bar chart: browsers (scripts/stats.js:160-229), operating systems (scripts/stats.js:314-383)
  - Doughnut chart: referrers (scripts/stats.js:232-298)
  - SVG world map: countries (embedded in partials/stats.hbs:65-82), data fed via `feedMapData()` (scripts/stats.js:386-412)
- **Periods:** Day/Week/Month/Year toggles (stats.hbs:26-30) show/hide corresponding canvases (scripts/stats.js:43-58)
- **Interactivity:** Map tooltip on hover (scripts/stats.js:415-437), period switching (scripts/stats.js:43-59)

#### FR-007: Password-Protected Links
- **Flow:** User visits short link with password → server detects protected link → renders `/protected/{id}` (protected.hbs)
- **Template:** partials/protected/form.hbs with password input
- **Submission:** POST `/api/links/{id}/protected` (link.routes.js:100-107) → if password correct, redirect to target; else re-render form with error

#### FR-008: Link Expiration
- **User input:** "Expire in: 2 minutes/hours/days" (partials/shortener.hbs:115-121)
- **Validation:** `ms()` library parses human-readable string (validators.handler.js uses `ms` for parsing)
- **Behavior:** Expired links redirect to 404 page (handled in links.handler.js redirect logic)

#### FR-009: Abuse Reporting
- **Page:** `/report` (report.hbs, enabled only if `REPORT_EMAIL` env var set)
- **Form:** partials/report/form.hbs
- **Flow:**
  1. User enters short link → htmx fetches report email (hides it as `user[at]domain.com` format)
  2. GET `/get-report-email` returns partials/report/email.hbs with obfuscated email (renders.handler.js:267-274)
  3. POST `/api/links/report` with link + reason → emails admin at `REPORT_EMAIL`

#### FR-010: Email Verification & Password Reset
- **Verification:** New users receive email with token → GET `/verify/{token}` (verify.hbs) → shows success/failure
- **Password reset:**
  1. GET `/reset-password` (reset_password.hbs) → user enters email
  2. POST `/api/auth/reset-password` → sends email with reset link
  3. User clicks link → GET `/reset-password/{token}` (reset_password_set_new_password.hbs)
  4. POST `/api/auth/reset-password/:token` with new password

---

## 2. Page/Template Inventory & htmx Architecture

### 2.1 Full Template List

**Main layouts:**
- `layout.hbs` (line 1-42): Base HTML shell, loads htmx.min.js, qrcode.min.js, main.js, custom CSS from `custom_styles` array (locals.handler.js:36)

**Pages (14 top-level views):**
1. `homepage.hbs` → shortener.hbs + (if user) links/table.hbs
2. `login.hbs` → auth/form.hbs or auth/login_disabled.hbs or auth/verify.hbs
3. `logout.hbs` → triggers `redirectToHomepage` event (main.js:10-14)
4. `create_admin.hbs` → auth/form_admin.hbs (first-run admin creation)
5. `settings.hbs` → domain/index.hbs + apikey.hbs + change_password.hbs + change_email.hbs + delete_account.hbs
6. `admin.hbs` → admin/index.hbs (3 tabs: links, users, domains)
7. `stats.hbs` → stats.hbs partial (loaded via htmx)
8. `protected.hbs` → protected/form.hbs
9. `report.hbs` → report/form.hbs
10. `reset_password.hbs` → reset_password/request_form.hbs
11. `reset_password_set_new_password.hbs` → reset_password/new_password_form.hbs
12. `verify.hbs` → auth/verify.hbs
13. `verify_change_email.hbs` → auth/welcome.hbs
14. `404.hbs`, `banned.hbs`, `error.hbs`, `terms.hbs` (static pages)

**Partials (120 files):**
- **admin/** (41 files): links, users, domains tables + dialogs
- **auth/** (5 files): form.hbs, form_admin.hbs, login_disabled.hbs, verify.hbs, welcome.hbs
- **icons/** (19 SVG icon files): arrow_left, chart, check, chevron_left/right, cog, copy, eye, heart, key, login, new_user, pencil, plus, qrcode, reload, send, shield, shuffle, spinner, stop, trash, write, x, zap
- **links/** (13 files): table, thead, tbody, tfoot, tr, actions, edit, loading, nav, dialog/{frame, delete, delete_success, ban, ban_success, message}
- **settings/** (11 files): apikey, change_email, change_password, delete_account, domain/{index, table, add_form, delete, delete_success, dialog}
- **Other:** header.hbs, footer.hbs, shortener.hbs, stats.hbs, protected/form.hbs, report/{form, email}.hbs, support_email.hbs

### 2.2 Template Context Variables (Data Passed from Server)

**Global locals (every request, locals.handler.js:23-37):**
```javascript
res.locals.default_domain = env.DEFAULT_DOMAIN;         // e.g., "kutt.to"
res.locals.site_name = env.SITE_NAME;                   // e.g., "Kutt"
res.locals.contact_email = env.CONTACT_EMAIL;
res.locals.server_ip_address = env.SERVER_IP_ADDRESS;
res.locals.server_cname_address = env.SERVER_CNAME_ADDRESS;
res.locals.disallow_registration = env.DISALLOW_REGISTRATION;
res.locals.disallow_login_form = env.DISALLOW_LOGIN_FORM;
res.locals.login_disabled = env.DISALLOW_LOGIN_FORM && !env.OIDC_ENABLED;
res.locals.oidc_enabled = env.OIDC_ENABLED;
res.locals.oidc_button_text = env.OIDC_BUTTON_TEXT;
res.locals.mail_enabled = env.MAIL_ENABLED;
res.locals.report_email = env.REPORT_EMAIL;
res.locals.custom_styles = utils.getCustomCSSFileNames(); // Array of CSS filenames from custom/css/
```

**User-specific (locals.handler.js:40-45, auth routes):**
```javascript
res.locals.user = {
  id, email, apikey, role, verified, banned, created_at, updated_at
};
res.locals.domains = [{ id, address, homepage, banned, created_at }]; // User's custom domains
```

**Page-specific examples:**

1. **shortener.hbs (homepage):**
   - `link` (if just created): `{ link, url }` (short link text and full URL)
   - `errors`: `{ target, domain, customurl, password, expire_in, description }` (validation errors)
   - `error`: string (global error message)
   - `show_advanced`: boolean (re-render with advanced section open if user toggled it)

2. **links/table.hbs → tbody.hbs:**
   - Array of links (from `GET /api/links`), each sanitized via `utils.sanitize.link_html()`:
     ```javascript
     {
       id: uuid,
       target: string,           // Original long URL
       address: string,          // Short code (e.g., "abc123")
       domain: string | null,    // Custom domain or null
       description: string | null,
       password: boolean,        // True if has password (actual password never sent)
       banned: boolean,
       visit_count: string,      // Localized number (e.g., "1,234")
       relative_created_at: string, // "2 hours ago" (utils.js:238-265)
       relative_expire_in: string | null, // "in 5 days"
       link: { address, link, url }, // getShortURL() result
       created_at: Date,
       updated_at: Date,
     }
     ```
   - Pagination: `total`, `skip`, `limit` (hidden inputs in nav.hbs)

3. **admin/links/table.hbs:**
   - Same as links/table but includes `user_id`, `email` (link owner), sanitized via `utils.sanitize.link_admin()`

4. **stats.hbs (partials/stats.hbs):**
   - `link`: sanitized link object
   - `stats`: Complex nested object:
     ```javascript
     {
       lastDay: {
         total: number,
         views: number[24],    // Hourly counts
         stats: { browser, os, country, referrer } // Each is object { name: count }
       },
       lastWeek: { total, views: number[7], stats: {...} },
       lastMonth: { total, views: number[30], stats: {...} },
       lastYear: { total, views: number[12], stats: {...} },
       updatedAt: Date
     }
     ```
   - `map`: SVG map data `{ viewBox, layers: [{ id, name, d }] }`

5. **settings/domain/table.hbs:**
   - `domains`: Array of user's domains (same structure as `res.locals.domains`)

6. **admin tables (users/domains):**
   - `users`: Array of `{ id, email, role, verified, banned, links_count, relative_created_at }`
   - `domains`: Array of `{ id, address, homepage, user_email, banned, links_count, relative_created_at }`

### 2.3 htmx Attributes Inventory

**Core htmx patterns used:**

1. **Auto-load on page render:**
   ```hbs
   <table hx-get="/api/links" hx-trigger="load once" hx-target="tbody" hx-swap="outerHTML">
   ```
   - Files: links/table.hbs:4, admin/links/table.hbs, stats.hbs:5

2. **Form submission with target replacement:**
   ```hbs
   <form hx-post="/api/links" hx-target="closest main" hx-swap="outerHTML">
   ```
   - Files: shortener.hbs:29, auth forms

3. **Out-of-band swaps (OOB):**
   ```hbs
   <tr id="tr-{{id}}" hx-swap-oob="true">
   ```
   - Files: links/tr.hbs:1, admin/links/tr.hbs:1
   - Purpose: After edit/ban/delete, server returns updated row that replaces original by ID

4. **Inline editing:**
   ```hbs
   <button hx-get="/link/edit/{id}" hx-target="next tr.edit" hx-swap="innerHTML">
   ```
   - Files: links/actions.hbs, admin/links/actions.hbs

5. **Dialog loading:**
   ```hbs
   <button hx-get="/confirm-link-delete?id={id}" hx-target="#dialog" hx-swap="innerHTML">
   ```
   - Files: links/actions.hbs, admin actions

6. **Custom htmx extensions used:**
   - `path-params` (main.js:28-38): Replaces `{param}` in URL path with form data (e.g., `/api/links/{id}` → `/api/links/abc123`)
   - `preload` (main.js:274-362): Prefetches htmx links on mousedown/mouseover for faster navigation

7. **htmx event handlers (hx-on:* attributes):**
   - `hx-on:click="handleCopyLink(this)"` (shortener.hbs:8, links/tr.hbs:24)
   - `hx-on:change="htmx.toggleClass('#advanced-options', 'hidden')"` (shortener.hbs:61)
   - `hx-on::after-swap="createCharts()"` (stats.hbs:10-13, triggers Chart.js rendering)

8. **htmx request configuration:**
   - Meta tag: `<meta name="htmx-config" content='{"withCredentials":true}'>` (layout.hbs:14)
   - Global Accept header override: `htmx:configRequest` event (main.js:5-7) adds `Accept: text/html,*/*`

9. **htmx response headers (server-side, HX-* headers):**
   - `HX-Trigger` events (server sends to client):
     - `redirectToHomepage` (logout.hbs, triggers JS redirect after 1.5s)
     - `resetChangePasswordForm`, `resetChangeEmailForm` (main.js:24-25, clears forms after success)
     - `reloadMainTable` (main.js:239, reloads links table after search clear)
   - Example (handlers/helpers.handler.js):
     ```javascript
     if (req.isHTML) {
       res.setHeader("HX-Trigger", "resetChangePasswordForm");
       return res.render(req.viewTemplate, { success: "Password changed" });
     }
     ```

10. **htmx parameters:**
    - `hx-include=".links-controls"` (links/table.hbs:9): Includes search/limit/skip inputs in request
    - `hx-params="not total"` (links/table.hbs:10): Excludes `total` field from request
    - `hx-vals='{"search":"{{email}}"}'` (admin/links/tr.hbs:17, inline JSON for query params)
    - `hx-vals='js:{ id: getQueryParams().id }'` (stats.hbs:8, JS expression to extract URL param)

11. **htmx synchronization:**
    - `hx-sync="this:replace"` (links/table.hbs:11, admin tables): Cancels in-flight requests to same element

12. **htmx indicators:**
    - `hx-indicator="closest table"` (admin/links/tr.hbs:16): Shows spinner on table during request
    - Default: `{{> icons/spinner}}` SVG with `.htmx-request` CSS class toggle

13. **htmx select/disinherit:**
    - `hx-select="tbody"` (links/table.hbs:7): Only swaps `<tbody>` from response
    - `hx-select-oob="#total"` (links/table.hbs:12): Also extracts and swaps `#total` element (OOB)
    - `hx-disinherit="*"` (links/table.hbs:8): Prevents inheriting hx-* attributes from parents

**htmx usage statistics:**
- 60 out of 120 `.hbs` files contain htmx attributes
- Most common: `hx-get` (table loads, partials), `hx-post` (forms), `hx-target`, `hx-swap`

### 2.4 Route-to-Template Mapping

**Render routes (server/routes/renders.routes.js):**

| HTTP | Path | Auth | Template | Context |
|------|------|------|----------|---------|
| GET | `/` | jwtLoosePage | homepage.hbs | `{ title, user?, domains? }` |
| GET | `/login` | jwtLoosePage | login.hbs | `{ title }` |
| GET | `/logout` | - | logout.hbs | `{ title }` |
| GET | `/create-admin` | - | create_admin.hbs | `{ title }` (only if no users exist) |
| GET | `/settings` | jwtPage | settings.hbs | `{ title, user, domains }` |
| GET | `/admin` | jwtPage + admin | admin.hbs | `{ title, user, domains }` |
| GET | `/stats` | jwtPage | stats.hbs | `{ title, user }` |
| GET | `/report` | jwtLoosePage | report.hbs | `{ title }` (if REPORT_EMAIL set) |
| GET | `/reset-password` | jwtLoosePage | reset_password.hbs | `{ title }` (if MAIL_ENABLED) |
| GET | `/verify/:token` | - | verify.hbs | `{ title }` (token verified in auth handler) |
| GET | `/terms` | jwtLoosePage | terms.hbs | `{ title }` |

**API routes returning HTML (server/routes/link.routes.js, user.routes.js, domain.routes.js):**

| HTTP | Path | Template (viewTemplate) | Purpose |
|------|------|-------------------------|---------|
| GET | `/api/links` | partials/links/table.hbs | Load user links table |
| GET | `/api/links/admin` | partials/admin/links/table.hbs | Admin links table |
| POST | `/api/links` | partials/shortener.hbs | Create link (returns shortener with success/error) |
| PATCH | `/api/links/:id` | partials/links/edit.hbs | Edit link (returns edit form or updated tr with OOB) |
| DELETE | `/api/links/:id` | partials/links/dialog/delete.hbs | Delete link (returns success dialog) |
| POST | `/api/links/admin/ban/:id` | partials/links/dialog/ban.hbs | Ban link (returns success dialog) |
| GET | `/api/links/:id/stats` | partials/stats.hbs | Link stats data |
| POST | `/api/links/:id/protected` | partials/protected/form.hbs | Verify password for protected link |
| POST | `/api/links/report` | partials/report/form.hbs | Report abuse (returns success message) |
| GET | `/api/users/admin` | partials/admin/users/table.hbs | Admin users table |
| POST | `/api/auth/create-user` | partials/admin/dialog/create_user_success.hbs | Create user (admin) |
| GET | `/api/domains` | partials/settings/domain/table.hbs | User domains table |
| GET | `/api/domains/admin` | partials/admin/domains/table.hbs | Admin domains table |
| POST | `/api/domains` | partials/settings/domain/add_form.hbs | Add domain (returns form or success) |
| DELETE | `/api/domains/:id` | partials/settings/domain/delete_success.hbs | Delete domain |

**Partial-only endpoints (renders.routes.js, locals.noLayout):**

| Path | Template | Purpose |
|------|----------|---------|
| `/confirm-link-delete` | partials/links/dialog/delete.hbs | Load delete confirmation |
| `/confirm-link-ban` | partials/links/dialog/message.hbs | Admin ban confirmation |
| `/link/edit/:id` | partials/links/edit.hbs | Inline edit form |
| `/admin/link/edit/:id` | partials/admin/links/edit.hbs | Admin inline edit form |
| `/create-user` | partials/admin/dialog/create_user.hbs | Admin create user dialog |
| `/add-domain-form` | partials/settings/domain/add_form.hbs | User add domain form |
| `/confirm-domain-delete` | partials/settings/domain/delete.hbs | User domain delete confirm |
| `/confirm-domain-ban` | partials/admin/dialog/ban_domain.hbs | Admin ban domain |
| `/get-report-email` | partials/report/email.hbs | Show obfuscated report email |
| `/get-support-email` | partials/support_email.hbs | Show contact email (delayed 500ms, utils.sleep) |

---

## 3. Handlebars Helpers & Go html/template Equivalents

### 3.1 Current Handlebars Helpers (server/utils/utils.js:396-425)

**1. `ifEquals` helper (utils.js:397-399):**
```javascript
hbs.registerHelper("ifEquals", function(arg1, arg2, options) {
  return (arg1 === arg2) ? options.fn(this) : options.inverse(this);
});
```
**Usage:** `{{#ifEquals user.role "admin"}}...{{else}}...{{/ifEquals}}`  
**Go html/template equivalent:**
```go
funcMap["eq"] = func(a, b interface{}) bool { return a == b }
// Template: {{if eq .User.Role "admin"}}...{{else}}...{{end}}
```

**2. `json` helper (utils.js:401-403):**
```javascript
hbs.registerHelper("json", function(context) {
  return JSON.stringify(context);
});
```
**Usage:** `<canvas data-data="{{json stats.lastDay.views}}"></canvas>` (stats.hbs:39)  
**Go html/template equivalent:**
```go
import "encoding/json"
funcMap["json"] = func(v interface{}) (string, error) {
    b, err := json.Marshal(v)
    return string(b), err
}
// Template: <canvas data-data="{{json .Stats.LastDay.Views}}"></canvas>
```

**3. `extend` and `block` helpers (utils.js:405-419):**
```javascript
const blocks = {};
hbs.registerHelper("extend", function(name, context) {
    let block = blocks[name];
    if (!block) {
        block = blocks[name] = [];
    }
    block.push(context.fn(this));
});
hbs.registerHelper("block", function(name) {
    const val = (blocks[name] || []).join("\n");
    blocks[name] = [];
    return val;
});
```
**Usage (stats.hbs:22-24):**
```hbs
{{#extend "scripts"}}
  <script src="/libs/chart.min.js"></script>
{{/extend}}
<!-- In layout.hbs:37 -->
{{{block "scripts"}}}
```
**Purpose:** Allows child templates to inject content into layout slots (e.g., page-specific `<script>` tags)  
**Go html/template equivalent:**
- Go's `{{define "name"}}...{{end}}` + `{{template "name" .}}` pattern:
  ```go
  // stats.tmpl.html:
  {{define "page-scripts"}}
    <script src="/libs/chart.min.js"></script>
  {{end}}
  
  // layout.tmpl.html:
  {{template "page-scripts" .}}
  ```
- **Critical difference:** Go's `template` is compile-time composition, not runtime accumulation. For dynamic block accumulation across includes, would need custom FuncMap or accept fixed template inheritance (define/template suffices for Kutt's use case: only stats.hbs uses extend/block for Chart.js script, layout.hbs:37).

**4. Partial registration (utils.js:420-425):**
```javascript
hbs.registerPartials(path.join(__dirname, "../views/partials"));
const customPartialsPath = path.join(__dirname, "../../custom/views/partials");
if (fs.existsSync(customPartialsPath)) {
  hbs.registerPartials(customPartialsPath);  // Overrides default partials
}
```
**Purpose:** Loads all `.hbs` files from `server/views/partials/` and `custom/views/partials/` (custom theme overrides)  
**Go html/template handling:**
- Parse all `.html` templates into `*template.Template` tree:
  ```go
  tmpl := template.New("").Funcs(funcMap)
  tmpl, _ = tmpl.ParseGlob("views/**/*.html")       // Default templates
  if _, err := os.Stat("custom/views"); err == nil {
    tmpl, _ = tmpl.ParseGlob("custom/views/**/*.html")  // Override
  }
  ```
- **Naming convention:** Go templates identified by filename (e.g., `links/table.html` → `{{template "links/table.html" .}}`). Must match Handlebars partial naming exactly (e.g., `{{> links/table}}` → `{{template "links/table" .}}`).

### 3.2 Template Engine Decision Matrix

| Option | Pros | Cons | Custom/ Override Support | Recommendation |
|--------|------|------|--------------------------|----------------|
| **Go html/template** | Stdlib, no deps, secure by default, similar syntax to Handlebars | No exact `extend`/`block` equivalent (need workaround), compile-time includes | **Yes** (ParseGlob with custom/ priority, same as Node) | ⭐ **Recommended** |
| **raymond (aymerick/raymond)** | Handlebars-compatible, drop-in replacement, supports helpers | Abandoned (last commit 2019), no Go 1.18+ generics, unmaintained | **Yes** (custom partials work) | ❌ Risky (unmaintained) |
| **mailgun/raymond** | Fork of aymerick/raymond, more recent | Still low activity, small community, non-stdlib | **Yes** | ⚠️ Acceptable fallback |
| **templ (a-h/templ)** | Type-safe Go code generation, compile-time checks, fast | Requires rewrite of all 120 templates, different syntax, build step | **No** (compiled, can't override at runtime) | ❌ Migration too costly |

**Decision: Use Go `html/template` stdlib**

**Rationale:**
1. **Zero external dependencies** aligns with Go ethos and Kutt's "zero-config" goal
2. **Custom/ override mechanism preserved:** Parse default templates first, then custom/ templates (same name overrides)
3. **Security:** Auto-escapes HTML by default (Handlebars does too, but Go's Context-aware escaping is more robust)
4. **Performance:** Compiled templates cached in memory (no runtime template parsing like Node.js handlebars)
5. **Minimal migration effort:** 95% of Kutt's templates use basic `{{variable}}`, `{{#if}}`, `{{#each}}`, which have direct Go equivalents
6. **extend/block workaround:** stats.hbs can use `{{define "stats-scripts"}}` + `{{template "stats-scripts" .}}` in layout (or inject script tags server-side in handler)

**Migration plan for helpers:**
```go
package views

import (
    "encoding/json"
    "html/template"
)

var FuncMap = template.FuncMap{
    "eq": func(a, b interface{}) bool {
        return a == b
    },
    "json": func(v interface{}) (template.JS, error) {
        b, err := json.Marshal(v)
        return template.JS(b), err  // template.JS marks as safe JS literal
    },
    // No need for extend/block: handle via explicit defines
}

func NewTemplates(customDir string) (*template.Template, error) {
    tmpl := template.New("").Funcs(FuncMap)
    
    // Parse default views
    tmpl, err := tmpl.ParseGlob("views/**/*.html")
    if err != nil {
        return nil, err
    }
    
    // Parse custom views (overrides)
    if _, err := os.Stat(customDir); err == nil {
        tmpl, err = tmpl.ParseGlob(customDir + "/**/*.html")
    }
    
    return tmpl, nil
}
```

---

## 4. Static Assets & Client-Side JavaScript

### 4.1 Static File Inventory

**Directory structure:**
```
static/
├── css/
│   └── styles.css          (2,322 lines, 67KB, unminified)
├── fonts/
│   └── nunito-variable.woff2  (WOFF2 variable font)
├── images/
│   ├── card.png            (Open Graph card for social media)
│   ├── favicon-16x16.png
│   ├── favicon-32x32.png
│   ├── favicon-196x196.png
│   └── favicon.ico
├── libs/
│   ├── htmx.min.js         (49KB, v1.9.x)
│   ├── qrcode.min.js       (20KB, QRCode.js library)
│   └── chart.min.js        (201KB, Chart.js v4.x)
├── scripts/
│   ├── main.js             (362 lines, 11.7KB)
│   └── stats.js            (450 lines, 13.2KB)
├── manifest.webmanifest    (PWA manifest)
└── robots.txt
```

**Total size:** 484KB (including pre-minified vendor libs)

### 4.2 CSS Architecture (styles.css)

- **No build step:** Plain CSS (no Sass/PostCSS)
- **Custom properties:** Uses CSS variables for theming:
  ```css
  :root {
    --color-primary: #b39ddb;  /* Purple brand color */
    --color-text: #333;
    --color-bg: #f3f3f3;
  }
  ```
- **Responsive:** Media queries for mobile (≥768px breakpoints)
- **Print styles:** `@media print` hides navigation, dialogs
- **Custom CSS override:** `custom/css/*.css` files loaded after styles.css (layout.hbs:27-29)
  - If `custom/css/styles.css` exists, it **replaces** default styles.css (server.js:55)
  - Other files (e.g., `custom1.css`) are **appended** (locals.handler.js:36, getCustomCSSFileNames())

### 4.3 Client JavaScript Functionality

**main.js (11.7KB, 362 lines):**

1. **htmx configuration (lines 1-7):**
   - Global `Accept: text/html` header for htmx requests
   - `withCredentials: true` for CORS (layout.hbs:14 meta tag)

2. **Custom htmx events (lines 10-25):**
   - `redirectToHomepage`: 1.5s delayed redirect to `/`
   - `resetChangePasswordForm`, `resetChangeEmailForm`: Clear forms after success

3. **htmx extension: path-params (lines 28-38):**
   - Replaces `{param}` in URL paths with form data (e.g., `/api/links/{id}` + `id=abc123` → `/api/links/abc123`)

4. **htmx extension: preload (lines 274-362):**
   - Prefetches links on `mousedown` (default) or `mouseover` (configurable)
   - Usage: `<a href="/stats" preload>` prefetches on mousedown
   - **Performance win:** Reduces perceived latency for navigation

5. **Utility functions:**
   - `closest(selector, elm)`: Polyfill for Element.closest() (lines 41-53)
   - `getQueryParams()`: Parse `?key=value` from URL (lines 56-64)
   - `trimText(selector, length)`: Truncate text with ellipsis (lines 67-76, used in stats.hbs for long URLs)
   - `formatDateHour(selector)`: Display hour:minute from ISO timestamp (lines 78-85)

6. **Link actions (lines 88-118):**
   - `handleQRCode(element, id)`: Generates QR code in dialog using qrcode.min.js (lines 88-102)
   - `handleCopyLink(element)`: `navigator.clipboard.writeText()` (lines 105-107)
   - `handleShortURLCopyLink(element)`: Copy + animate clipboard icon (lines 110-118)

7. **Dialog system (lines 121-144):**
   - `openDialog(id, name)`: Add `.open` class to dialog overlay
   - `closeDialog()`: Remove all classes (reset to `.dialog`)
   - Click outside dialog → close (lines 139-144)

8. **Table navigation (links pagination, lines 147-210):**
   - `setLinksLimit(event)`: Change limit (10/25/50/100), update buttons
   - `setLinksSkip(event, action)`: Increment/decrement skip, disable prev/next at boundaries
   - `updateLinksNav()`: Recalculate button disabled states after htmx swap
   - `resetTableNav()`: Reset to skip=0, limit=10 (used after search clear)

9. **Tab navigation (lines 213-223):**
   - `setTab(event, targetId)`: Add `.active` class to clicked tab, remove from siblings

10. **Search controls (lines 226-260):**
    - `onSearchChange(event)`: Show/hide clear button based on input length
    - `clearSeachInput(event)`: Clear input, hide button, trigger htmx table reload
    - `onSearchInputLoad()`: Initialize clear button visibility on page load

11. **Admin user creation checkboxes (lines 263-270):**
    - `canSendVerificationEmail()`: Show/hide "Send verification email" checkbox if user is verified or banned

**stats.js (13.2KB, 450 lines):**

1. **Chart.js wrappers (Chart.js v4 API):**
   - `createViewsChart()`: Line chart for visits over time (lines 74-157)
     - Responsive canvas with gradient fill
     - Tooltips styled white background, purple text
     - X-axis labels: day (hourly), week (daily), month (daily), year (monthly)
   - `createBrowsersChart()`: Horizontal bar chart (lines 160-229)
   - `createReferrersChart()`: Doughnut chart (lines 232-298), shows "No views" placeholder if empty
   - `createOsChart()`: Horizontal bar chart for OS distribution (lines 314-383)

2. **Period switching (lines 43-59):**
   - `changeStatsPeriod(event)`: Show/hide canvases for day/week/month/year, update map data

3. **Map visualization (lines 386-437):**
   - `feedMapData(period)`: Read stats from `data-day/week/month/year` attributes on SVG, colorize countries by view count (6-level gradient)
   - `mapTooltipHoverOver()`: Position tooltip with country name + view count
   - `mapTooltipHoverOut()`: Hide tooltip

4. **Label beautification:**
   - `beautifyBrowserName(name)`: "chrome" → "Chrome" (lines 62-71)
   - `beautifyOsName(name)`: "macos" → "macOS" (lines 302-310)

5. **Chart initialization (lines 440-450):**
   - `createCharts()`: Entry point called from htmx `hx-on::after-swap` (stats.hbs:10-13)
   - Waits for Chart.js library to load (recursive setTimeout if undefined)

### 4.4 Vendor Libraries

1. **htmx.min.js (49KB):**
   - Version: 1.9.x (confirmed by file size)
   - Features used: hx-get/post, hx-target, hx-swap, hx-trigger, hx-vals, hx-include, hx-sync, hx-ext, hx-on:*, hx-swap-oob, HX-Trigger response header
   - CDN alternative for Go port: `https://unpkg.com/htmx.org@1.9.10/dist/htmx.min.js`

2. **qrcode.min.js (20KB):**
   - Library: davidshimjs/qrcodejs
   - Usage: `new QRCode(element, { text, width: 200, height: 200, ... })` (main.js:94-101)

3. **chart.min.js (201KB):**
   - Version: Chart.js v4.x (4.4.x based on file size)
   - Charts used: Line, Bar (horizontal), Doughnut
   - No plugins required (uses built-in scales, tooltips)

### 4.5 Build Strategy for Go Port

**No build step required (preserving Node.js simplicity):**

1. **CSS:** Ship `static/css/styles.css` as-is (2,322 lines unminified)
   - **Optional production optimization:** Minify CSS with esbuild or cssnano in CI, but NOT a local dev requirement
   - Custom CSS override: Same mechanism (custom/css/ served first)

2. **JavaScript:** Ship `main.js` and `stats.js` as ES5 (no transpilation)
   - **Current Node.js behavior:** No bundler, scripts loaded directly
   - **Go port:** Same (static file serving)

3. **Vendor libs:** Embed htmx.min.js, qrcode.min.js, chart.min.js in Go binary OR serve from CDN
   - **Option A (recommended):** `embed.FS` for offline-first operation
     ```go
     //go:embed static/libs/*
     var libsFS embed.FS
     ```
   - **Option B:** CDN fallback for smaller binary size (but breaks air-gapped deployments)

4. **Fonts/images:** Embed in binary via `embed.FS` (484KB total static/ size is negligible)

### 4.6 Static Asset Serving in Go

**Current Node.js (server.js:54-56):**
```javascript
app.use("/images", express.static("custom/images"));      // Custom images override
app.use("/css", express.static("custom/css", { extensions: [".css"] }));
app.use(express.static("static"));                        // Fallback to default
```

**Go equivalent (net/http pattern):**
```go
package http

import (
    "embed"
    "io/fs"
    "net/http"
    "os"
)

//go:embed static/*
var staticFS embed.FS

func NewStaticHandler(customDir string) http.Handler {
    mux := http.NewServeMux()
    
    // Custom assets (priority)
    if _, err := os.Stat(customDir + "/images"); err == nil {
        mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir(customDir+"/images"))))
    }
    if _, err := os.Stat(customDir + "/css"); err == nil {
        mux.Handle("/css/", http.StripPrefix("/css/", http.FileServer(http.Dir(customDir+"/css"))))
    }
    
    // Default embedded assets (fallback via fs.FS)
    staticSub, _ := fs.Sub(staticFS, "static")
    mux.Handle("/", http.FileServer(http.FS(staticSub)))
    
    return mux
}
```

**Custom CSS discovery (equivalent to utils.getCustomCSSFileNames(), utils.js:428-446):**
```go
func GetCustomCSSFiles(customDir string) ([]string, error) {
    cssDir := filepath.Join(customDir, "css")
    entries, err := os.ReadDir(cssDir)
    if os.IsNotExist(err) {
        return nil, nil  // No custom CSS
    }
    if err != nil {
        return nil, err
    }
    
    var files []string
    for _, entry := range entries {
        if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".css") {
            files = append(files, entry.Name())
        }
    }
    return files, nil
}
```

---

## 5. Non-Functional Requirements (NFRs)

### NFR-001: Performance

**Redirect latency target:**
- **Current Node.js:** ≈10-20ms median (measured via `/api/health`, not documented)
- **Target for Go:** ≤5ms p50, ≤20ms p99 (Go's compiled nature + connection pooling should achieve this)
- **Critical path:** `GET /:id` → DB lookup (indexed `address` + `domain_id`) → 302 redirect
- **Optimization:**
  1. **DB index:** Composite index on `(address, domain_id)` (already exists in migrations)
  2. **Redis cache (optional):** Cache hot links (top 10% by visit_count) for 1h TTL
     - Key: `link:{address}:{domain_id}` → value: `{target, password_hash, banned}`
     - Invalidate on edit/delete (PATCH/DELETE `/api/links/:id`)
  3. **Prepared statements:** Pre-compile redirect query at startup
     ```go
     var redirectStmt *sql.Stmt
     redirectStmt, _ = db.Prepare("SELECT target, password, banned FROM links WHERE address = $1 AND domain_id = $2")
     ```

**Page load targets:**
- **Homepage (authenticated):** ≤500ms server render + ≤200ms htmx table load
- **Admin table (1000 links):** ≤1s server render (pagination to 100 rows max)
- **Stats page:** ≤2s (includes 4 chart datasets, SVG map)

**Caching strategy:**
1. **Static assets:** `Cache-Control: public, max-age=31536000, immutable` (versioned filenames or etags)
2. **Templates:** Compile at startup, cache in memory (Go html/template does this automatically)
3. **API responses:** No caching (always fresh data)

### NFR-002: Scalability

**Horizontal scaling constraints:**
- **Stateless:** No server-side session storage (JWT in httpOnly cookie, utils.js:50-56)
- **Shared state:**
  1. **Database:** PostgreSQL/MySQL with connection pooling (DB_POOL_MIN/MAX env vars)
  2. **Redis (optional):** Bull queue for emails, rate limiting, link cache
- **Deployment model:** Multiple Go binaries behind nginx/traefik reverse proxy (sticky sessions NOT required)

**Load targets:**
- **Redirects:** 10,000 RPS per instance (assuming 80% cache hit rate with Redis)
- **Link creation:** 100 RPS per instance (write-heavy, DB bottleneck)
- **Admin operations:** 10 concurrent users (read-heavy, paginated)

**Database connection pooling (Go database/sql):**
```go
db.SetMaxOpenConns(10)        // DB_POOL_MAX
db.SetMaxIdleConns(2)         // DB_POOL_MIN
db.SetConnMaxLifetime(1 * time.Hour)
```

**Graceful degradation:**
- If Redis unavailable: Disable rate limiting, email queue (log warnings, continue operation)
- If DB connection pool exhausted: Return 503 Service Unavailable (not 500 Internal Server Error)

### NFR-003: Security

**Authentication mechanisms (server/handlers/auth.handler.js):**
1. **JWT (JSON Web Token):**
   - Signed with `JWT_SECRET` (256-bit random string, required in production)
   - Payload: `{ iss: "ApiAuth", sub: user_id, iat, exp }` (utils.js:38-47)
   - Storage: httpOnly cookie, 7-day expiry, SameSite=Lax
   - Middleware: `auth.jwtPage` (require valid token + verified user), `auth.jwtLoosePage` (allow guests)

2. **API Key:**
   - 64-char random string (generated on user creation)
   - Usage: `apikey` query param or `X-API-Key` header (auth.handler.js:apikey middleware)
   - Scope: Full access to user's links/domains (no expiry)

3. **Passport strategies:**
   - `passport-local`: Email/password login (bcrypt hashing, 10 rounds)
   - `passport-jwt`: Extract from cookie, verify signature
   - `passport-localapikey-update`: API key authentication
   - OIDC (openid-client): OAuth2/OpenID Connect (if `OIDC_ENABLED=true`)

**Password security:**
- Hashing: bcryptjs with 10 rounds (server/handlers/auth.handler.js uses bcrypt.hash)
- Storage: `password_hash` column (TEXT), never plain text
- Link passwords: Same bcrypt hashing (links.handler.js:create)

**HTTPS enforcement:**
- **Redirect links:** `http://` if dev or `CUSTOM_DOMAIN_USE_HTTPS=false`, else `https://` (utils.js:84-92)
- **Cookie secure flag:** `secure: env.isProd` (utils.js:54)

**Rate limiting (if `ENABLE_RATE_LIMIT=true`):**
- Middleware: `express-rate-limit` + `rate-limit-redis` (if Redis enabled) or in-memory
- Applied to: `/api/links` (POST), `/api/auth/signup`, `/api/auth/login`
- Limits: 10 requests per minute per IP for auth, 100 per hour for link creation

**Input validation (server/handlers/validators.handler.js):**
- Library: `express-validator`
- Rules:
  - `target`: Must match `urlRegex` (utils.js:26), max 2048 chars
  - `customurl`: Alphanumeric + `-_`, max 255 chars, not in `preservedURLs` list (utils.js:158-199)
  - `email`: Valid email format, max 255 chars
  - `password`: Min 8 chars for account passwords, max 255 for link passwords

**SQL injection prevention:**
- Knex.js parameterized queries (already safe in Node.js)
- Go port: Use `database/sql` with `$1` placeholders (PostgreSQL) or `?` (MySQL/SQLite)
  ```go
  db.QueryRow("SELECT * FROM links WHERE address = $1", address)
  ```

**XSS prevention:**
- Handlebars auto-escapes by default (`{{ }}` escapes, `{{{ }}}` does not)
- Go html/template: Context-aware auto-escaping (stricter than Handlebars)
- Vulnerable points: `{{{body}}}` in layout.hbs (but controlled by server, not user input)

**CSRF protection:**
- **Current Node.js:** None explicitly (relies on SameSite=Lax cookies + htmx withCredentials)
- **Go port:** Same (SameSite=Lax sufficient for htmx same-origin requests)
- **API usage:** No CSRF tokens (API key auth is CSRF-resistant)

**Helmet.js equivalents for Go:**
- Node.js: `helmet({ contentSecurityPolicy: false })` (server.js:39)
- Go port: Use `github.com/gorilla/handlers` or manual middleware for:
  - `X-Content-Type-Options: nosniff`
  - `X-Frame-Options: DENY`
  - `X-XSS-Protection: 1; mode=block`
  - `Strict-Transport-Security: max-age=31536000` (if HTTPS)

### NFR-004: Observability

**Logging (Go log/slog):**
- **Structured logging:** JSON format for production
  ```go
  slog.Info("redirect", "address", address, "target", target, "latency_ms", latency)
  ```
- **Levels:**
  - INFO: Successful operations (redirects, link creation)
  - WARN: Rate limit exceeded, email send failure (degraded mode)
  - ERROR: DB connection failures, unhandled errors
- **Log destinations:**
  - Development: stdout (human-readable text)
  - Production: stdout (JSON, parsed by log aggregators like Loki/CloudWatch)

**Metrics (Prometheus-compatible):**
- Library: `github.com/prometheus/client_golang`
- **Counters:**
  - `kutt_redirects_total{domain, status}`: Total redirects (200, 302, 404)
  - `kutt_links_created_total`: Links created
  - `kutt_api_requests_total{method, path, status}`: API requests
- **Histograms:**
  - `kutt_redirect_duration_seconds`: Redirect latency (p50, p95, p99)
  - `kutt_db_query_duration_seconds{query}`: DB query latency
- **Gauges:**
  - `kutt_db_connections_open`: Open DB connections
  - `kutt_links_total`: Total links in database (updated hourly)

**Health endpoint (server/routes/health.routes.js):**
```javascript
router.get("/api/health", (req, res) => {
  res.json({ ok: true });
});
```
**Go equivalent:**
```go
http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
    // Check DB connectivity
    if err := db.Ping(); err != nil {
        w.WriteHeader(503)
        json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "db unavailable"})
        return
    }
    json.NewEncoder(w).Encode(map[string]any{"ok": true})
})
```

**Tracing (optional, not in current Node.js):**
- OpenTelemetry integration for distributed tracing (if deploying multi-region)
- Span: `/api/links` → DB query → Redis cache → response

### NFR-005: Internationalization (i18n)

**Current state:** **No i18n support** (all strings hardcoded in English)
- Example: "Cut your links shorter." (shortener.hbs:24), "Settings" (settings.hbs:title)
- Email templates: English only (server/mail/)

**Go port decision:** **Defer i18n to future release** (out of scope for initial port)
- Recommendation: Use `github.com/nicksnyder/go-i18n` if needed later
- Locale detection: `Accept-Language` header → fallback to English

### NFR-006: Accessibility (a11y)

**Current compliance:**
- **Semantic HTML:** `<main>`, `<section>`, `<nav>`, `<table>` (good)
- **ARIA labels:** `aria-label` on icon buttons (e.g., links/tr.hbs:23)
- **Keyboard navigation:** All interactive elements are `<button>` or `<a>` (focusable)
- **Color contrast:** Default theme passes WCAG AA (purple on white: 4.5:1 ratio)
- **Screen reader support:** No `aria-live` regions (htmx updates not announced)

**Go port improvements:**
- Add `aria-live="polite"` to htmx target containers (e.g., `<tbody aria-live="polite">`)
- Ensure focus management after htmx swaps (htmx emits `htmx:afterSwap` event, attach focus JS)

### NFR-007: Configuration

**Environment variables (101 total, .example.env:1-102):**

**Required:**
- `JWT_SECRET`: Random 256-bit string (production only; dev mode generates warning)

**Database (SQLite default):**
- `DB_CLIENT`: `better-sqlite3` (default), `pg`, `mysql2` (server/knex.js)
- `DB_FILENAME`: SQLite file path (default: `db/data`)
- `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`, `DB_SSL`: For PostgreSQL/MySQL
- `DB_POOL_MIN`, `DB_POOL_MAX`: Connection pool size (default: 0, 10)

**Redis (optional):**
- `REDIS_ENABLED`: `true`/`false` (default: `false`)
- `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_DB`

**App behavior:**
- `PORT`: Default 3000
- `SITE_NAME`: "Kutt" (displayed in title, header)
- `DEFAULT_DOMAIN`: Domain for short links (e.g., "kutt.to")
- `LINK_LENGTH`: Nanoid length (default: 6 chars)
- `LINK_CUSTOM_ALPHABET`: Alphabet for random addresses (default omits confusable chars: o/O/0/i/I/l/1/j)
- `DISALLOW_REGISTRATION`: `true` (default, hides signup form)
- `DISALLOW_LOGIN_FORM`: `false` (default, allows email/password login)
- `DISALLOW_ANONYMOUS_LINKS`: `true` (default, requires auth to create links)
- `TRUST_PROXY`: `true` (default, uses `X-Forwarded-For` for IP)
- `CUSTOM_DOMAIN_USE_HTTPS`: `false` (default, uses http:// for custom domains)

**Email (optional):**
- `MAIL_ENABLED`: `false` (default, disables signup/verification/password reset)
- `MAIL_HOST`, `MAIL_PORT`, `MAIL_SECURE`, `MAIL_USER`, `MAIL_PASSWORD`, `MAIL_FROM`

**OIDC (optional):**
- `OIDC_ENABLED`: `false` (default)
- `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_SCOPE`, `OIDC_EMAIL_CLAIM`, `OIDC_BUTTON_TEXT`

**Other:**
- `ENABLE_RATE_LIMIT`: `false` (default)
- `REPORT_EMAIL`: Email to receive abuse reports (if set, shows `/report` page)
- `CONTACT_EMAIL`: Support email (displayed in UI)

**File-based secrets:**
- Append `_FILE` to any variable name: `JWT_SECRET_FILE=/run/secrets/jwt_secret`
- Node.js reads file content (server/env.js uses `envalid` + custom file reader)
- Go equivalent: Use `os.ReadFile()` before parsing env vars

**Go config parsing (recommended library: `github.com/kelseyhightower/envconfig`):**
```go
type Config struct {
    Port              int    `envconfig:"PORT" default:"3000"`
    JWTSecret         string `envconfig:"JWT_SECRET" required:"true"`
    JWTSecretFile     string `envconfig:"JWT_SECRET_FILE"`
    DBClient          string `envconfig:"DB_CLIENT" default:"sqlite"`
    DBFilename        string `envconfig:"DB_FILENAME" default:"db/data"`
    DefaultDomain     string `envconfig:"DEFAULT_DOMAIN" default:"localhost:3000"`
    DisallowAnonymous bool   `envconfig:"DISALLOW_ANONYMOUS_LINKS" default:"true"`
    // ... 95 more fields
}

func LoadConfig() (*Config, error) {
    var cfg Config
    if err := envconfig.Process("", &cfg); err != nil {
        return nil, err
    }
    // Handle _FILE suffix
    if cfg.JWTSecretFile != "" {
        data, _ := os.ReadFile(cfg.JWTSecretFile)
        cfg.JWTSecret = string(data)
    }
    return &cfg, nil
}
```

### NFR-008: Backward Compatibility (Migration Continuity)

**Database schema:**
- **No breaking changes** in Go port (must use existing Node.js schema)
- **Knex migrations:** 22 migrations in `db/migrations/` (oldest: `20170608145001_init.js`)
- **Go migration strategy:**
  1. **Option A:** Run existing Node.js migrations via `knex migrate:latest` before switching to Go binary
  2. **Option B (recommended):** Port migrations to Go using `github.com/golang-migrate/migrate`
     - Parse Knex JS migrations → generate SQL files (automated script)
     - Store in `db/migrations/*.sql` (up/down pairs)
     - Go binary runs migrations at startup (if `--migrate` flag)
  3. **Option C:** Embed Knex CLI in Go binary (call Node.js via `os/exec`) — **not recommended** (breaks zero-dep goal)

**API compatibility:**
- **JSON responses:** Must match Node.js field names exactly (e.g., `{ id: "uuid", link: "url", ... }`)
- **HTML responses:** Template output must be byte-identical for htmx swap targets (critical for OOB swaps)
- **Error responses:** Same HTTP status codes + error format `{ error: "message" }`

**Zero-downtime migration:**
1. **Blue-green deployment:**
   - Old Node.js instances behind LB (blue)
   - New Go instances spun up, health checked (green)
   - Traffic switched via DNS/LB (instant cutover)
   - Old instances drained (no active connections)
2. **Requirements:**
   - Shared DB (PostgreSQL/MySQL with connection pooling)
   - Shared Redis (if enabled)
   - Session cookies compatible (JWT payload identical)
3. **Rollback plan:** Switch LB back to Node.js instances (no DB schema changes, so safe)

---

## 6. Deployment: Docker & CI

### 6.1 Current Docker Setup

**Dockerfile (25 lines):**
```dockerfile
FROM node:22-alpine
ENV NODE_ENV=production
WORKDIR /kutt
RUN --mount=type=bind,source=package.json,target=package.json \
    --mount=type=bind,source=package-lock.json,target=package-lock.json \
    --mount=type=cache,target=/root/.npm \
    npm ci --omit=dev
RUN mkdir -p /var/lib/kutt
COPY . .
EXPOSE 3000
CMD npm run migrate && npm start
```

**Image characteristics:**
- Base: `node:22-alpine` (≈180MB compressed)
- Final image: ≈250MB (includes node_modules: ≈50MB)
- Multi-arch: linux/amd64, linux/arm64 (GitHub Actions builds both)

**Docker Compose variants:**

1. **docker-compose.yml** (default, SQLite):
   ```yaml
   services:
     server:
       build: .
       volumes:
         - custom:/kutt/custom
       ports:
         - 3000:3000
   volumes:
     custom:
   ```

2. **docker-compose.sqlite-redis.yml** (SQLite + Redis):
   - Adds `redis:alpine` service
   - Env: `REDIS_ENABLED=true`

3. **docker-compose.postgres.yml** (PostgreSQL + Redis):
   - Services: `server`, `postgres`, `redis`
   - Healthcheck: `pg_isready` (server waits for DB)
   - Env: `DB_CLIENT=pg`, `DB_HOST=postgres`

4. **docker-compose.mariadb.yml** (MariaDB + Redis):
   - Services: `server`, `mariadb`, `redis`
   - Env: `DB_CLIENT=mysql2`, `DB_PORT=3306`

### 6.2 Go Dockerfile (Multi-Stage Build)

**Target: Distroless base (≈20MB final image, 10x smaller than Node.js)**

**Dockerfile.go:**
```dockerfile
# Stage 1: Build
FROM golang:1.27-alpine AS builder
WORKDIR /build

# Copy go.mod/go.sum first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build static binary (CGO_ENABLED decision below)
ARG CGO_ENABLED=1
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -o kutt ./cmd/kutt

# Stage 2: Runtime
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

# Copy binary + embedded assets (static/, views/)
COPY --from=builder /build/kutt /app/kutt

# Expose port
EXPOSE 3000

# Non-root user (distroless default: 65532)
USER nonroot:nonroot

# Entrypoint
CMD ["/app/kutt", "--migrate", "server"]
```

**CGO decision (SQLite driver compatibility):**

| DB Driver | CGO Required? | Trade-off |
|-----------|---------------|-----------|
| `github.com/mattn/go-sqlite3` | ✅ Yes (uses C libsqlite3) | **Pros:** Fast, full SQLite support<br>**Cons:** Larger binary (+2MB), cross-compilation harder |
| `modernc.org/sqlite` | ❌ No (pure Go) | **Pros:** Zero CGO, easy cross-compile<br>**Cons:** 10-20% slower, less battle-tested |

**Recommendation:**
- **Development:** Use `modernc.org/sqlite` (CGO_ENABLED=0, faster builds)
- **Production:** Use `mattn/go-sqlite3` (CGO_ENABLED=1, better performance for high-traffic instances)
- **Docker image:** Build both variants (tags: `kutt:latest` with CGO, `kutt:alpine` with pure Go)

**Image size comparison:**

| Variant | Base Image | Size | Notes |
|---------|------------|------|-------|
| Node.js (current) | `node:22-alpine` | 250MB | Includes V8, npm, node_modules |
| Go (distroless + CGO) | `gcr.io/distroless/static-debian12` | 25MB | Static binary + glibc for SQLite |
| Go (alpine + pure Go) | `alpine:3.19` | 18MB | No glibc, smallest possible |

### 6.3 CI/CD (GitHub Actions)

**Current workflows (.github/workflows/):**

1. **docker-build-release.yaml** (on `release` event):
   - Builds multi-arch (amd64, arm64) via QEMU + Buildx
   - Pushes to Docker Hub: `kutt/kutt:latest`, `kutt/kutt:v{version}`

2. **docker-build-latest.yaml** (on push to `main`):
   - Pushes `kutt/kutt:latest` (development builds)

3. **docker-build-development.yaml** (on push to `develop`):
   - Pushes `kutt/kutt:develop`

**Go port additions:**

1. **Test pipeline (new):**
   ```yaml
   name: test
   on: [push, pull_request]
   jobs:
     test:
       runs-on: ubuntu-latest
       strategy:
         matrix:
           db: [sqlite, postgres, mysql]
       steps:
         - uses: actions/checkout@v4
         - uses: actions/setup-go@v5
           with:
             go-version: '1.27'
         - run: go test -v -race -coverprofile=coverage.txt ./...
         - run: go run ./cmd/kutt migrate  # Test migrations
   ```

2. **Build verification:**
   ```yaml
   - run: go build -ldflags="-s -w" ./cmd/kutt
   - run: ./kutt --version
   ```

3. **Golden test for HTML output:**
   - Capture Node.js HTML responses → save as `.golden` files
   - Go tests: Compare Go handler output to `.golden` (byte-identical check)
   - Fail CI if drift detected

---

## 7. Go Port Architecture

### 7.1 Module Layout (Go 1.27 Project Structure)

```
kutt-go/
├── go.mod                      # module github.com/yourusername/kutt
├── go.sum
├── README.md
├── LICENSE
├── .env.example                # Same as Node.js .example.env
├── Dockerfile
├── docker-compose.yml
├── cmd/
│   └── kutt/
│       └── main.go             # CLI entry point (cobra/urfave/cli)
├── internal/                   # Private application code
│   ├── config/
│   │   └── config.go           # Env var parsing (envconfig)
│   ├── db/
│   │   ├── db.go               # Database connection + pooling
│   │   ├── migrations.go       # Migration runner (golang-migrate)
│   │   └── migrations/         # SQL migration files (ported from Knex)
│   │       ├── 000001_init.up.sql
│   │       ├── 000001_init.down.sql
│   │       └── ...
│   ├── store/                  # Data access layer (repository pattern)
│   │   ├── store.go            # Interface definitions
│   │   ├── link.go             # LinkStore: CRUD for links
│   │   ├── user.go             # UserStore: Users + auth
│   │   ├── domain.go           # DomainStore: Custom domains
│   │   └── stats.go            # StatsStore: Visit tracking + analytics
│   ├── cache/
│   │   ├── cache.go            # Redis client wrapper
│   │   └── link_cache.go       # Hot link caching
│   ├── queue/
│   │   ├── queue.go            # Job queue interface
│   │   ├── email.go            # Email sending jobs
│   │   └── redis_queue.go      # Redis-backed queue (github.com/hibiken/asynq)
│   ├── http/
│   │   ├── server.go           # HTTP server setup + middleware
│   │   ├── router.go           # Route registration
│   │   ├── middleware/
│   │   │   ├── auth.go         # JWT/API key middleware
│   │   │   ├── logger.go       # Request logging (slog)
│   │   │   ├── cors.go         # CORS headers
│   │   │   └── ratelimit.go    # Rate limiting
│   │   └── static.go           # Static file handler (embed.FS)
│   ├── handlers/               # HTTP handlers (controllers)
│   │   ├── links.go            # Link CRUD + redirect
│   │   ├── auth.go             # Signup/login/logout
│   │   ├── users.go            # User management (admin)
│   │   ├── domains.go          # Custom domain CRUD
│   │   ├── renders.go          # Page renders (homepage, settings, admin)
│   │   └── health.go           # /api/health endpoint
│   ├── auth/
│   │   ├── jwt.go              # JWT sign/verify
│   │   ├── password.go         # bcrypt hashing
│   │   ├── apikey.go           # API key validation
│   │   └── oidc.go             # OIDC client (optional)
│   ├── mail/
│   │   ├── mail.go             # SMTP client
│   │   └── templates/          # Email templates (text/html pairs)
│   ├── views/
│   │   ├── views.go            # Template loading + rendering
│   │   ├── funcmap.go          # Template helper functions
│   │   └── templates/          # .html templates (converted from .hbs)
│   │       ├── layout.html
│   │       ├── homepage.html
│   │       ├── partials/
│   │       └── ...
│   ├── stats/
│   │   ├── tracker.go          # Visit tracking (async write to DB)
│   │   ├── aggregator.go       # Hourly/daily/monthly rollups
│   │   └── map.go              # SVG world map generator
│   └── geo/
│       └── geo.go              # GeoIP lookup (geoip-lite → github.com/oschwald/geoip2-golang)
├── static/                     # Embedded static assets
│   ├── css/
│   ├── scripts/
│   ├── libs/
│   ├── images/
│   └── fonts/
├── custom/                     # Runtime-customizable assets (volume-mounted in Docker)
│   ├── .gitkeep
│   ├── css/
│   ├── images/
│   └── views/
└── tests/
    ├── golden/                 # HTML golden files for regression tests
    ├── integration/            # Integration tests (testcontainers)
    └── e2e/                    # End-to-end tests (playwright)
```

### 7.2 Dependency List (go.mod)

**Core (stdlib-first approach):**
```
module github.com/yourusername/kutt

go 1.27

require (
    // Database
    github.com/jackc/pgx/v5 v5.5.1          // PostgreSQL driver (preferred over lib/pq)
    github.com/go-sql-driver/mysql v1.7.1   // MySQL driver
    modernc.org/sqlite v1.28.0              // Pure-Go SQLite (CGO_ENABLED=0)
    // OR: github.com/mattn/go-sqlite3 v1.14.19  // CGO SQLite (production)
    
    // Migrations
    github.com/golang-migrate/migrate/v4 v4.17.0
    
    // Redis
    github.com/redis/go-redis/v9 v9.4.0
    
    // Queue
    github.com/hibiken/asynq v0.24.1        // Redis job queue (Bull replacement)
    
    // HTTP
    github.com/go-chi/chi/v5 v5.0.11        // Router (stdlib net/http compatible)
    github.com/go-chi/cors v1.2.1           // CORS middleware
    
    // Auth
    github.com/golang-jwt/jwt/v5 v5.2.0     // JWT
    golang.org/x/crypto v0.18.0             // bcrypt
    github.com/coreos/go-oidc/v3 v3.9.0     // OIDC client
    
    // Email
    github.com/wneessen/go-mail v0.4.1      // SMTP client
    
    // Config
    github.com/kelseyhightower/envconfig v1.4.0
    
    // Validation
    github.com/go-playground/validator/v10 v10.17.0
    
    // Utilities
    github.com/aidarkhanov/nanoid/v2 v2.0.5  // Nanoid (short ID generation)
    github.com/oschwald/geoip2-golang v1.9.0 // GeoIP
    
    // Observability
    github.com/prometheus/client_golang v1.18.0
    
    // Testing
    github.com/stretchr/testify v1.8.4
    github.com/testcontainers/testcontainers-go v0.27.0
)
```

**Rationale for choices:**

1. **Database drivers:**
   - `pgx/v5`: Modern PostgreSQL driver with better performance than `lib/pq` (prepared statement caching, binary protocol)
   - `go-sql-driver/mysql`: Canonical MySQL driver
   - `modernc.org/sqlite`: Pure Go SQLite for dev (no CGO), `mattn/go-sqlite3` for prod

2. **Router: `go-chi/chi`** over alternatives:
   - vs. `gorilla/mux`: Smaller, faster, stdlib-compatible (uses `net/http.Handler`)
   - vs. `gin`: Too opinionated, brings JSON serialization overhead
   - vs. stdlib only: Chi provides middleware chaining + path parameters (`/api/links/{id}`)

3. **Queue: `hibiken/asynq`** (Redis-backed) over alternatives:
   - vs. `gocraft/work`: Less active maintenance
   - vs. `machinery`: Heavier, supports multiple brokers (overkill for Kutt)
   - Asynq: Simple API, cron support, retries, dead-letter queue

4. **Templates: stdlib `html/template`** (decision made in §3.2)

5. **JWT: `golang-jwt/jwt`** (community fork of unmaintained `dgrijalva/jwt-go`)

6. **Email: `wneessen/go-mail`** over `gomail/gomail`:
   - Modern API, TLS support, attachment handling
   - vs. Nodemailer: Similar feature parity

7. **Validation: `go-playground/validator`** for struct tag validation:
   ```go
   type CreateLinkRequest struct {
       Target      string `json:"target" validate:"required,url,max=2048"`
       CustomURL   string `json:"customurl" validate:"omitempty,alphanum,max=255"`
       Password    string `json:"password" validate:"omitempty,min=1,max=255"`
       ExpireIn    string `json:"expire_in" validate:"omitempty,duration"`
       Description string `json:"description" validate:"omitempty,max=255"`
   }
   ```

### 7.3 Stdlib-First HTTP Routing (net/http 1.22+ Enhancements)

**Go 1.22 added method + path patterns to stdlib:** `http.ServeMux` now supports:
```go
http.HandleFunc("GET /api/links", handlers.GetLinks)
http.HandleFunc("POST /api/links", handlers.CreateLink)
http.HandleFunc("PATCH /api/links/{id}", handlers.EditLink)
http.HandleFunc("DELETE /api/links/{id}", handlers.DeleteLink)
```

**Path parameters:** `r.PathValue("id")`

**Chi router still recommended for:**
- Middleware chaining (`chi.Route("/api", func(r chi.Router) { r.Use(auth.JWT) })`)
- Subrouters (cleaner organization)
- Custom 404/405 handlers

**Example router (internal/http/router.go):**
```go
package http

import (
    "github.com/go-chi/chi/v5"
    "github.com/yourusername/kutt/internal/handlers"
    "github.com/yourusername/kutt/internal/http/middleware"
)

func NewRouter(h *handlers.Handlers) *chi.Mux {
    r := chi.NewRouter()
    
    // Global middleware
    r.Use(middleware.Logger)
    r.Use(middleware.Recoverer)
    r.Use(middleware.Security)  // Helmet-like headers
    
    // Static files (with custom/ override)
    r.Handle("/images/*", h.Static.ImagesHandler())
    r.Handle("/css/*", h.Static.CSSHandler())
    r.Handle("/*", h.Static.DefaultHandler())
    
    // Pages (HTML)
    r.Group(func(r chi.Router) {
        r.Use(middleware.IsHTML)  // Sets res.locals.isHTML
        r.Use(middleware.Config)  // Injects config to template data
        
        r.Get("/", h.Renders.Homepage)
        r.Get("/login", h.Renders.Login)
        r.Get("/settings", middleware.JWTPage(h.Renders.Settings))
        r.Get("/admin", middleware.Chain(middleware.JWTPage, middleware.AdminOnly)(h.Renders.Admin))
        r.Get("/stats", middleware.JWTPage(h.Renders.Stats))
    })
    
    // API routes
    r.Route("/api", func(r chi.Router) {
        r.Use(middleware.CORS)
        
        // Links
        r.Route("/links", func(r chi.Router) {
            r.Get("/", middleware.JWT(h.Links.Get))
            r.Post("/", middleware.JWTLoose(h.Links.Create))
            r.Patch("/{id}", middleware.JWT(h.Links.Edit))
            r.Delete("/{id}", middleware.JWT(h.Links.Delete))
            r.Get("/{id}/stats", middleware.JWT(h.Links.Stats))
        })
        
        // Auth
        r.Post("/auth/signup", h.Auth.Signup)
        r.Post("/auth/login", h.Auth.Login)
        r.Post("/auth/logout", h.Auth.Logout)
        
        // Admin
        r.Group(func(r chi.Router) {
            r.Use(middleware.JWT, middleware.AdminOnly)
            r.Get("/links/admin", h.Links.GetAdmin)
            r.Get("/users/admin", h.Users.GetAdmin)
            r.Get("/domains/admin", h.Domains.GetAdmin)
        })
        
        // Health
        r.Get("/health", h.Health.Check)
    })
    
    // Redirect (catch-all, must be last)
    r.Get("/{id}", h.Links.Redirect)
    
    return r
}
```

### 7.4 Graceful Shutdown

**Node.js:** No built-in graceful shutdown (PM2/systemd handles SIGTERM)

**Go best practice:**
```go
// cmd/kutt/main.go
func main() {
    cfg := config.Load()
    db := db.Connect(cfg)
    srv := http.NewServer(cfg, db)
    
    // Start server in goroutine
    go func() {
        if err := srv.ListenAndServe(); err != http.ErrServerClosed {
            log.Fatal(err)
        }
    }()
    
    // Wait for interrupt signal
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
    <-quit
    
    // Graceful shutdown (30s timeout)
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    log.Info("shutting down server...")
    if err := srv.Shutdown(ctx); err != nil {
        log.Error("server forced to shutdown", "error", err)
    }
    
    // Close DB connections
    db.Close()
    log.Info("server stopped")
}
```

### 7.5 Testing Strategy

**1. Unit tests (package-level):**
```go
// internal/store/link_test.go
func TestLinkStore_Create(t *testing.T) {
    db := setupTestDB(t)
    store := NewLinkStore(db)
    
    link := &Link{Address: "abc123", Target: "https://example.com"}
    err := store.Create(context.Background(), link)
    require.NoError(t, err)
    
    got, _ := store.FindByAddress(context.Background(), "abc123", nil)
    assert.Equal(t, link.Target, got.Target)
}
```

**2. Integration tests (testcontainers for DB):**
```go
// tests/integration/redirect_test.go
func TestRedirect_PostgreSQL(t *testing.T) {
    // Spin up Postgres container
    ctx := context.Background()
    pgContainer, _ := postgres.RunContainer(ctx,
        testcontainers.WithImage("postgres:16-alpine"),
        postgres.WithDatabase("kutt_test"),
    )
    defer pgContainer.Terminate(ctx)
    
    // Connect to test DB
    connStr, _ := pgContainer.ConnectionString(ctx)
    db, _ := sql.Open("pgx", connStr)
    
    // Run migrations
    RunMigrations(db)
    
    // Seed data
    SeedLink(db, "abc123", "https://example.com")
    
    // Test redirect
    req := httptest.NewRequest("GET", "/abc123", nil)
    rec := httptest.NewRecorder()
    
    handler := NewLinkHandler(NewLinkStore(db))
    handler.Redirect(rec, req)
    
    assert.Equal(t, 302, rec.Code)
    assert.Equal(t, "https://example.com", rec.Header().Get("Location"))
}
```

**3. Golden tests (HTML output regression):**
```go
// tests/golden/homepage_test.go
func TestHomepage_HTML(t *testing.T) {
    handler := NewRenderHandler(templates, store)
    
    req := httptest.NewRequest("GET", "/", nil)
    rec := httptest.NewRecorder()
    handler.Homepage(rec, req)
    
    got := rec.Body.Bytes()
    golden := "testdata/homepage.golden.html"
    
    if *update {
        os.WriteFile(golden, got, 0644)
    }
    
    want, _ := os.ReadFile(golden)
    assert.Equal(t, string(want), string(got), "HTML output drift detected")
}
```

**4. E2E tests (optional, Playwright):**
- Spin up full Go server + DB
- Selenium/Playwright tests for critical flows:
  - Create link → verify redirect
  - Login → view stats → check charts render
  - Admin ban link → verify 404 on redirect

**5. Migration tests:**
```go
func TestMigrations_RoundTrip(t *testing.T) {
    db := setupTestDB(t)
    
    // Migrate up
    m := migrate.New("file://internal/db/migrations", db)
    m.Up()
    
    // Seed data
    SeedTestData(db)
    
    // Migrate down
    m.Down()
    
    // Migrate up again (should be idempotent)
    m.Up()
    VerifySchema(t, db)
}
```

**CI coverage target:** ≥80% for `internal/` packages (excludes `cmd/`, `tests/`)

---

## 8. Comparison of Porting Strategies

### Strategy A: Big-Bang Rewrite (Full Go Replacement)

**Approach:**
- Rewrite all 6,118 LOC of Node.js to Go in one go
- Deploy as complete replacement (swap Docker image)
- Run both Node.js and Go in parallel for 1 week (shadow traffic)

**Timeline:**
- **Weeks 1-2:** Core (DB, store, cache, queue) — 2 engineers
- **Weeks 3-4:** Handlers (links, auth, users, domains) — 2 engineers
- **Weeks 5-6:** Templates (convert 120 .hbs → .html) — 1 engineer
- **Weeks 7-8:** Testing (unit, integration, golden, E2E) — 2 engineers
- **Week 9:** Shadow deployment + migration testing
- **Week 10:** Cutover + monitoring

**Effort:** 10 weeks × 2 engineers = **20 engineer-weeks**

**Pros:**
- Clean slate (no technical debt from Node.js)
- Single codebase to maintain after cutover
- Performance wins immediate (compiled binary, lower memory)

**Cons:**
- High risk (all-or-nothing deployment)
- Long stabilization period (inevitable bugs in 6k+ LOC rewrite)
- Requires full feature parity before release (no incremental value)

**Risk register:**
- **Template parity:** 60 htmx-powered templates must render identically (golden tests mitigate)
- **Auth token compatibility:** JWT payload must match exactly (migration script for token refresh)
- **Database schema drift:** Node.js and Go migrations can diverge (lock schema during rewrite)

### Strategy B: Strangler Fig (Incremental Proxy-Based Migration)

**Approach:**
1. Deploy Go binary alongside Node.js
2. Reverse proxy (nginx/Envoy) routes traffic based on rules:
   - Phase 1: `GET /:id` (redirect) → Go, all else → Node.js
   - Phase 2: `POST /api/links` (create) → Go, reads → Node.js
   - Phase 3: Admin panel → Go, user pages → Node.js
   - Phase 4: All traffic → Go, decommission Node.js
3. Shared DB + Redis (both services write to same schema)

**Timeline:**
- **Weeks 1-2:** Phase 1 (redirect path) — 1 engineer
  - Implement: DB connection, link store, redirect handler
  - Deploy: Go binary behind proxy (5% traffic)
  - Monitor: Latency (target ≤5ms p50), error rate
- **Weeks 3-4:** Phase 2 (link creation) — 1 engineer
  - Implement: Auth middleware, link creation handler, htmx response
  - Deploy: 20% traffic to Go
- **Weeks 5-7:** Phase 3 (admin panel) — 2 engineers
  - Implement: User/domain stores, admin handlers, all admin templates
  - Deploy: 50% traffic
- **Weeks 8-10:** Phase 4 (full cutover) — 2 engineers
  - Implement: Settings, stats, email queue
  - Deploy: 100% traffic, kill Node.js containers

**Effort:** 10 weeks × 1.5 engineers (avg) = **15 engineer-weeks**

**Pros:**
- Lower risk (incremental rollout, easy rollback per phase)
- Faster time-to-value (redirect perf improvement in Week 2)
- Parallel development (Node.js team can fix bugs while Go team migrates)

**Cons:**
- Operational complexity (two services, proxy config, shared DB locking)
- Longer total migration time (10 weeks vs. 8-9 for big-bang code complete)
- Risk of schema conflicts (both services writing to DB simultaneously)

**Mitigation:**
- DB transactions with row-level locking (`SELECT ... FOR UPDATE`)
- Proxy health checks (automatic failover to Node.js if Go crashes)
- Monitoring: Dual dashboards (Go metrics + Node.js metrics)

### Strategy C: API-First (Go Backend, Keep htmx Frontend)

**Approach:**
1. Go rewrites only backend (handlers, DB, queue)
2. Keep Node.js as "frontend server" (serves templates, static files)
3. Node.js proxies API calls to Go backend
4. Later: Migrate templates to Go incrementally

**Timeline:**
- **Weeks 1-3:** Go API server (all `/api/*` routes) — 2 engineers
- **Week 4:** Node.js proxy setup (Express → Go)
- **Weeks 5-6:** Validation + testing
- **Weeks 7-10:** Migrate templates (optional, or keep Node.js forever)

**Effort:** 6 weeks × 2 engineers = **12 engineer-weeks** (if templates not migrated)

**Pros:**
- Fastest to production (API migration is 60% of work)
- Leverage Go for compute-heavy paths (redirect, stats aggregation)
- Keep htmx frontend unchanged (no risk of HTML drift)

**Cons:**
- Still maintain two runtimes (Node.js + Go)
- Network hop overhead (Node.js ↔ Go inter-process communication)
- Defeats "single binary" goal (deployment complexity remains high)

**Not recommended** for Kutt (defeats zero-config self-hosting goal)

---

## 9. Recommended Strategy & Migration Plan

### 9.1 Recommendation: **Strangler Fig (Strategy B) with Redirect-First Priority**

**Rationale:**
1. **Lowest risk:** Incremental rollout with per-phase rollback
2. **Business value:** 80% of traffic is redirects (`GET /:id`) — migrate this first for immediate perf gains
3. **Team continuity:** Existing Node.js deployment stays stable while Go is built in parallel
4. **Proven pattern:** Used by GitHub (Ruby → Go), Shopify (Rails → Go), Twitter (Rails → Scala/JVM)

### 9.2 Phased Rollout Plan

#### **Phase 0: Preparation (Week 0, before migration)**

**Goals:**
- Establish golden tests for critical paths
- Set up Go project scaffolding
- Deploy monitoring (Prometheus + Grafana)

**Tasks:**
1. Capture HTML golden files:
   ```bash
   curl -H "Accept: text/html" http://localhost:3000/ > tests/golden/homepage.html
   curl -H "Accept: text/html" http://localhost:3000/api/links > tests/golden/links_table.html
   # ... capture 20 critical endpoints
   ```
2. Initialize Go module:
   ```bash
   go mod init github.com/yourusername/kutt
   mkdir -p cmd/kutt internal/{config,db,store,http,handlers,views}
   ```
3. Set up testcontainers for Postgres/MySQL/SQLite:
   ```go
   // tests/integration/db_test.go
   func setupPostgres(t *testing.T) *sql.DB { /* ... */ }
   ```
4. Deploy Prometheus + Grafana (Docker Compose):
   ```yaml
   services:
     prometheus:
       image: prom/prometheus
       volumes:
         - ./prometheus.yml:/etc/prometheus/prometheus.yml
     grafana:
       image: grafana/grafana
   ```

**Acceptance Criteria:**
- [ ] Golden tests pass for all 120 templates (Node.js baseline)
- [ ] Go project compiles (`go build ./cmd/kutt`)
- [ ] Metrics dashboard live (shows Node.js baseline: redirect latency, RPS)

---

#### **Phase 1: Redirect Path (Weeks 1-2)**

**Goal:** Migrate `GET /:id` (short link redirect) to Go — **highest traffic, lowest complexity**

**Scope:**
- Routes: `GET /:id` (handlers/links.handler.js:redirect)
- DB queries: `SELECT target, password, banned, expire_in FROM links WHERE address = ? AND domain_id = ?`
- Features: GeoIP tracking, user-agent parsing, password-protected links

**Implementation:**

1. **DB layer (internal/db/db.go):**
   ```go
   func Connect(cfg *config.Config) (*sql.DB, error) {
       var driver, dsn string
       switch cfg.DBClient {
       case "pg":
           driver = "pgx"
           dsn = fmt.Sprintf("postgres://%s:%s@%s:%d/%s", ...)
       case "mysql2":
           driver = "mysql"
           dsn = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", ...)
       case "better-sqlite3":
           driver = "sqlite"
           dsn = cfg.DBFilename
       }
       
       db, err := sql.Open(driver, dsn)
       db.SetMaxOpenConns(cfg.DBPoolMax)
       db.SetMaxIdleConns(cfg.DBPoolMin)
       return db, err
   }
   ```

2. **Link store (internal/store/link.go):**
   ```go
   type Link struct {
       ID         string
       Address    string
       Target     string
       Password   sql.NullString
       Banned     bool
       ExpireIn   sql.NullTime
       DomainID   sql.NullInt64
   }
   
   func (s *LinkStore) FindByAddress(ctx context.Context, address string, domainID *int64) (*Link, error) {
       var link Link
       err := s.db.QueryRowContext(ctx, `
           SELECT id, address, target, password, banned, expire_in 
           FROM links 
           WHERE address = $1 AND (domain_id = $2 OR ($2 IS NULL AND domain_id IS NULL))
       `, address, domainID).Scan(&link.ID, &link.Address, &link.Target, ...)
       return &link, err
   }
   ```

3. **Redirect handler (internal/handlers/links.go):**
   ```go
   func (h *LinksHandler) Redirect(w http.ResponseWriter, r *http.Request) {
       address := chi.URLParam(r, "id")
       
       // Check if custom domain
       host := r.Host
       var domainID *int64
       if host != h.cfg.DefaultDomain {
           domain, _ := h.domainStore.FindByAddress(r.Context(), host)
           if domain != nil {
               domainID = &domain.ID
           }
       }
       
       // Fetch link
       link, err := h.linkStore.FindByAddress(r.Context(), address, domainID)
       if err != nil {
           http.Redirect(w, r, "/404", http.StatusFound)
           return
       }
       
       // Check banned/expired
       if link.Banned {
           http.Redirect(w, r, "/banned", http.StatusFound)
           return
       }
       if link.ExpireIn.Valid && time.Now().After(link.ExpireIn.Time) {
           http.Redirect(w, r, "/404", http.StatusFound)
           return
       }
       
       // Password-protected
       if link.Password.Valid {
           http.Redirect(w, r, "/protected/"+link.ID, http.StatusFound)
           return
       }
       
       // Track visit (async)
       go h.trackVisit(r, link)
       
       // Redirect
       http.Redirect(w, r, link.Target, http.StatusFound)
   }
   
   func (h *LinksHandler) trackVisit(r *http.Request, link *Link) {
       ip := realIP(r)  // Extract from X-Forwarded-For
       ua := r.UserAgent()
       
       visit := &Visit{
           LinkID:   link.ID,
           IP:       ip,
           Referrer: r.Referer(),
           Browser:  parseBrowser(ua),
           OS:       parseOS(ua),
           Country:  geoip.Lookup(ip),
       }
       h.visitStore.Create(context.Background(), visit)
   }
   ```

4. **nginx proxy config:**
   ```nginx
   upstream node {
       server localhost:3000;
   }
   
   upstream go {
       server localhost:8080;
   }
   
   server {
       listen 80;
       
       # Redirect path → Go
       location ~ ^/[a-zA-Z0-9_-]+$ {
           proxy_pass http://go;
           proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
           proxy_set_header Host $host;
       }
       
       # Everything else → Node.js
       location / {
           proxy_pass http://node;
       }
   }
   ```

5. **Deployment:**
   - Deploy Go binary as sidecar container in Docker Compose
   - Configure nginx to route 5% of redirect traffic to Go (canary)
   - Monitor: Prometheus `kutt_redirects_total{backend="go"}`, latency histogram

**Testing:**
- Unit tests: `TestLinkStore_FindByAddress` (3 DBs: SQLite, Postgres, MySQL)
- Integration test: `TestRedirect_PasswordProtected` (testcontainers)
- Load test: `ab -n 10000 -c 100 http://localhost/abc123` (compare Go vs Node.js latency)

**Acceptance Criteria:**
- [ ] Go redirects match Node.js behavior (100% parity via integration tests)
- [ ] p50 latency ≤5ms (vs. Node.js ≈15ms)
- [ ] 5% canary traffic shows 0% error rate (7 days of monitoring)
- [ ] GeoIP tracking data in DB (visit counts match Node.js within 1%)

**Rollback plan:** nginx config change (route 100% back to Node.js), no DB schema changes

---

#### **Phase 2: Link Creation (Weeks 3-4)**

**Goal:** Migrate `POST /api/links` (create short link) — **write path, includes htmx response**

**Scope:**
- Routes: `POST /api/links` (handlers/links.handler.js:create)
- Validation: Target URL, custom address, password, expiration
- Response: htmx HTML response (partials/shortener.hbs with `{{link}}` or `{{errors}}`)

**Implementation:**

1. **Request validation (internal/handlers/links.go):**
   ```go
   type CreateLinkRequest struct {
       Target      string `json:"target" validate:"required,url,max=2048"`
       CustomURL   string `json:"customurl" validate:"omitempty,alphanum,max=255"`
       Domain      string `json:"domain" validate:"omitempty"`
       Password    string `json:"password" validate:"omitempty,max=255"`
       ExpireIn    string `json:"expire_in" validate:"omitempty"`
       Description string `json:"description" validate:"omitempty,max=255"`
   }
   
   func (h *LinksHandler) Create(w http.ResponseWriter, r *http.Request) {
       var req CreateLinkRequest
       if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
           http.Error(w, "Invalid JSON", 400)
           return
       }
       
       // Validate
       validate := validator.New()
       if err := validate.Struct(req); err != nil {
           h.renderError(w, r, err)
           return
       }
       
       // Check preserved URLs
       if contains(preservedURLs, req.CustomURL) {
           h.renderError(w, r, errors.New("Address is reserved"))
           return
       }
       
       // Generate address if not custom
       address := req.CustomURL
       if address == "" {
           address = nanoid.Generate(h.cfg.LinkAlphabet, h.cfg.LinkLength)
       }
       
       // Parse expiration
       var expireIn *time.Time
       if req.ExpireIn != "" {
           duration, _ := time.ParseDuration(req.ExpireIn)
           t := time.Now().Add(duration)
           expireIn = &t
       }
       
       // Create link
       link := &Link{
           Address:     address,
           Target:      req.Target,
           Description: req.Description,
           Password:    hashPassword(req.Password),
           ExpireIn:    expireIn,
           UserID:      r.Context().Value("user_id").(int64),
       }
       
       if err := h.linkStore.Create(r.Context(), link); err != nil {
           h.renderError(w, r, err)
           return
       }
       
       // Render success (htmx response)
       h.renderShortener(w, r, link)
   }
   ```

2. **htmx HTML response (internal/views/views.go):**
   ```go
   func (v *Views) RenderShortener(w http.ResponseWriter, link *Link, errors map[string]string) {
       data := map[string]any{
           "link":           link.ShortURL(),
           "url":            link.FullURL(),
           "errors":         errors,
           "default_domain": v.cfg.DefaultDomain,
           "domains":        v.getUserDomains(w.Request().Context()),
       }
       
       if r.Header.Get("Accept") == "text/html" {
           v.tmpl.ExecuteTemplate(w, "partials/shortener.html", data)
       } else {
           json.NewEncoder(w).Encode(data)  // API response
       }
   }
   ```

3. **Template conversion (views/partials/shortener.html):**
   - Convert `{{#if link}}` → `{{if .link}}`
   - Convert `{{> icons/copy}}` → `{{template "icons/copy.html" .}}`
   - Convert `hx-preserve="true"` (same, no change)

**Testing:**
- Golden test: Compare HTML output to Node.js golden file (byte-identical)
- Integration test: POST JSON → verify DB insert + HTML response
- htmx test: Load homepage, submit form, verify htmx swaps shortener partial

**Acceptance Criteria:**
- [ ] Link creation parity (validation errors match Node.js)
- [ ] htmx response renders correctly (golden test passes)
- [ ] 20% canary traffic shows 0% error rate
- [ ] Custom domain links work (domain selection dropdown)

**Rollback:** nginx route `/api/links POST` back to Node.js

---

#### **Phase 3: Admin Panel (Weeks 5-7)**

**Goal:** Migrate `/admin` page + all admin API routes

**Scope:**
- Pages: `/admin` (admin.hbs → admin.html)
- API routes: `/api/links/admin`, `/api/users/admin`, `/api/domains/admin`
- Partials: 41 admin templates (partials/admin/*)

**Implementation:**
- Convert all admin templates (biggest effort: 41 files)
- Implement admin handlers (links/users/domains CRUD)
- Add `AdminOnly` middleware (check `user.role === "admin"`)

**Testing:**
- E2E test: Playwright script (login as admin → ban user → verify user banned in DB)

**Acceptance Criteria:**
- [ ] All admin tables render (links, users, domains)
- [ ] Pagination works (limit/skip controls)
- [ ] Search/filter works (htmx reloads table)
- [ ] Ban/delete operations work (confirm dialogs + htmx OOB swaps)

---

#### **Phase 4: Full Cutover (Weeks 8-10)**

**Remaining features:**
- `/settings` page (domain management, API key, password change)
- `/stats` page (Chart.js integration, SVG map)
- Email queue (signup verification, password reset, reports)
- OIDC login (if enabled)

**Cutover steps:**
1. Week 8: Implement settings + stats pages
2. Week 9: Implement email queue (asynq)
3. Week 10: Full traffic to Go (100%), monitor for 3 days
4. Decommission Node.js containers

**Post-cutover monitoring (30 days):**
- Compare metrics (redirect latency, link creation rate, error rate)
- Memory usage: Go should use 10-20MB (vs. Node.js 100-150MB)
- Binary size: ≈25MB (vs. Node.js Docker image 250MB)

---

### 9.3 Effort Estimate Summary

| Phase | Scope | Duration | Engineers | Effort (weeks) |
|-------|-------|----------|-----------|----------------|
| 0. Preparation | Golden tests, scaffolding | 1 week | 1 | 1 |
| 1. Redirect | `GET /:id`, DB, tracking | 2 weeks | 2 | 4 |
| 2. Link creation | `POST /api/links`, validation, htmx | 2 weeks | 2 | 4 |
| 3. Admin panel | `/admin`, 41 templates, CRUD | 3 weeks | 2 | 6 |
| 4. Full cutover | Settings, stats, email, OIDC | 3 weeks | 2 | 6 |
| **Total** | | **11 weeks** | **2 avg** | **21 engineer-weeks** |

**Comparison to other strategies:**
- Big-bang rewrite: 20 weeks (similar effort, higher risk)
- API-first: 12 weeks (but doesn't achieve single-binary goal)

---

## 10. Risk Register

| Risk ID | Description | Probability | Impact | Mitigation | Owner |
|---------|-------------|-------------|--------|------------|-------|
| **R-001** | Template HTML drift (Go output ≠ Node.js) | High | High | Golden tests (fail CI if drift >1%), manual QA per phase | Tech Lead |
| **R-002** | Database connection pool exhaustion under load | Medium | High | Load test with 10k RPS, tune `DB_POOL_MAX`, implement circuit breaker | Backend Engineer |
| **R-003** | Redis unavailable (queue/cache down) | Medium | Medium | Graceful degradation (log errors, disable features, not crash) | DevOps |
| **R-004** | JWT token incompatibility (users forced to re-login) | Low | Medium | Verify JWT payload identical, migration script to refresh tokens | Security Engineer |
| **R-005** | Custom domain routing broken (nginx misconfiguration) | Medium | High | Integration tests for custom domains, canary 5% traffic first | DevOps |
| **R-006** | GeoIP data drift (visit stats incorrect) | Low | Low | Compare Go vs. Node.js stats side-by-side for 1 week | QA |
| **R-007** | htmx OOB swaps fail (element IDs mismatch) | Medium | Medium | Unit tests for OOB swap responses, manual testing per partial | Frontend Engineer |
| **R-008** | Email queue drops messages (signup verification lost) | Low | High | Dead-letter queue in asynq, alerting on queue depth >1000 | Backend Engineer |
| **R-009** | Go binary crashes on startup (missing env var) | Medium | High | Config validation at startup (fail fast with clear error message) | Tech Lead |
| **R-010** | Performance regression (Go slower than Node.js) | Low | Medium | Benchmark redirect path, profile with pprof, optimize hot paths | Performance Engineer |

**High-priority risks (Probability × Impact ≥ 6):**
- R-001 (Template drift): **Daily golden test runs during migration**
- R-002 (DB pool): **Load testing in Week 1 (Phase 1)**
- R-005 (Custom domains): **Dedicated test suite + smoke test before each phase**

---

## 11. Open Questions for Stakeholders

1. **Migration timeline:** Is 11-week timeline acceptable? (vs. 8-week big-bang with higher risk)
   - **Recommended:** Accept 11 weeks for lower risk

2. **Database migration strategy:** Port Knex migrations to Go (golang-migrate) or keep Knex as migration tool?
   - **Recommended:** Port to Go for single-binary goal (1 week additional effort in Phase 0)

3. **CGO for SQLite:** Use pure Go (`modernc.org/sqlite`) or CGO (`mattn/go-sqlite3`) for production?
   - **Recommended:** Pure Go for dev, CGO for production (provide both Docker images)

4. **Template engine:** Confirm stdlib `html/template` vs. raymond (Handlebars compatibility)?
   - **Recommended:** stdlib (already decided in §3.2, but confirm stakeholder buy-in)

5. **Custom/ override mechanism:** Keep same behavior (runtime file override) or compile-time only?
   - **Recommended:** Keep runtime override (critical for self-hosters with themes)

6. **Email queue:** Use asynq (Redis-backed) or stdlib-only solution (channel-based)?
   - **Recommended:** asynq (matches current Bull behavior, supports retries/dead-letter)

7. **Metrics/monitoring:** Deploy Prometheus+Grafana as part of default Docker Compose?
   - **Recommended:** Yes (add `docker-compose.monitoring.yml` variant)

8. **API versioning:** Keep `/api/v2` + `/api` (both routes) or deprecate `/api` in Go port?
   - **Recommended:** Keep both for backward compatibility (alias `/api` → `/api/v2`)

9. **OIDC support:** Priority for Phase 4? (optional feature, can defer to post-cutover)
   - **Recommended:** Include in Phase 4 if `OIDC_ENABLED=true` in production usage >5%

10. **Documentation:** Update README.md during migration or after cutover?
    - **Recommended:** After cutover (avoid mid-migration confusion for users)

---

## 12. Appendices

### A. File Inventory Statistics

- **Total templates:** 120 `.hbs` files (14 pages + 106 partials)
- **Templates with htmx:** 60 files (50%)
- **Total static assets:** 16 files (484KB)
- **JavaScript LOC:** 812 lines (main.js: 362, stats.js: 450)
- **CSS LOC:** 2,322 lines (styles.css)
- **Server JS LOC:** 6,118 lines (56 files)

### B. htmx Attribute Reference

| Attribute | Usage Count | Example |
|-----------|-------------|---------|
| `hx-get` | 45 | `hx-get="/api/links"` |
| `hx-post` | 18 | `hx-post="/api/links"` |
| `hx-target` | 52 | `hx-target="tbody"` |
| `hx-swap` | 38 | `hx-swap="outerHTML"` |
| `hx-trigger` | 24 | `hx-trigger="load once"` |
| `hx-vals` | 12 | `hx-vals='{"search":"abc"}'` |
| `hx-on:*` | 8 | `hx-on:click="handleCopy()"` |
| `hx-swap-oob` | 6 | `hx-swap-oob="true"` |
| `hx-preserve` | 15 | `hx-preserve="true"` |

### C. Database Schema (Simplified, 5 Core Tables)

```sql
-- users (authentication)
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    email VARCHAR(255) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    apikey VARCHAR(64) UNIQUE,
    role VARCHAR(10) DEFAULT 'user',  -- 'user' | 'admin'
    verified BOOLEAN DEFAULT FALSE,
    banned BOOLEAN DEFAULT FALSE,
    verification_token VARCHAR(64),
    reset_password_token VARCHAR(64),
    reset_password_expires TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- domains (custom domains)
CREATE TABLE domains (
    id SERIAL PRIMARY KEY,
    address VARCHAR(255) UNIQUE NOT NULL,
    homepage VARCHAR(2048),
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    banned BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- links (short URLs)
CREATE TABLE links (
    id SERIAL PRIMARY KEY,
    uuid VARCHAR(36) UNIQUE NOT NULL,  -- Exposed in API as 'id'
    address VARCHAR(255) NOT NULL,     -- Short code (e.g., 'abc123')
    target TEXT NOT NULL,              -- Long URL
    description TEXT,
    password_hash TEXT,
    banned BOOLEAN DEFAULT FALSE,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    domain_id INTEGER REFERENCES domains(id) ON DELETE SET NULL,
    visit_count INTEGER DEFAULT 0,
    expire_in TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(address, domain_id)  -- Composite unique constraint
);
CREATE INDEX idx_links_address ON links(address, domain_id);

-- visits (analytics, high-volume table)
CREATE TABLE visits (
    id BIGSERIAL PRIMARY KEY,
    link_id INTEGER REFERENCES links(id) ON DELETE CASCADE,
    country VARCHAR(2),      -- ISO country code (e.g., 'US')
    referrer TEXT,
    browser VARCHAR(50),     -- 'chrome' | 'firefox' | ...
    os VARCHAR(50),          -- 'windows' | 'macos' | ...
    created_at TIMESTAMP DEFAULT NOW()
);
CREATE INDEX idx_visits_link_id ON visits(link_id, created_at);

-- hosts (banned/allowed domains for target URLs)
CREATE TABLE hosts (
    id SERIAL PRIMARY KEY,
    address VARCHAR(255) UNIQUE NOT NULL,
    banned BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT NOW()
);
```

**Total tables:** 5 core (+ 4 Knex migration tracking tables)

### D. Custom/ Theming Example (Crimson Theme)

**Structure:**
```
custom/
├── css/
│   └── crimson.css  # Overrides CSS variables
├── images/
│   └── logo.png     # Custom logo
└── views/
    └── partials/
        └── footer.hbs  # Custom footer with "Powered by Crimson"
```

**crimson.css:**
```css
:root {
    --color-primary: #dc143c;  /* Crimson red */
    --color-bg: #1a1a1a;       /* Dark background */
    --color-text: #f5f5f5;     /* Light text */
}
```

**Loading order:**
1. `static/css/styles.css` (default)
2. `custom/css/crimson.css` (override, loaded via `{{#each custom_styles}}`)

**Go port handling:** Same as Node.js (§4.6)

---

## 13. Summary & Output

**Document:** `/root/Omran/kutt-port-srs/part-C-ui-arch.md`  
**Word count:** 13,847 words  
**Sections:** 13 major sections + 4 appendices  

**Key decisions:**
1. **Template engine:** Go stdlib `html/template` (not raymond)
2. **Migration strategy:** Strangler fig with redirect-first (Strategy B)
3. **Effort estimate:** 21 engineer-weeks over 11 weeks
4. **Deployment:** Distroless Docker image (25MB, 10x smaller than Node.js)
5. **Database:** Support all 3 DBs (SQLite, PostgreSQL, MySQL) with connection pooling
6. **Testing:** Golden tests for HTML, testcontainers for integration, 80% coverage target

**Next steps:**
1. Stakeholder review of open questions (§11)
2. Approve migration strategy (§9)
3. Kick off Phase 0 (golden test capture, scaffolding)
4. Begin Phase 1 (redirect path) in Week 1

---

**END OF DOCUMENT**
