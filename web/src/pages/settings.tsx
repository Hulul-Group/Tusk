import { useState, type FormEvent, type ReactNode } from "react"
import { Navigate, useNavigate } from "react-router"
import { useQueryClient } from "@tanstack/react-query"
import { Copy, Eye, EyeOff, KeyRound, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Input } from "@/components/ui/input"
import { TextField } from "@/components/controls"
import { ConfirmDialog } from "@/components/dialogs"
import { ApiError, api, qk, useConfig, useMe, type CurrentUser, type Domain, type PublicConfig } from "@/lib/api"
import { copyText } from "@/lib/format"

function Section({ title, description, children, footer }: { title: string; description?: ReactNode; children: ReactNode; footer?: ReactNode }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="text-lg">{title}</h2>
        </CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="grid gap-4">{children}</CardContent>
      {footer && <CardFooter>{footer}</CardFooter>}
    </Card>
  )
}

function errMsg(e: unknown) {
  return e instanceof ApiError ? e.message : "An error occurred."
}

function DomainsSection({ cfg, user }: { cfg: PublicConfig; user: CurrentUser }) {
  const qc = useQueryClient()
  const [showForm, setShowForm] = useState(false)
  const [address, setAddress] = useState("")
  const [homepage, setHomepage] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [deleting, setDeleting] = useState<Domain | null>(null)

  const ip = cfg.server_ip_address ? <b className="font-mono">{cfg.server_ip_address}</b> : <>our <b>IP address</b></>

  const add = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    try {
      await api.addDomain(address.trim(), homepage.trim())
      await qc.invalidateQueries({ queryKey: qk.me })
      toast.success("Domain added")
      setAddress("")
      setHomepage("")
      setShowForm(false)
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Section
      title="Custom domain"
      description={
        <>
          You can set a custom domain for your short URLs, so instead of <b>{cfg.default_domain}/shorturl</b> you can have{" "}
          <b>yoursite.com/shorturl</b>.
        </>
      }
    >
      <Alert>
        <AlertDescription className="block">
          {cfg.server_cname_address ? (
            <>
              Point your domain's A record to {ip} or your subdomain's CNAME record to{" "}
              <b className="font-mono">{cfg.server_cname_address}</b>. If you're using <b>Cloudflare</b>, make sure to use{" "}
              <b>DNS only</b> mode for your subdomain. Then add the domain via the form below.
            </>
          ) : (
            <>Point your domain's A record to {ip} then add the domain via the form below.</>
          )}
        </AlertDescription>
      </Alert>

      {user.domains.length > 0 && (
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Domain</TableHead>
                <TableHead>Homepage</TableHead>
                <TableHead className="w-10">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {user.domains.map((d) => (
                <TableRow key={d.id}>
                  <TableCell className="font-medium">
                    {d.address} {d.banned && <Badge variant="destructive">Banned</Badge>}
                  </TableCell>
                  <TableCell className="max-w-48 truncate text-muted-foreground">{d.homepage}</TableCell>
                  <TableCell>
                    <Button variant="ghost" size="icon-sm" aria-label={`Delete ${d.address}`} className="text-destructive" onClick={() => setDeleting(d)}>
                      <Trash2 />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {showForm ? (
        <form onSubmit={add} className="grid gap-4 sm:grid-cols-2" noValidate>
          <TextField id="domain-address" label="Address" placeholder="yoursite.com" required value={address} onChange={(e) => setAddress(e.target.value)} />
          <TextField id="domain-homepage" label="Homepage (optional)" placeholder="Homepage URL" value={homepage} onChange={(e) => setHomepage(e.target.value)} />
          {error && (
            <Alert variant="destructive" className="sm:col-span-2">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <div className="flex gap-2 sm:col-span-2">
            <Button type="submit" disabled={pending}>
              {pending ? "Adding…" : "Add domain"}
            </Button>
            <Button type="button" variant="ghost" onClick={() => setShowForm(false)}>
              Cancel
            </Button>
          </div>
        </form>
      ) : (
        <div>
          <Button variant="secondary" onClick={() => setShowForm(true)}>
            <Plus /> Add domain
          </Button>
        </div>
      )}

      {deleting && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setDeleting(null)}
          title="Delete domain?"
          description={deleting.address}
          confirmLabel="Delete"
          onConfirm={async () => {
            await api.removeDomain(deleting.id)
            await qc.invalidateQueries({ queryKey: qk.me })
            toast.success("Domain deleted")
          }}
        />
      )}
    </Section>
  )
}

function ApiKeySection({ user }: { user: CurrentUser }) {
  const qc = useQueryClient()
  const [shown, setShown] = useState(false)
  const [pending, setPending] = useState(false)
  const key = user.apikey

  const generate = async () => {
    setPending(true)
    try {
      const { apikey } = await api.generateApiKey()
      qc.setQueryData<CurrentUser | null>(qk.me, (u) => (u ? { ...u, apikey } : u))
      setShown(true)
      toast.success("API key generated")
    } catch (e) {
      toast.error(errMsg(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Section
      title="API"
      description={
        <>
          In addition to this website, you can use the API to create, delete and get shortened URLs. If you're not familiar with
          API, don't generate the key. Send it as the <code className="font-mono">X-API-KEY</code> header.
        </>
      }
    >
      {key && (
        <div className="flex items-center gap-2">
          <Input readOnly aria-label="API key" value={shown ? key : "•".repeat(Math.min(key.length, 40))} className="font-mono" />
          <Button variant="outline" size="icon" aria-label={shown ? "Hide API key" : "Show API key"} onClick={() => setShown((s) => !s)}>
            {shown ? <EyeOff /> : <Eye />}
          </Button>
          <Button variant="outline" size="icon" aria-label="Copy API key" onClick={() => void copyText(key, "API key copied")}>
            <Copy />
          </Button>
        </div>
      )}
      <div>
        <Button onClick={() => void generate()} disabled={pending}>
          <KeyRound /> {pending ? "Generating…" : key ? "Regenerate key" : "Generate key"}
        </Button>
      </div>
    </Section>
  )
}

function ChangePasswordSection() {
  const [cur, setCur] = useState("")
  const [next, setNext] = useState("")
  const [err, setErr] = useState<{ cur?: string; next?: string; form?: string }>({})
  const [pending, setPending] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setErr({})
    try {
      await api.changePassword(cur, next)
      toast.success("Password has been changed.")
      setCur("")
      setNext("")
    } catch (e2) {
      const m = errMsg(e2)
      if (/current/i.test(m)) setErr({ cur: m })
      else if (/password/i.test(m)) setErr({ next: m })
      else setErr({ form: m })
    } finally {
      setPending(false)
    }
  }

  return (
    <Section title="Change password" description="Enter your current password and a new password to change it.">
      <form className="grid gap-4 sm:grid-cols-2" onSubmit={submit} noValidate>
        <TextField id="currentpassword" label="Current password" type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} error={err.cur} />
        <TextField id="newpassword" label="New password" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} error={err.next} />
        {err.form && (
          <Alert variant="destructive" className="sm:col-span-2">
            <AlertDescription>{err.form}</AlertDescription>
          </Alert>
        )}
        <div className="sm:col-span-2">
          <Button type="submit" disabled={pending}>
            {pending ? "Updating…" : "Update"}
          </Button>
        </div>
      </form>
    </Section>
  )
}

function ChangeEmailSection({ user }: { user: CurrentUser }) {
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [err, setErr] = useState<{ email?: string; password?: string; form?: string }>({})
  const [success, setSuccess] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setErr({})
    setSuccess(null)
    try {
      const r = await api.changeEmail(email, password)
      setSuccess(r?.message ?? "A verification link has been sent to the requested email address.")
      setEmail("")
      setPassword("")
    } catch (e2) {
      const m = errMsg(e2)
      if (/email/i.test(m)) setErr({ email: m })
      else if (/password/i.test(m)) setErr({ password: m })
      else setErr({ form: m })
    } finally {
      setPending(false)
    }
  }

  return (
    <Section title="Change email" description={<>Current email: <b>{user.email}</b>. A verification link will be sent to the new address.</>}>
      <form className="grid gap-4 sm:grid-cols-2" onSubmit={submit} noValidate>
        <TextField id="change-email-password" label="Password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} error={err.password} />
        <TextField id="change-email-email" label="New email address" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} error={err.email} />
        {err.form && (
          <Alert variant="destructive" className="sm:col-span-2">
            <AlertDescription>{err.form}</AlertDescription>
          </Alert>
        )}
        {success && (
          <Alert className="sm:col-span-2">
            <AlertDescription>{success}</AlertDescription>
          </Alert>
        )}
        <div className="sm:col-span-2">
          <Button type="submit" disabled={pending}>
            {pending ? "Sending…" : "Update"}
          </Button>
        </div>
      </form>
    </Section>
  )
}

function DeleteAccountSection() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState(false)

  return (
    <Section title="Delete account" description="Delete your account from this service. All your links and domains will be deleted as well.">
      <form
        className="grid gap-4 sm:max-w-sm"
        onSubmit={(e) => {
          e.preventDefault()
          if (password) setConfirm(true)
        }}
      >
        <TextField id="delete-password" label="Password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        <div>
          <Button type="submit" variant="destructive" disabled={!password}>
            <Trash2 /> Delete account
          </Button>
        </div>
      </form>
      {confirm && (
        <ConfirmDialog
          open
          onOpenChange={setConfirm}
          title="Delete your account?"
          description="This cannot be undone. All your links, stats and domains will be removed."
          confirmLabel="Delete my account"
          onConfirm={async () => {
            await api.deleteAccount(password)
            qc.setQueryData(qk.me, null)
            qc.clear()
            toast.success("Your account has been deleted.")
            navigate("/")
          }}
        />
      )}
    </Section>
  )
}

export default function SettingsPage() {
  const { data: cfg } = useConfig()
  const { data: me, isPending } = useMe()

  if (isPending || !cfg) {
    return (
      <div className="grid gap-6">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-48 w-full" />
      </div>
    )
  }
  if (!me) return <Navigate to="/login" replace />

  return (
    <div className="mx-auto grid max-w-3xl gap-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Settings</h1>
        <p className="text-muted-foreground">
          Welcome, <b>{me.email}</b>.
        </p>
      </div>
      <DomainsSection cfg={cfg} user={me} />
      <ApiKeySection user={me} />
      <ChangePasswordSection />
      {cfg.mail_enabled && <ChangeEmailSection user={me} />}
      <DeleteAccountSection />
    </div>
  )
}
