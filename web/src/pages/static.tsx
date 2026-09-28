import { useState, type FormEvent } from "react"
import { Link, Navigate } from "react-router"
import { Ban, FileQuestion, Mail } from "lucide-react"

import { Button, buttonVariants } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { TextField } from "@/components/controls"
import { ApiError, api, useConfig } from "@/lib/api"
import { obfuscateEmail } from "@/lib/format"

export function ReportPage() {
  const { data: cfg } = useConfig()
  const [link, setLink] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [showEmail, setShowEmail] = useState(false)

  if (cfg && !cfg.report_email) return <Navigate to="/404" replace />

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    setSuccess(null)
    try {
      const r = await api.report(link)
      setSuccess(r?.message ?? "Thanks for the report, we'll take actions shortly.")
      setLink("")
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "An error occurred.")
    } finally {
      setPending(false)
    }
  }

  return (
    <Card className="mx-auto max-w-xl">
      <CardHeader>
        <CardTitle>
          <h1 className="text-2xl">Report abuse</h1>
        </CardTitle>
        <CardDescription>
          Report abuses, malware and phishing links to the email address below or use the form. We'll review as soon as we can.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-6">
        <div>
          {showEmail ? (
            <p className="flex items-center gap-2 font-medium">
              <Mail className="size-4" /> {obfuscateEmail(cfg?.report_email ?? "")}
            </p>
          ) : (
            <Button variant="outline" onClick={() => setShowEmail(true)}>
              <Mail /> Show report email
            </Button>
          )}
        </div>
        {cfg?.mail_enabled && (
          <form className="grid gap-4" onSubmit={submit} noValidate>
            <TextField
              id="report-link"
              label="URL containing malware/scam"
              placeholder={`${cfg.default_domain}/example`}
              value={link}
              onChange={(e) => setLink(e.target.value)}
              error={error}
            />
            {success && (
              <Alert>
                <AlertDescription>{success}</AlertDescription>
              </Alert>
            )}
            <div>
              <Button type="submit" disabled={pending}>
                {pending ? "Sending…" : "Send report"}
              </Button>
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  )
}

export function TermsPage() {
  const { data: cfg } = useConfig()
  const site = cfg?.site_name ?? "Kutt"
  const domain = cfg?.default_domain ?? ""
  return (
    <article className="mx-auto grid max-w-3xl gap-4 leading-7 text-muted-foreground">
      <h1 className="text-3xl font-bold tracking-tight text-foreground">{domain} Terms of Service</h1>
      <p>
        By accessing the website at <a className="underline" href={`https://${domain}`}>https://{domain}</a>, you are agreeing to be bound by
        these terms of service, all applicable laws and regulations, and agree that you are responsible for compliance with any applicable local
        laws. If you do not agree with any of these terms, you are prohibited from using or accessing this site. The materials contained in
        this website are protected by applicable copyright and trademark law.
      </p>
      <p>
        In no event shall {site} or its suppliers be liable for any damages (including, without limitation, damages for loss of data or
        profit, or due to business interruption) arising out of the use or inability to use the materials on {domain} website, even if {site}{" "}
        or a {site} authorized representative has been notified orally or in writing of the possibility of such damage. Because some
        jurisdictions do not allow limitations on implied warranties, or limitations of liability for consequential or incidental damages,
        these limitations may not apply to you.
      </p>
      <p>
        The materials appearing on {site} website could include technical, typographical, or photographic errors. {site} does not warrant that
        any of the materials on its website are accurate, complete or current. {site} may make changes to the materials contained on its website
        at any time without notice. However {site} does not make any commitment to update the materials.
      </p>
      <p>
        {site} has not reviewed all of the sites linked to its website and is not responsible for the contents of any such linked site. The
        inclusion of any link does not imply endorsement by {site} of the site. Use of any such linked website is at the user's own risk.
      </p>
      <p>
        {site} may revise these terms of service for its website at any time without notice. By using this website you are agreeing to be
        bound by the then current version of these terms of service.
      </p>
    </article>
  )
}

function Message({ icon, title, children }: { icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <div className="mx-auto flex max-w-md flex-col items-center gap-4 py-12 text-center">
      <div className="text-muted-foreground [&_svg]:size-12">{icon}</div>
      <h1 className="text-3xl font-bold tracking-tight">{title}</h1>
      <div className="text-muted-foreground">{children}</div>
      <Link to="/" className={buttonVariants()}>
        Go to homepage
      </Link>
    </div>
  )
}

export function NotFoundPage() {
  return (
    <Message icon={<FileQuestion />} title="404 | Link could not be found.">
      The page or short link you were looking for doesn't exist or has expired.
    </Message>
  )
}

export function BannedPage() {
  const { data: cfg } = useConfig()
  return (
    <Message icon={<Ban />} title="Link has been banned and removed because of malware or scam.">
      {cfg?.report_email ? (
        <>
          If you noticed a malware/scam link shortened by {cfg.site_name}, <Link className="underline" to="/report">send us a report</Link>.
        </>
      ) : null}
    </Message>
  )
}
