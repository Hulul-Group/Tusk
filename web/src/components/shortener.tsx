import { useState, type FormEvent } from "react"
import { Copy, QrCode, Settings2, Sparkles } from "lucide-react"
import { useQueryClient } from "@tanstack/react-query"
import { QRCodeSVG } from "qrcode.react"

import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Field, FieldError, FieldLabel } from "@/components/ui/field"
import { QRDialog } from "@/components/dialogs"
import { ApiError, api, invalidateLinks, type CreateLinkInput, type Link } from "@/lib/api"
import { copyText, stripProtocol } from "@/lib/format"
import type { CurrentUser, PublicConfig } from "@/lib/api"

type Errors = Partial<Record<keyof CreateLinkInput | "form", string>>

/** Map Kutt's single error string back to a field (it only returns the first message). */
function fieldFor(msg: string): keyof Errors {
  const m = msg.toLowerCase()
  if (m.includes("custom url") || m.includes("custom address")) return "customurl"
  if (m.includes("password")) return "password"
  if (m.includes("expire")) return "expire_in"
  if (m.includes("description")) return "description"
  if (m.includes("domain")) return "domain"
  if (m.includes("url") || m.includes("target")) return "target"
  return "form"
}

export function Shortener({ cfg, user }: { cfg: PublicConfig; user: CurrentUser | null | undefined }) {
  const qc = useQueryClient()
  const [target, setTarget] = useState("")
  const [advanced, setAdvanced] = useState(false)
  const [domain, setDomain] = useState(cfg.default_domain)
  const [customurl, setCustomurl] = useState("")
  const [password, setPassword] = useState("")
  const [expireIn, setExpireIn] = useState("")
  const [description, setDescription] = useState("")
  const [errors, setErrors] = useState<Errors>({})
  const [pending, setPending] = useState(false)
  const [result, setResult] = useState<Link | null>(null)
  const [qrOpen, setQrOpen] = useState(false)

  const anonymousBlocked = !user && cfg.disallow_anonymous_links
  const domains = [cfg.default_domain, ...(user?.domains ?? []).filter((d) => !d.banned).map((d) => d.address)]

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!target.trim()) {
      setErrors({ target: "Target is missing." })
      return
    }
    setPending(true)
    setErrors({})
    const input: CreateLinkInput = { target: target.trim() }
    if (advanced) {
      if (customurl) input.customurl = customurl.trim()
      if (password) input.password = password
      if (expireIn) input.expire_in = expireIn.trim()
      if (description) input.description = description.trim()
      if (domain && domain !== cfg.default_domain) input.domain = domain
    }
    try {
      const link = await api.createLink(input)
      setResult(link)
      setTarget("")
      setCustomurl("")
      setPassword("")
      setExpireIn("")
      setDescription("")
      void invalidateLinks(qc)
      void copyText(link.link, "Short link created and copied")
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : "An error occurred."
      if (err instanceof ApiError && err.status === 401) {
        setErrors({ form: "You need to log in to create links." })
      } else {
        setErrors({ [fieldFor(msg)]: msg })
      }
    } finally {
      setPending(false)
    }
  }

  return (
    <section aria-labelledby="shortener-title" className="mx-auto w-full max-w-3xl">
      <div className="mb-8 text-center">
        {result ? (
          <Card className="mx-auto max-w-xl text-left">
            <CardHeader>
              <CardDescription>Your short link</CardDescription>
              <CardTitle id="shortener-title" className="text-2xl break-all">
                <a href={result.link} target="_blank" rel="noopener noreferrer" className="underline-offset-4 hover:underline">
                  {stripProtocol(result.link)}
                </a>
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-wrap items-center gap-4">
              <button
                type="button"
                className="rounded-md bg-white p-2 ring-1 ring-border"
                onClick={() => setQrOpen(true)}
                aria-label="Show larger QR code"
              >
                <QRCodeSVG value={result.link} size={88} />
              </button>
              <div className="flex flex-1 flex-wrap gap-2">
                <Button onClick={() => void copyText(result.link)}>
                  <Copy /> Copy
                </Button>
                <Button variant="outline" onClick={() => setQrOpen(true)}>
                  <QrCode /> QR code
                </Button>
                <Button variant="ghost" onClick={() => setResult(null)}>
                  <Sparkles /> Shorten another
                </Button>
              </div>
            </CardContent>
            <QRDialog url={result.link} open={qrOpen} onOpenChange={setQrOpen} />
          </Card>
        ) : (
          <h1 id="shortener-title" className="text-4xl font-bold tracking-tight text-balance sm:text-5xl">
            Cut your links <span className="text-muted-foreground">shorter</span>.
          </h1>
        )}
      </div>

      {anonymousBlocked ? (
        <Alert>
          <AlertDescription>
            Anonymous link creation is disabled. <a className="underline" href="/login">Log in</a> to shorten links.
          </AlertDescription>
        </Alert>
      ) : (
        <form onSubmit={submit} autoComplete="off" noValidate className="grid gap-4" aria-describedby={errors.form ? "form-error" : undefined}>
          <Field data-invalid={!!errors.target || undefined}>
            <Label htmlFor="target" className="sr-only">
              Long URL
            </Label>
            <div className="flex flex-col gap-2 sm:flex-row">
              <Input
                id="target"
                name="target"
                type="text"
                inputMode="url"
                placeholder="Paste your long URL"
                autoFocus
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                aria-invalid={!!errors.target || undefined}
                className="h-11 text-base"
              />
              <Button type="submit" size="lg" className="h-11 px-6" disabled={pending}>
                {pending ? "Shortening…" : "Shorten"}
              </Button>
            </div>
            {errors.target && <FieldError>{errors.target}</FieldError>}
          </Field>
          {errors.form && (
            <Alert variant="destructive" id="form-error">
              <AlertDescription>{errors.form}</AlertDescription>
            </Alert>
          )}

          <div className="flex items-center gap-2">
            <Switch id="show-advanced" checked={advanced} onCheckedChange={setAdvanced} />
            <Label htmlFor="show-advanced" className="flex items-center gap-1.5">
              <Settings2 className="size-4" /> Show advanced options
            </Label>
          </div>

          {advanced && (
            <Card>
              <CardContent className="grid gap-4 sm:grid-cols-2">
                <Field data-invalid={!!errors.domain || undefined}>
                  <FieldLabel htmlFor="domain">Domain</FieldLabel>
                  <Select items={domains.map((d) => ({ value: d, label: d }))} value={domain} onValueChange={(v) => setDomain((v as string) ?? cfg.default_domain)} disabled={!user}>
                    <SelectTrigger id="domain" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {domains.map((d) => (
                        <SelectItem key={d} value={d}>
                          {d}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {errors.domain && <FieldError>{errors.domain}</FieldError>}
                </Field>
                <Field data-invalid={!!errors.customurl || undefined}>
                  <FieldLabel htmlFor="customurl">Custom address</FieldLabel>
                  <div className="flex items-center gap-1">
                    <span className="max-w-[50%] shrink-0 truncate text-sm text-muted-foreground" title={`${domain}/`}>{domain}/</span>
                    <Input
                      id="customurl"
                      placeholder="Custom address..."
                      value={customurl}
                      disabled={!user}
                      onChange={(e) => setCustomurl(e.target.value)}
                      aria-invalid={!!errors.customurl || undefined}
                    />
                  </div>
                  {errors.customurl && <FieldError>{errors.customurl}</FieldError>}
                </Field>
                <Field data-invalid={!!errors.password || undefined}>
                  <FieldLabel htmlFor="password">Password</FieldLabel>
                  <Input
                    id="password"
                    type="password"
                    placeholder="Password..."
                    autoComplete="new-password"
                    value={password}
                    disabled={!user}
                    onChange={(e) => setPassword(e.target.value)}
                    aria-invalid={!!errors.password || undefined}
                  />
                  {errors.password && <FieldError>{errors.password}</FieldError>}
                </Field>
                <Field data-invalid={!!errors.expire_in || undefined}>
                  <FieldLabel htmlFor="expire_in">Expire in</FieldLabel>
                  <Input
                    id="expire_in"
                    placeholder="2 minutes/hours/days"
                    value={expireIn}
                    onChange={(e) => setExpireIn(e.target.value)}
                    aria-invalid={!!errors.expire_in || undefined}
                  />
                  {errors.expire_in && <FieldError>{errors.expire_in}</FieldError>}
                </Field>
                <Field data-invalid={!!errors.description || undefined} className="sm:col-span-2">
                  <FieldLabel htmlFor="description">Description</FieldLabel>
                  <Input
                    id="description"
                    placeholder="Description..."
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    aria-invalid={!!errors.description || undefined}
                  />
                  {errors.description && <FieldError>{errors.description}</FieldError>}
                </Field>
                {!user && (
                  <p className="text-sm text-muted-foreground sm:col-span-2">
                    Domain, custom address and password are available to logged-in users.
                  </p>
                )}
              </CardContent>
            </Card>
          )}
        </form>
      )}
    </section>
  )
}
