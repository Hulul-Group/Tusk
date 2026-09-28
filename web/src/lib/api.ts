/**
 * Typed client for Kutt's /api/v2 JSON API (the only backend the SPA talks to).
 *
 * Every endpoint the UI uses lives in this file so the shapes can be reconciled
 * against the Go handlers in one place. Shapes follow Kutt v3.2.6
 * (server/utils/utils.js `sanitize.*`, server/handlers/*.handler.js). Anything
 * that Kutt did not expose as JSON (because it rendered HTML) is marked
 * "TUSK EXTENSION" and is listed in docs/CONTRACTS.md.
 *
 * Auth: the httpOnly `token` cookie set by POST /api/v2/auth/login. We always
 * send credentials: "include" and never read or store the JWT in JS.
 */
import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query"

export const API_BASE = "/api/v2"

// ---------------------------------------------------------------------------
// Shapes
// ---------------------------------------------------------------------------

/** GET /api/v2/config (TUSK EXTENSION, public). Mirrors Kutt's res.locals. */
export interface PublicConfig {
  site_name: string
  default_domain: string
  disallow_registration: boolean
  disallow_anonymous_links: boolean
  disallow_login_form: boolean
  oidc_enabled: boolean
  oidc_button_text: string
  mail_enabled: boolean
  report_email: string
  contact_email: string
  server_ip_address: string
  server_cname_address: string
  /** TUSK EXTENSION (optional): true when the users table is empty → /create-admin. */
  needs_admin_setup?: boolean
}

export type Role = "ADMIN" | "USER"

/** sanitize.domain */
export interface Domain {
  id: string
  address: string
  homepage: string
  banned: boolean
  created_at: string
  updated_at: string
}

/** GET /api/v2/users — Kutt returns {apikey, email, domains}; Tusk adds id/role/verified. */
export interface CurrentUser {
  apikey: string | null
  email: string
  domains: Domain[]
  id?: number
  role?: Role | string
  verified?: boolean
}

/** sanitize.link */
export interface Link {
  id: string
  address: string
  banned: boolean
  created_at: string
  updated_at: string
  description: string | null
  expire_in: string | null
  password: boolean
  target: string
  visit_count: number
  domain: string | null
  /** full short URL, e.g. https://kutt.it/abc */
  link: string
}

/** sanitize.link_admin (JSON variant) — adds owner email; domain defaults to DEFAULT_DOMAIN. */
export interface AdminLink extends Omit<Link, "link" | "visit_count"> {
  email: string | null
  user_id: number | null
  visit_count: number | string
  link: string | { address: string; link: string; url: string }
}

export interface AdminUser {
  id: number
  email: string
  verified: boolean
  role: Role | string
  banned: boolean
  created_at: string
  updated_at: string
  links_count: number | string
  domains: string | null
}

export interface AdminDomain {
  id: number
  uuid: string
  address: string
  homepage: string | null
  banned: boolean
  created_at: string
  updated_at: string
  user_id: number | null
  email: string | null
  links_count: number | string
}

export interface Paginated<T> {
  total: number
  limit: number
  skip: number
  data: T[]
}

export interface StatItem {
  name: string
  value: number
}

export interface PeriodStats {
  total: number
  views: number[]
  stats: {
    browser: StatItem[]
    os: StatItem[]
    country: StatItem[]
    referrer: StatItem[]
  }
}

export type Period = "lastDay" | "lastWeek" | "lastMonth" | "lastYear"

export type LinkStats = Link & Record<Period, PeriodStats> & { updatedAt: string }

export interface CreateLinkInput {
  target: string
  customurl?: string
  password?: string
  expire_in?: string
  description?: string
  domain?: string
  reuse?: boolean
}

export interface EditLinkInput {
  target?: string
  address?: string
  password?: string
  expire_in?: string
  description?: string
}

export interface BanLinkInput {
  host?: boolean
  user?: boolean
  userLinks?: boolean
  domain?: boolean
}

export interface CreateUserInput {
  email: string
  password: string
  role?: Role
  verified?: boolean
  banned?: boolean
  verification_email?: boolean
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

/** Kutt error body: { error: string } (helpers.handler.js `error`). */
export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
  }
}

type Query = object

function qs(params?: Query): string {
  if (!params) return ""
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params as Record<string, unknown>)) {
    if (v === undefined || v === null || v === "") continue
    sp.set(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ""
}

export async function request<T>(
  method: "GET" | "POST" | "PATCH" | "DELETE",
  path: string,
  opts: { body?: unknown; query?: Query; signal?: AbortSignal } = {},
): Promise<T> {
  const res = await fetch(`${API_BASE}${path}${qs(opts.query)}`, {
    method,
    credentials: "include",
    headers: {
      Accept: "application/json",
      ...(opts.body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    signal: opts.signal,
  })
  const text = await res.text()
  let data: unknown = undefined
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    let msg = res.statusText || "An error occurred."
    if (data && typeof data === "object" && "error" in data) msg = String((data as { error: unknown }).error)
    else if (typeof data === "string" && data.length < 300) msg = data
    throw new ApiError(res.status, msg)
  }
  return data as T
}

// ---------------------------------------------------------------------------
// Raw endpoint functions
// ---------------------------------------------------------------------------

export const api = {
  // config / session
  config: (signal?: AbortSignal) => request<PublicConfig>("GET", "/config", { signal }),
  me: (signal?: AbortSignal) => request<CurrentUser>("GET", "/users", { signal }),

  // auth
  login: (email: string, password: string) =>
    request<{ token: string }>("POST", "/auth/login", { body: { email, password } }),
  signup: (email: string, password: string) =>
    request<{ message: string }>("POST", "/auth/signup", { body: { email, password } }),
  createAdmin: (email: string, password: string) =>
    request<{ token: string }>("POST", "/auth/create-admin", { body: { email, password } }),
  /** TUSK EXTENSION: clears the httpOnly cookie (Kutt did this in the /logout page render). */
  logout: () => request<unknown>("POST", "/auth/logout"),
  changePassword: (currentpassword: string, newpassword: string) =>
    request<{ message: string }>("POST", "/auth/change-password", { body: { currentpassword, newpassword } }),
  changeEmail: (email: string, password: string) =>
    request<{ message: string }>("POST", "/auth/change-email", { body: { email, password } }),
  generateApiKey: () => request<{ apikey: string }>("POST", "/auth/apikey"),
  resetPassword: (email: string) => request<{ message: string }>("POST", "/auth/reset-password", { body: { email } }),
  newPassword: (reset_password_token: string, new_password: string, repeat_password: string) =>
    request<{ message?: string }>("POST", "/auth/new-password", {
      body: { reset_password_token, new_password, repeat_password },
    }),
  /** TUSK EXTENSION: JSON form of GET /verify/:token. Sets the cookie on success. */
  verifyEmail: (token: string) => request<{ token?: string; message?: string }>("POST", `/auth/verify/${encodeURIComponent(token)}`),
  /** TUSK EXTENSION: JSON form of GET /verify-email/:token (change-email confirmation). */
  verifyChangeEmail: (token: string) =>
    request<{ token?: string; message?: string }>("POST", `/auth/verify-email/${encodeURIComponent(token)}`),
  deleteAccount: (password: string) => request<unknown>("POST", "/users/delete", { body: { password } }),

  // links
  links: (q: { limit: number; skip: number; search?: string }, signal?: AbortSignal) =>
    request<Paginated<Link>>("GET", "/links", { query: q, signal }),
  createLink: (input: CreateLinkInput) => request<Link>("POST", "/links", { body: input }),
  editLink: (id: string, input: EditLinkInput) => request<Link>("PATCH", `/links/${id}`, { body: input }),
  deleteLink: (id: string) => request<{ message: string }>("DELETE", `/links/${id}`),
  linkStats: (id: string, signal?: AbortSignal) => request<LinkStats>("GET", `/links/${id}/stats`, { signal }),
  report: (link: string) => request<{ message: string }>("POST", "/links/report", { body: { link } }),

  // domains (user)
  addDomain: (address: string, homepage?: string) =>
    request<Domain>("POST", "/domains", { body: { address, homepage: homepage || undefined } }),
  removeDomain: (id: string) => request<{ message: string }>("DELETE", `/domains/${id}`),

  // admin
  adminLinks: (q: AdminLinksQuery, signal?: AbortSignal) =>
    request<Paginated<AdminLink>>("GET", "/links/admin", { query: q, signal }),
  adminEditLink: (id: string, input: EditLinkInput) => request<Link>("PATCH", `/links/admin/${id}`, { body: input }),
  banLink: (id: string, input: BanLinkInput) => request<{ message: string }>("POST", `/links/admin/ban/${id}`, { body: input }),
  adminUsers: (q: AdminUsersQuery, signal?: AbortSignal) =>
    request<Paginated<AdminUser>>("GET", "/users/admin", { query: q, signal }),
  createUser: (input: CreateUserInput) => request<{ message: string }>("POST", "/users/admin", { body: input }),
  deleteUser: (id: number) => request<{ message: string }>("DELETE", `/users/admin/${id}`),
  banUser: (id: number, input: { links?: boolean; domains?: boolean }) =>
    request<{ message: string }>("POST", `/users/admin/ban/${id}`, { body: input }),
  adminDomains: (q: AdminDomainsQuery, signal?: AbortSignal) =>
    request<Paginated<AdminDomain>>("GET", "/domains/admin", { query: q, signal }),
  adminAddDomain: (input: { address: string; homepage?: string; banned?: boolean }) =>
    request<{ message: string }>("POST", "/domains/admin", { body: input }),
  adminDeleteDomain: (id: number, links?: boolean) =>
    request<{ message: string }>("DELETE", `/domains/admin/${id}`, { query: { links } }),
  banDomain: (id: number, input: { links?: boolean; user?: boolean }) =>
    request<{ message: string }>("POST", `/domains/admin/ban/${id}`, { body: input }),
}

export type Tri = "" | "true" | "false"

export interface AdminLinksQuery {
  limit: number
  skip: number
  search?: string
  user?: string
  domain?: string
  banned?: Tri
  anonymous?: Tri
  has_domain?: Tri
}

export interface AdminUsersQuery {
  limit: number
  skip: number
  search?: string
  role?: "" | Role
  verified?: Tri
  banned?: Tri
  domains?: Tri
  links?: Tri
}

export interface AdminDomainsQuery {
  limit: number
  skip: number
  search?: string
  user?: string
  banned?: Tri
  owner?: Tri
  links?: Tri
}

// ---------------------------------------------------------------------------
// TanStack Query hooks
// ---------------------------------------------------------------------------

export const qk = {
  config: ["config"] as const,
  me: ["me"] as const,
  links: (q: object) => ["links", q] as const,
  stats: (id: string) => ["stats", id] as const,
  adminLinks: (q: object) => ["admin", "links", q] as const,
  adminUsers: (q: object) => ["admin", "users", q] as const,
  adminDomains: (q: object) => ["admin", "domains", q] as const,
}

const FALLBACK_CONFIG: PublicConfig = {
  site_name: "Kutt",
  default_domain: typeof window !== "undefined" ? window.location.host : "localhost:3000",
  disallow_registration: true,
  disallow_anonymous_links: true,
  disallow_login_form: false,
  oidc_enabled: false,
  oidc_button_text: "Log in with OIDC",
  mail_enabled: false,
  report_email: "",
  contact_email: "",
  server_ip_address: "",
  server_cname_address: "",
}

export function useConfig() {
  return useQuery({
    queryKey: qk.config,
    queryFn: async ({ signal }) => ({ ...FALLBACK_CONFIG, ...(await api.config(signal)) }),
    staleTime: 5 * 60_000,
    placeholderData: FALLBACK_CONFIG,
  })
}

/** Current user or null when not logged in (401). */
export function useMe() {
  return useQuery({
    queryKey: qk.me,
    queryFn: async ({ signal }) => {
      try {
        return await api.me(signal)
      } catch (e) {
        if (e instanceof ApiError && (e.status === 401 || e.status === 403)) return null
        throw e
      }
    },
    staleTime: 60_000,
    retry: false,
  })
}

export function isAdmin(user: CurrentUser | null | undefined): boolean {
  return !!user?.role && String(user.role).toUpperCase() === "ADMIN"
}

export function useLinks(q: { limit: number; skip: number; search?: string }, enabled = true) {
  return useQuery({
    queryKey: qk.links(q),
    queryFn: ({ signal }) => api.links(q, signal),
    placeholderData: keepPreviousData,
    enabled,
  })
}

export function useLinkStats(id: string) {
  return useQuery({
    queryKey: qk.stats(id),
    queryFn: ({ signal }) => api.linkStats(id, signal),
    enabled: !!id,
    retry: false,
  })
}

export function useAdminLinks(q: AdminLinksQuery) {
  return useQuery({
    queryKey: qk.adminLinks(q),
    queryFn: ({ signal }) => api.adminLinks(q, signal),
    placeholderData: keepPreviousData,
  })
}

export function useAdminUsers(q: AdminUsersQuery) {
  return useQuery({
    queryKey: qk.adminUsers(q),
    queryFn: ({ signal }) => api.adminUsers(q, signal),
    placeholderData: keepPreviousData,
  })
}

export function useAdminDomains(q: AdminDomainsQuery) {
  return useQuery({
    queryKey: qk.adminDomains(q),
    queryFn: ({ signal }) => api.adminDomains(q, signal),
    placeholderData: keepPreviousData,
  })
}

/** Invalidate everything that lists links (user + admin tables). */
export function invalidateLinks(qc: QueryClient) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: ["links"] }),
    qc.invalidateQueries({ queryKey: ["admin"] }),
  ])
}

/** Small helper: a mutation that invalidates the given keys on success. */
export function useApiMutation<TVars, TData>(
  fn: (v: TVars) => Promise<TData>,
  opts: { invalidate?: ReadonlyArray<readonly unknown[]>; onSuccess?: (d: TData, v: TVars) => void } = {},
) {
  const qc = useQueryClient()
  return useMutation<TData, ApiError, TVars>({
    mutationFn: fn,
    onSuccess: async (d, v) => {
      await Promise.all((opts.invalidate ?? []).map((k) => qc.invalidateQueries({ queryKey: k })))
      opts.onSuccess?.(d, v)
    },
  })
}
