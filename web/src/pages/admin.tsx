import { useState, type FormEvent } from "react"
import { Navigate, useSearchParams } from "react-router"
import { useQueryClient } from "@tanstack/react-query"
import { Ban, Copy, Lock, Plus, Search, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { TablePager, TextField, TriSelect } from "@/components/controls"
import { ConfirmDialog } from "@/components/dialogs"
import { LinkActions } from "@/components/link-actions"
import {
  ApiError,
  api,
  isAdmin,
  useAdminDomains,
  useAdminLinks,
  useAdminUsers,
  useConfig,
  useMe,
  type AdminDomain,
  type AdminDomainsQuery,
  type AdminLinksQuery,
  type AdminUser,
  type AdminUsersQuery,
  type Role,
  type Tri,
} from "@/lib/api"
import { copyText, formatNumber, msLong, relativeTime, shortUrl, stripProtocol, useDebounced } from "@/lib/format"

type Tab = "links" | "users" | "domains"

function SearchInput({ label, value, onChange, className }: { label: string; value: string; onChange: (v: string) => void; className?: string }) {
  return (
    <div className={`relative ${className ?? "w-full sm:w-56"}`}>
      <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
      <Input type="search" aria-label={label} placeholder={`${label}...`} className="h-7 pl-8" value={value} onChange={(e) => onChange(e.target.value)} />
    </div>
  )
}

function Loading({ cols }: { cols: number }) {
  return (
    <>
      {Array.from({ length: 3 }).map((_, i) => (
        <TableRow key={i}>
          <TableCell colSpan={cols}>
            <Skeleton className="h-6 w-full" />
          </TableCell>
        </TableRow>
      ))}
    </>
  )
}

function Empty({ cols }: { cols: number }) {
  return (
    <TableRow>
      <TableCell colSpan={cols} className="py-10 text-center text-muted-foreground">
        Nothing found.
      </TableCell>
    </TableRow>
  )
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

function AdminLinks({ initialUser, onUserClick }: { initialUser: string; onUserClick: (email: string) => void }) {
  const { data: cfg } = useConfig()
  const [search, setSearch] = useState("")
  const [user, setUser] = useState(initialUser)
  const [domain, setDomain] = useState("")
  const [f, setF] = useState<{ banned: Tri; anonymous: Tri; has_domain: Tri }>({ banned: "", anonymous: "", has_domain: "" })
  const [page, setPage] = useState({ limit: 10, skip: 0 })
  const ds = useDebounced(search)
  const du = useDebounced(user)
  const dd = useDebounced(domain)
  const q: AdminLinksQuery = { ...page, search: ds, user: du, domain: dd, ...f }
  const { data, isPending, isFetching, error } = useAdminLinks(q)
  const reset = () => setPage((p) => ({ ...p, skip: 0 }))

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap gap-2">
        <SearchInput label="Search link" value={search} onChange={(v) => (setSearch(v), reset())} />
        <SearchInput label="Search user" value={user} onChange={(v) => (setUser(v), reset())} className="w-full sm:w-44" />
        <SearchInput label="Search domain" value={domain} onChange={(v) => (setDomain(v), reset())} className="w-full sm:w-44" />
        <TriSelect label="Banned" value={f.banned} onChange={(v) => (setF({ ...f, banned: v as Tri }), reset())} any="Banned..." yes="Banned" no="Not banned" />
        <TriSelect label="Anonymous" value={f.anonymous} onChange={(v) => (setF({ ...f, anonymous: v as Tri }), reset())} any="Anonymous..." yes="Anonymous" no="User" />
        <TriSelect label="Domain" value={f.has_domain} onChange={(v) => (setF({ ...f, has_domain: v as Tri }), reset())} any="Domain..." yes="With domain" no="No domain" />
      </div>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error.message}</AlertDescription>
        </Alert>
      )}
      <div className="overflow-x-auto">
        <Table aria-busy={isFetching}>
          <TableHeader>
            <TableRow>
              <TableHead>Original URL</TableHead>
              <TableHead className="hidden lg:table-cell">Created</TableHead>
              <TableHead>Short link</TableHead>
              <TableHead className="text-right">Views</TableHead>
              <TableHead className="hidden md:table-cell">User</TableHead>
              <TableHead className="w-10">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isPending && <Loading cols={6} />}
            {data?.data.length === 0 && <Empty cols={6} />}
            {data?.data.map((l) => {
              const url = shortUrl(l.link)
              const exp = msLong(l.expire_in)
              return (
                <TableRow key={l.id}>
                  <TableCell className="max-w-[12rem] lg:max-w-xs">
                    <a href={l.target} target="_blank" rel="noopener noreferrer" className="block truncate hover:underline" title={l.target}>
                      {l.target}
                    </a>
                    {l.description && <p className="truncate text-xs text-muted-foreground">{l.description}</p>}
                  </TableCell>
                  <TableCell className="hidden whitespace-nowrap text-muted-foreground lg:table-cell">
                    {relativeTime(l.created_at)}
                    {exp && <p className="text-xs">Expires in {exp}</p>}
                  </TableCell>
                  <TableCell className="max-w-[10rem] lg:max-w-xs">
                    <div className="flex items-center gap-1">
                      <Button variant="ghost" size="icon-xs" aria-label={`Copy ${stripProtocol(url)}`} onClick={() => void copyText(url)}>
                        <Copy />
                      </Button>
                      <a href={url} target="_blank" rel="noopener noreferrer" className="truncate font-medium hover:underline">
                        {stripProtocol(url)}
                      </a>
                      {l.password && <Lock className="size-3.5 shrink-0 text-muted-foreground" aria-label="Password protected" />}
                      {l.banned && <Badge variant="destructive" className="shrink-0">Banned</Badge>}
                    </div>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{formatNumber(l.visit_count)}</TableCell>
                  <TableCell className="hidden max-w-40 truncate md:table-cell">
                    {l.email ? (
                      <button type="button" className="truncate underline-offset-4 hover:underline" onClick={() => onUserClick(l.email!)}>
                        {l.email}
                      </button>
                    ) : (
                      <span className="text-muted-foreground">Anonymous</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <LinkActions
                      admin
                      canBan
                      banned={l.banned}
                      link={{ id: l.id, target: l.target, address: l.address, description: l.description, expire_in: l.expire_in, password: l.password, link: url }}
                    />
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>
      {data && <TablePager total={data.total} limit={page.limit} skip={page.skip} onChange={setPage} loading={isFetching} />}
      {cfg && <p className="text-xs text-muted-foreground">Tip: search domain "{cfg.default_domain}" to list links on the default domain.</p>}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

function CreateUserDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient()
  const { data: cfg } = useConfig()
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [role, setRole] = useState<Role>("USER")
  const [verified, setVerified] = useState(false)
  const [banned, setBanned] = useState(false)
  const [sendEmail, setSendEmail] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    try {
      await api.createUser({ email, password, role, verified, banned, verification_email: sendEmail })
      await qc.invalidateQueries({ queryKey: ["admin", "users"] })
      toast.success("The user has been created successfully.")
      onOpenChange(false)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "An error occurred.")
    } finally {
      setPending(false)
    }
  }

  const roles = [
    { value: "USER", label: "User" },
    { value: "ADMIN", label: "Admin" },
  ]

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <DialogHeader>
            <DialogTitle>Create user</DialogTitle>
            <DialogDescription>Add a new account to this instance.</DialogDescription>
          </DialogHeader>
          <TextField id="new-user-email" label="Email" type="email" placeholder="Email address..." value={email} onChange={(e) => setEmail(e.target.value)} />
          <TextField id="new-user-password" label="Password" type="password" autoComplete="new-password" placeholder="Password..." value={password} onChange={(e) => setPassword(e.target.value)} />
          <div className="grid gap-2">
            <Label htmlFor="new-user-role">Role</Label>
            <Select items={roles} value={role} onValueChange={(v) => setRole((v as Role) ?? "USER")}>
              <SelectTrigger id="new-user-role" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {roles.map((r) => (
                  <SelectItem key={r.value} value={r.value}>
                    {r.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-3">
            <div className="flex items-center gap-2">
              <Switch id="new-user-verified" checked={verified} onCheckedChange={setVerified} />
              <Label htmlFor="new-user-verified">Verified</Label>
            </div>
            <div className="flex items-center gap-2">
              <Switch id="new-user-banned" checked={banned} onCheckedChange={setBanned} />
              <Label htmlFor="new-user-banned">Banned</Label>
            </div>
            {cfg?.mail_enabled && (
              <div className="flex items-center gap-2">
                <Switch id="new-user-mail" checked={sendEmail} onCheckedChange={setSendEmail} />
                <Label htmlFor="new-user-mail">Send verification email</Label>
              </div>
            )}
          </div>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? "Creating…" : "Create user"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function AdminUsers({ initialSearch }: { initialSearch: string }) {
  const qc = useQueryClient()
  const { data: me } = useMe()
  const [search, setSearch] = useState(initialSearch)
  const [f, setF] = useState<{ role: "" | Role; verified: Tri; banned: Tri; domains: Tri; links: Tri }>({
    role: "",
    verified: "",
    banned: "",
    domains: "",
    links: "",
  })
  const [page, setPage] = useState({ limit: 10, skip: 0 })
  const [creating, setCreating] = useState(false)
  const [target, setTarget] = useState<{ kind: "ban" | "delete"; user: AdminUser } | null>(null)
  const ds = useDebounced(search)
  const q: AdminUsersQuery = { ...page, search: ds, ...f }
  const { data, isPending, isFetching, error } = useAdminUsers(q)
  const reset = () => setPage((p) => ({ ...p, skip: 0 }))
  const roles = [
    { value: "", label: "Role..." },
    { value: "ADMIN", label: "Admin" },
    { value: "USER", label: "User" },
  ]

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap gap-2">
        <SearchInput label="Search user" value={search} onChange={(v) => (setSearch(v), reset())} />
        <Select items={roles} value={f.role} onValueChange={(v) => (setF({ ...f, role: (v as "" | Role) ?? "" }), reset())}>
          <SelectTrigger size="sm" aria-label="Role" className="min-w-28">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {roles.map((r) => (
              <SelectItem key={r.value || "any"} value={r.value}>
                {r.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <TriSelect label="Verification" value={f.verified} onChange={(v) => (setF({ ...f, verified: v as Tri }), reset())} any="Verification..." yes="Verified" no="Not verified" />
        <TriSelect label="Banned" value={f.banned} onChange={(v) => (setF({ ...f, banned: v as Tri }), reset())} any="Banned..." yes="Banned" no="Not banned" />
        <TriSelect label="Domains" value={f.domains} onChange={(v) => (setF({ ...f, domains: v as Tri }), reset())} any="Domain..." yes="With domains" no="No domains" />
        <TriSelect label="Links" value={f.links} onChange={(v) => (setF({ ...f, links: v as Tri }), reset())} any="Links..." yes="With links" no="No links" />
        <Button size="sm" className="ml-auto" onClick={() => setCreating(true)}>
          <Plus /> Create user
        </Button>
      </div>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error.message}</AlertDescription>
        </Alert>
      )}
      <div className="overflow-x-auto">
        <Table aria-busy={isFetching}>
          <TableHeader>
            <TableRow>
              <TableHead className="w-14">ID</TableHead>
              <TableHead>Email</TableHead>
              <TableHead className="hidden md:table-cell">Created</TableHead>
              <TableHead className="text-right">Links</TableHead>
              <TableHead className="w-20">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isPending && <Loading cols={5} />}
            {data?.data.length === 0 && <Empty cols={5} />}
            {data?.data.map((u) => (
              <TableRow key={u.id}>
                <TableCell className="tabular-nums text-muted-foreground">{u.id}</TableCell>
                <TableCell className="max-w-64">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="truncate font-medium">{u.email}</span>
                    {String(u.role).toUpperCase() === "ADMIN" && <Badge>Admin</Badge>}
                    {!u.verified && <Badge variant="outline">Not verified</Badge>}
                    {u.banned && <Badge variant="destructive">Banned</Badge>}
                  </div>
                  {u.domains && <p className="truncate text-xs text-muted-foreground">{u.domains}</p>}
                </TableCell>
                <TableCell className="hidden whitespace-nowrap text-muted-foreground md:table-cell">{relativeTime(u.created_at)}</TableCell>
                <TableCell className="text-right tabular-nums">{formatNumber(u.links_count ?? 0)}</TableCell>
                <TableCell>
                  <div className="flex justify-end gap-0.5">
                    {!u.banned && u.id !== me?.id && (
                      <Button variant="ghost" size="icon-sm" aria-label={`Ban ${u.email}`} className="text-destructive" onClick={() => setTarget({ kind: "ban", user: u })}>
                        <Ban />
                      </Button>
                    )}
                    <Button variant="ghost" size="icon-sm" aria-label={`Delete ${u.email}`} className="text-destructive" onClick={() => setTarget({ kind: "delete", user: u })}>
                      <Trash2 />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      {data && <TablePager total={data.total} limit={page.limit} skip={page.skip} onChange={setPage} loading={isFetching} />}
      {creating && <CreateUserDialog open onOpenChange={setCreating} />}
      {target?.kind === "ban" && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setTarget(null)}
          title="Ban user?"
          description={target.user.email}
          confirmLabel="Ban"
          options={[
            { key: "links", label: "Ban user's links" },
            { key: "domains", label: "Ban user's domains" },
          ]}
          onConfirm={async (v) => {
            await api.banUser(target.user.id, v)
            await qc.invalidateQueries({ queryKey: ["admin"] })
            toast.success("Banned user successfully.")
          }}
        />
      )}
      {target?.kind === "delete" && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setTarget(null)}
          title="Delete user?"
          description={<>{target.user.email} — all of the user's links, domains and stats will be deleted.</>}
          confirmLabel="Delete"
          onConfirm={async () => {
            await api.deleteUser(target.user.id)
            await qc.invalidateQueries({ queryKey: ["admin"] })
            toast.success("User has been deleted successfully.")
          }}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Domains
// ---------------------------------------------------------------------------

function AddDomainDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient()
  const [address, setAddress] = useState("")
  const [homepage, setHomepage] = useState("")
  const [banned, setBanned] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    try {
      await api.adminAddDomain({ address, homepage: homepage || undefined, banned })
      await qc.invalidateQueries({ queryKey: ["admin", "domains"] })
      toast.success("The domain has been added successfully.")
      onOpenChange(false)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "An error occurred.")
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <DialogHeader>
            <DialogTitle>Add domain</DialogTitle>
            <DialogDescription>Domains added here have no owner.</DialogDescription>
          </DialogHeader>
          <TextField id="admin-domain-address" label="Address" placeholder="Address..." value={address} onChange={(e) => setAddress(e.target.value)} />
          <TextField id="admin-domain-homepage" label="Homepage" placeholder="Homepage address.." value={homepage} onChange={(e) => setHomepage(e.target.value)} />
          <div className="flex items-center gap-2">
            <Switch id="admin-domain-banned" checked={banned} onCheckedChange={setBanned} />
            <Label htmlFor="admin-domain-banned">Banned</Label>
          </div>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? "Adding…" : "Add domain"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function AdminDomains({ onUserClick }: { onUserClick: (email: string) => void }) {
  const qc = useQueryClient()
  const [search, setSearch] = useState("")
  const [user, setUser] = useState("")
  const [f, setF] = useState<{ banned: Tri; owner: Tri; links: Tri }>({ banned: "", owner: "", links: "" })
  const [page, setPage] = useState({ limit: 10, skip: 0 })
  const [adding, setAdding] = useState(false)
  const [target, setTarget] = useState<{ kind: "ban" | "delete"; domain: AdminDomain } | null>(null)
  const ds = useDebounced(search)
  const du = useDebounced(user)
  const q: AdminDomainsQuery = { ...page, search: ds, user: du, ...f }
  const { data, isPending, isFetching, error } = useAdminDomains(q)
  const reset = () => setPage((p) => ({ ...p, skip: 0 }))

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap gap-2">
        <SearchInput label="Search domain" value={search} onChange={(v) => (setSearch(v), reset())} />
        <SearchInput label="Search user" value={user} onChange={(v) => (setUser(v), reset())} className="w-full sm:w-44" />
        <TriSelect label="Banned" value={f.banned} onChange={(v) => (setF({ ...f, banned: v as Tri }), reset())} any="Banned..." yes="Banned" no="Not banned" />
        <TriSelect label="Owner" value={f.owner} onChange={(v) => (setF({ ...f, owner: v as Tri }), reset())} any="Owner..." yes="With owner" no="No owner" />
        <TriSelect label="Links" value={f.links} onChange={(v) => (setF({ ...f, links: v as Tri }), reset())} any="Links..." yes="With links" no="No links" />
        <Button size="sm" className="ml-auto" onClick={() => setAdding(true)}>
          <Plus /> Add domain
        </Button>
      </div>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error.message}</AlertDescription>
        </Alert>
      )}
      <div className="overflow-x-auto">
        <Table aria-busy={isFetching}>
          <TableHeader>
            <TableRow>
              <TableHead className="w-14">ID</TableHead>
              <TableHead>Address</TableHead>
              <TableHead className="hidden md:table-cell">Homepage</TableHead>
              <TableHead className="hidden md:table-cell">Owner</TableHead>
              <TableHead className="text-right">Links</TableHead>
              <TableHead className="w-20">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isPending && <Loading cols={6} />}
            {data?.data.length === 0 && <Empty cols={6} />}
            {data?.data.map((d) => (
              <TableRow key={d.id}>
                <TableCell className="tabular-nums text-muted-foreground">{d.id}</TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <span className="font-medium">{d.address}</span>
                    {d.banned && <Badge variant="destructive">Banned</Badge>}
                  </div>
                  <p className="text-xs text-muted-foreground">{relativeTime(d.created_at)}</p>
                </TableCell>
                <TableCell className="hidden max-w-48 truncate text-muted-foreground md:table-cell">{d.homepage || "—"}</TableCell>
                <TableCell className="hidden max-w-40 truncate md:table-cell">
                  {d.email ? (
                    <button type="button" className="truncate underline-offset-4 hover:underline" onClick={() => onUserClick(d.email!)}>
                      {d.email}
                    </button>
                  ) : (
                    <span className="text-muted-foreground">No owner</span>
                  )}
                </TableCell>
                <TableCell className="text-right tabular-nums">{formatNumber(d.links_count ?? 0)}</TableCell>
                <TableCell>
                  <div className="flex justify-end gap-0.5">
                    {!d.banned && (
                      <Button variant="ghost" size="icon-sm" aria-label={`Ban ${d.address}`} className="text-destructive" onClick={() => setTarget({ kind: "ban", domain: d })}>
                        <Ban />
                      </Button>
                    )}
                    <Button variant="ghost" size="icon-sm" aria-label={`Delete ${d.address}`} className="text-destructive" onClick={() => setTarget({ kind: "delete", domain: d })}>
                      <Trash2 />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      {data && <TablePager total={data.total} limit={page.limit} skip={page.skip} onChange={setPage} loading={isFetching} />}
      {adding && <AddDomainDialog open onOpenChange={setAdding} />}
      {target?.kind === "ban" && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setTarget(null)}
          title="Ban domain?"
          description={target.domain.address}
          confirmLabel="Ban"
          options={[
            { key: "links", label: "Ban domain's links" },
            ...(target.domain.user_id ? [{ key: "user", label: "Ban domain's owner" }] : []),
          ]}
          onConfirm={async (v) => {
            await api.banDomain(target.domain.id, v)
            await qc.invalidateQueries({ queryKey: ["admin"] })
            toast.success("Banned domain successfully.")
          }}
        />
      )}
      {target?.kind === "delete" && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setTarget(null)}
          title="Delete domain?"
          description={target.domain.address}
          confirmLabel="Delete"
          options={[{ key: "links", label: "Delete all links of this domain" }]}
          onConfirm={async (v) => {
            await api.adminDeleteDomain(target.domain.id, v.links || undefined)
            await qc.invalidateQueries({ queryKey: ["admin"] })
            toast.success("Domain deleted successfully")
          }}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------

export default function AdminPage() {
  const { data: me, isPending } = useMe()
  const [params, setParams] = useSearchParams()
  const tab = (params.get("tab") as Tab) || "links"
  const userFilter = params.get("user") ?? ""
  const searchFilter = params.get("search") ?? ""

  if (isPending) return <Skeleton className="h-96 w-full" />
  if (!me) return <Navigate to="/login" replace />
  if (!isAdmin(me)) return <Navigate to="/404" replace />

  const go = (t: Tab, extra: Record<string, string> = {}) => setParams({ tab: t, ...extra })

  return (
    <div className="grid gap-6">
      <h1 className="text-3xl font-bold tracking-tight">Admin</h1>
      <Tabs value={tab} onValueChange={(v) => go(v as Tab)}>
        <TabsList aria-label="Admin sections">
          <TabsTrigger value="links" className="px-3">
            Links
          </TabsTrigger>
          <TabsTrigger value="users" className="px-3">
            Users
          </TabsTrigger>
          <TabsTrigger value="domains" className="px-3">
            Domains
          </TabsTrigger>
        </TabsList>
        <Card className="mt-2">
          <CardContent>
            {/* key= remounts the tab when navigated to with a filter (Kutt's user-email click). */}
            <TabsContent value="links">
              <AdminLinks key={`l-${userFilter}`} initialUser={userFilter} onUserClick={(email) => go("users", { search: email })} />
            </TabsContent>
            <TabsContent value="users">
              <AdminUsers key={`u-${searchFilter}`} initialSearch={searchFilter} />
            </TabsContent>
            <TabsContent value="domains">
              <AdminDomains onUserClick={(email) => go("users", { search: email })} />
            </TabsContent>
          </CardContent>
        </Card>
      </Tabs>
    </div>
  )
}
