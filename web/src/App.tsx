import { lazy, Suspense, useEffect } from "react"
import { BrowserRouter, Route, Routes, useLocation } from "react-router"

import { Layout } from "@/components/layout"
import { Skeleton } from "@/components/ui/skeleton"
import { useConfig } from "@/lib/api"
import HomePage from "@/pages/home"
import {
  CreateAdminPage,
  LoginPage,
  NewPasswordPage,
  ResetPasswordPage,
  VerifyChangeEmailPage,
  VerifyPage,
} from "@/pages/auth"
import { BannedPage, NotFoundPage, ReportPage, TermsPage } from "@/pages/static"

// Code-split the heavy / rarely used routes: stats pulls in Recharts, admin is admins-only.
const StatsPage = lazy(() => import("@/pages/stats"))
const AdminPage = lazy(() => import("@/pages/admin"))
const SettingsPage = lazy(() => import("@/pages/settings"))

const TITLES: Record<string, string> = {
  "/login": "Log in or sign up",
  "/create-admin": "Create admin account",
  "/settings": "Settings",
  "/admin": "Admin",
  "/stats": "Stats",
  "/report": "Report abuse",
  "/terms": "Terms of Service",
  "/banned": "Banned link",
  "/404": "404 - Not found",
  "/reset-password": "Reset password",
}

function DocumentTitle() {
  const { pathname } = useLocation()
  const { data: cfg } = useConfig()
  useEffect(() => {
    const site = cfg?.site_name ?? "Kutt"
    const key = Object.keys(TITLES).find((k) => pathname === k || pathname.startsWith(k + "/"))
    document.title = key ? `${TITLES[key]} | ${site}` : `${site} | Modern Open Source URL shortener.`
  }, [pathname, cfg?.site_name])
  return null
}

function PageFallback() {
  return (
    <div className="grid gap-4" aria-busy="true" aria-label="Loading">
      <Skeleton className="h-10 w-1/2" />
      <Skeleton className="h-64 w-full" />
    </div>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <DocumentTitle />
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route element={<Layout />}>
            <Route index element={<HomePage />} />
            <Route path="login" element={<LoginPage />} />
            <Route path="create-admin" element={<CreateAdminPage />} />
            <Route path="settings" element={<SettingsPage />} />
            <Route path="admin" element={<AdminPage />} />
            <Route path="stats" element={<StatsPage />} />
            <Route path="report" element={<ReportPage />} />
            <Route path="terms" element={<TermsPage />} />
            <Route path="banned" element={<BannedPage />} />
            <Route path="reset-password" element={<ResetPasswordPage />} />
            <Route path="reset-password/:token" element={<NewPasswordPage />} />
            <Route path="verify/:token" element={<VerifyPage />} />
            <Route path="verify-email/:token" element={<VerifyChangeEmailPage />} />
            <Route path="404" element={<NotFoundPage />} />
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Routes>
      </Suspense>
    </BrowserRouter>
  )
}
