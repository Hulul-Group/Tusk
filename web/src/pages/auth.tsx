import { useEffect, useState, type FormEvent, type ReactNode } from "react"
import { Link, Navigate, useNavigate, useParams } from "react-router"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { CheckCircle2, XCircle } from "lucide-react"

import { Button, buttonVariants } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Separator } from "@/components/ui/separator"
import { Skeleton } from "@/components/ui/skeleton"
import { TextField } from "@/components/controls"
import { ApiError, api, qk, useConfig, useMe } from "@/lib/api"
import { cn } from "@/lib/utils"

function AuthCard({ title, description, children, footer }: { title: string; description?: ReactNode; children: ReactNode; footer?: ReactNode }) {
  return (
    <Card className="mx-auto w-full max-w-sm">
      <CardHeader>
        <CardTitle className="text-2xl">
          <h1>{title}</h1>
        </CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="grid gap-4">{children}</CardContent>
      {footer && <CardFooter className="flex-col gap-2">{footer}</CardFooter>}
    </Card>
  )
}

function errMsg(e: unknown) {
  return e instanceof ApiError ? e.message : "An error occurred."
}

function fieldOf(msg: string): "email" | "password" | "form" {
  const m = msg.toLowerCase()
  if (m.includes("email") || m.includes("user already")) return "email"
  if (m.includes("password")) return "password"
  return "form"
}

// ---------------------------------------------------------------------------
// /login
// ---------------------------------------------------------------------------

export function LoginPage() {
  const { data: cfg } = useConfig()
  const { data: me } = useMe()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [errors, setErrors] = useState<Partial<Record<"email" | "password" | "form", string>>>({})
  const [pending, setPending] = useState<"login" | "signup" | null>(null)
  const [signedUp, setSignedUp] = useState(false)

  if (cfg?.needs_admin_setup) return <Navigate to="/create-admin" replace />
  if (me) return <Navigate to="/" replace />

  const loginDisabled = !!cfg?.disallow_login_form && !cfg?.oidc_enabled
  const canSignup = !cfg?.disallow_registration && !!cfg?.mail_enabled

  const run = async (kind: "login" | "signup", e?: FormEvent) => {
    e?.preventDefault()
    setErrors({})
    setPending(kind)
    try {
      if (kind === "login") {
        await api.login(email, password)
        await qc.invalidateQueries({ queryKey: qk.me })
        navigate("/")
      } else {
        await api.signup(email, password)
        setSignedUp(true)
      }
    } catch (err) {
      const m = errMsg(err)
      setErrors({ [fieldOf(m)]: m })
    } finally {
      setPending(null)
    }
  }

  if (loginDisabled) {
    return (
      <AuthCard title="Log in">
        <Alert>
          <AlertTitle>Login is disabled</AlertTitle>
          <AlertDescription>Logging in is disabled on this instance.</AlertDescription>
        </Alert>
      </AuthCard>
    )
  }

  if (signedUp) {
    return (
      <AuthCard title="Verify your email">
        <Alert>
          <CheckCircle2 />
          <AlertTitle>A verification email has been sent.</AlertTitle>
          <AlertDescription>
            Follow the link sent to <b>{email}</b> to activate your account.
          </AlertDescription>
        </Alert>
      </AuthCard>
    )
  }

  return (
    <AuthCard
      title={canSignup ? "Log in or sign up" : "Log in"}
      description={canSignup ? "Use your email and password. New here? Sign up with the same form." : undefined}
      footer={
        !cfg?.disallow_login_form && cfg?.mail_enabled ? (
          <Link to="/reset-password" className="text-sm text-muted-foreground underline-offset-4 hover:underline">
            Forgot your password?
          </Link>
        ) : undefined
      }
    >
      {!cfg?.disallow_login_form && (
        <form className="grid gap-4" onSubmit={(e) => void run("login", e)} noValidate>
          <TextField
            id="email"
            label="Email address"
            type="email"
            autoComplete="email"
            placeholder="Email address..."
            autoFocus
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            error={errors.email}
          />
          <TextField
            id="password"
            label="Password"
            type="password"
            autoComplete="current-password"
            placeholder="Password..."
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            error={errors.password}
          />
          {errors.form && (
            <Alert variant="destructive">
              <AlertDescription>{errors.form}</AlertDescription>
            </Alert>
          )}
          <div className={cn("grid gap-2", canSignup && "grid-cols-2")}>
            <Button type="submit" disabled={!!pending}>
              {pending === "login" ? "Logging in…" : "Log in"}
            </Button>
            {canSignup && (
              <Button type="button" variant="secondary" disabled={!!pending} onClick={() => void run("signup")}>
                {pending === "signup" ? "Signing up…" : "Sign up"}
              </Button>
            )}
          </div>
        </form>
      )}
      {cfg?.oidc_enabled && (
        <>
          {!cfg.disallow_login_form && <Separator />}
          {/* Full navigation: the OIDC dance is server-side (GET /login/oidc). */}
          <a href="/login/oidc" className={cn(buttonVariants({ variant: "outline" }), "w-full")}>
            {cfg.oidc_button_text}
          </a>
        </>
      )}
    </AuthCard>
  )
}

// ---------------------------------------------------------------------------
// /create-admin (first run)
// ---------------------------------------------------------------------------

export function CreateAdminPage() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [errors, setErrors] = useState<Partial<Record<"email" | "password" | "form", string>>>({})
  const [pending, setPending] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setErrors({})
    try {
      await api.createAdmin(email, password)
      await qc.invalidateQueries({ queryKey: qk.config })
      await qc.invalidateQueries({ queryKey: qk.me })
      navigate("/")
    } catch (err) {
      const m = errMsg(err)
      setErrors({ [fieldOf(m)]: m })
    } finally {
      setPending(false)
    }
  }

  return (
    <AuthCard title="Create admin account" description="Welcome! Create the first account; it becomes the administrator.">
      <form className="grid gap-4" onSubmit={submit} noValidate>
        <TextField id="email" label="Email address" type="email" autoComplete="email" autoFocus value={email} onChange={(e) => setEmail(e.target.value)} error={errors.email} />
        <TextField
          id="password"
          label="Password"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          error={errors.password}
          description="8 to 64 characters."
        />
        {errors.form && (
          <Alert variant="destructive">
            <AlertDescription>{errors.form}</AlertDescription>
          </Alert>
        )}
        <Button type="submit" disabled={pending}>
          {pending ? "Creating…" : "Create admin account"}
        </Button>
      </form>
    </AuthCard>
  )
}

// ---------------------------------------------------------------------------
// /reset-password  and  /reset-password/:token
// ---------------------------------------------------------------------------

export function ResetPasswordPage() {
  const { data: cfg } = useConfig()
  const [email, setEmail] = useState("")
  const [message, setMessage] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  if (cfg && !cfg.mail_enabled) return <Navigate to="/404" replace />

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    try {
      await api.resetPassword(email)
      setMessage("If the email address exists, a reset password email will be sent to it.")
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <AuthCard title="Reset password" description="If you forgot your password, enter your email and we'll send you a reset link.">
      {message ? (
        <Alert>
          <CheckCircle2 />
          <AlertDescription>{message}</AlertDescription>
        </Alert>
      ) : (
        <form className="grid gap-4" onSubmit={submit} noValidate>
          <TextField id="email" label="Email address" type="email" autoComplete="email" autoFocus value={email} onChange={(e) => setEmail(e.target.value)} error={error} />
          <Button type="submit" disabled={pending}>
            {pending ? "Sending…" : "Reset password"}
          </Button>
        </form>
      )}
    </AuthCard>
  )
}

export function NewPasswordPage() {
  const { token = "" } = useParams()
  const [pw, setPw] = useState("")
  const [repeat, setRepeat] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)
  const [pending, setPending] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (pw !== repeat) {
      setError("Passwords don't match.")
      return
    }
    setPending(true)
    setError(null)
    try {
      await api.newPassword(token, pw, repeat)
      setDone(true)
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <AuthCard title="Set a new password">
      {done ? (
        <>
          <Alert>
            <CheckCircle2 />
            <AlertDescription>Your password has been updated. You can now log in.</AlertDescription>
          </Alert>
          <Link to="/login" className={buttonVariants()}>
            Log in
          </Link>
        </>
      ) : (
        <form className="grid gap-4" onSubmit={submit} noValidate>
          <TextField id="new_password" label="New password" type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} />
          <TextField id="repeat_password" label="Repeat password" type="password" autoComplete="new-password" value={repeat} onChange={(e) => setRepeat(e.target.value)} />
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <Button type="submit" disabled={pending}>
            {pending ? "Saving…" : "Set password"}
          </Button>
        </form>
      )}
    </AuthCard>
  )
}

// ---------------------------------------------------------------------------
// /verify/:token  and  /verify-email/:token
// ---------------------------------------------------------------------------

function VerifyResult({ kind }: { kind: "signup" | "change-email" }) {
  const { token = "" } = useParams()
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ["verify", kind, token],
    queryFn: () => (kind === "signup" ? api.verifyEmail(token) : api.verifyChangeEmail(token)),
    retry: false,
    staleTime: Infinity,
  })
  useEffect(() => {
    if (q.isSuccess) void qc.invalidateQueries({ queryKey: qk.me })
  }, [q.isSuccess, qc])

  const title = kind === "signup" ? "Verify account" : "Verify new email"
  if (q.isPending) {
    return (
      <AuthCard title={title}>
        <Skeleton className="h-16 w-full" />
      </AuthCard>
    )
  }
  if (q.isError) {
    return (
      <AuthCard title={title}>
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Invalid or expired link</AlertTitle>
          <AlertDescription>The verification link is invalid or has expired.</AlertDescription>
        </Alert>
      </AuthCard>
    )
  }
  return (
    <AuthCard title={title}>
      <Alert>
        <CheckCircle2 />
        <AlertTitle>{kind === "signup" ? "Your account has been verified." : "Your email address has been changed."}</AlertTitle>
        <AlertDescription>You are now logged in.</AlertDescription>
      </Alert>
      <Link to="/" className={buttonVariants()}>
        Go to homepage
      </Link>
    </AuthCard>
  )
}

export function VerifyPage() {
  return <VerifyResult kind="signup" />
}

export function VerifyChangeEmailPage() {
  return <VerifyResult kind="change-email" />
}
