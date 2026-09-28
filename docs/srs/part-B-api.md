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
