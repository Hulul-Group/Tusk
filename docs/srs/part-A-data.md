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
