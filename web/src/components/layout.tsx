import { Link, NavLink, Outlet } from "react-router"
import { LogOut, Menu, Moon, Settings, Shield, Sun, Monitor } from "lucide-react"

import { Button, buttonVariants } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet"
import { Separator } from "@/components/ui/separator"
import { useTheme } from "@/components/theme-provider"
import { isAdmin, useConfig, useMe } from "@/lib/api"
import { useLogout } from "@/lib/session"
import { cn } from "@/lib/utils"
import { useState } from "react"

function ThemeToggle() {
  const { setTheme, resolved } = useTheme()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="ghost" size="icon" aria-label="Toggle theme" />}
      >
        {resolved === "dark" ? <Moon /> : <Sun />}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onClick={() => setTheme("light")}>
          <Sun /> Light
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => setTheme("dark")}>
          <Moon /> Dark
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => setTheme("system")}>
          <Monitor /> System
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function NavLinks({ onNavigate, vertical }: { onNavigate?: () => void; vertical?: boolean }) {
  const { data: me } = useMe()
  const { data: cfg } = useConfig()
  const logout = useLogout()
  const loginDisabled = !!cfg?.disallow_login_form && !cfg?.oidc_enabled
  const itemCls = vertical ? "w-full justify-start" : ""

  if (!me) {
    return loginDisabled ? null : (
      <Link to="/login" onClick={onNavigate} className={cn(buttonVariants({ size: "default" }), itemCls)}>
        Log in / Sign up
      </Link>
    )
  }
  return (
    <>
      <NavLink
        to="/settings"
        onClick={onNavigate}
        className={cn(buttonVariants({ variant: "ghost" }), itemCls)}
      >
        <Settings /> Settings
      </NavLink>
      {isAdmin(me) && (
        <NavLink to="/admin" onClick={onNavigate} className={cn(buttonVariants({ variant: "ghost" }), itemCls)}>
          <Shield /> Admin
        </NavLink>
      )}
      <Button
        variant="ghost"
        className={itemCls}
        onClick={() => {
          onNavigate?.()
          void logout()
        }}
      >
        <LogOut /> Log out
      </Button>
    </>
  )
}

function Header() {
  const { data: cfg } = useConfig()
  const [open, setOpen] = useState(false)
  return (
    <header className="sticky top-0 z-40 border-b bg-background/80 backdrop-blur supports-backdrop-filter:bg-background/60">
      <div className="mx-auto flex h-14 w-full max-w-6xl items-center gap-2 px-4">
        <Link to="/" className="flex items-center gap-2 font-semibold" aria-label={`${cfg?.site_name} homepage`}>
          <img src="/images/logo.png" alt="" width={18} height={24} className="dark:invert" />
          <span>{cfg?.site_name}</span>
        </Link>
        <nav aria-label="Secondary" className="ml-4 hidden items-center gap-1 text-sm text-muted-foreground sm:flex">
          <a
            className="rounded-md px-2 py-1 hover:text-foreground"
            href="https://github.com/Hulul-Group/Tusk"
            target="_blank"
            rel="noopener noreferrer"
          >
            GitHub
          </a>
          {cfg?.report_email && (
            <Link className="rounded-md px-2 py-1 hover:text-foreground" to="/report">
              Report
            </Link>
          )}
        </nav>
        <div className="ml-auto flex items-center gap-1">
          <nav aria-label="Main" className="hidden items-center gap-1 sm:flex">
            <NavLinks />
          </nav>
          <ThemeToggle />
          <Sheet open={open} onOpenChange={setOpen}>
            <SheetTrigger render={<Button variant="ghost" size="icon" className="sm:hidden" aria-label="Open menu" />}>
              <Menu />
            </SheetTrigger>
            <SheetContent side="right" className="w-72">
              <SheetHeader>
                <SheetTitle>{cfg?.site_name}</SheetTitle>
              </SheetHeader>
              <nav aria-label="Mobile" className="flex flex-col gap-1 px-4">
                <NavLinks vertical onNavigate={() => setOpen(false)} />
                <Separator className="my-2" />
                {cfg?.report_email && (
                  <Link
                    to="/report"
                    onClick={() => setOpen(false)}
                    className={cn(buttonVariants({ variant: "ghost" }), "w-full justify-start")}
                  >
                    Report abuse
                  </Link>
                )}
                <Link
                  to="/terms"
                  onClick={() => setOpen(false)}
                  className={cn(buttonVariants({ variant: "ghost" }), "w-full justify-start")}
                >
                  Terms of Service
                </Link>
              </nav>
            </SheetContent>
          </Sheet>
        </div>
      </div>
    </header>
  )
}

function Footer() {
  const { data: cfg } = useConfig()
  const [showContact, setShowContact] = useState(false)
  return (
    <footer className="border-t">
      <div className="mx-auto flex max-w-6xl flex-wrap items-center justify-center gap-x-3 gap-y-1 px-4 py-6 text-center text-sm text-muted-foreground">
        <span>
          Powered by{" "}
          <a className="underline-offset-4 hover:underline" href="https://github.com/Hulul-Group/Tusk" target="_blank" rel="noopener noreferrer">
            Tusk
          </a>
        </span>
        <span aria-hidden>|</span>
        <Link className="underline-offset-4 hover:underline" to="/terms">
          Terms of Service
        </Link>
        {cfg?.report_email && (
          <>
            <span aria-hidden>|</span>
            <Link className="underline-offset-4 hover:underline" to="/report">
              Report Abuse
            </Link>
          </>
        )}
        {cfg?.contact_email && (
          <>
            <span aria-hidden>|</span>
            {showContact ? (
              <a className="underline-offset-4 hover:underline" href={`mailto:${cfg.contact_email}`}>
                {cfg.contact_email}
              </a>
            ) : (
              <button type="button" className="underline-offset-4 hover:underline" onClick={() => setShowContact(true)}>
                Contact us
              </button>
            )}
          </>
        )}
      </div>
    </footer>
  )
}

export function Layout() {
  return (
    <div className="flex min-h-svh flex-col">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:rounded-md focus:bg-background focus:px-3 focus:py-2 focus:ring-2 focus:ring-ring"
      >
        Skip to content
      </a>
      <Header />
      <main id="main" className="mx-auto w-full max-w-6xl flex-1 px-4 py-8 sm:py-12">
        <Outlet />
      </main>
      <Footer />
    </div>
  )
}
