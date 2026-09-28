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
