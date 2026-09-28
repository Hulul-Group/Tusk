import { createContext, useContext, useEffect, useState, type ReactNode } from "react"

// shadcn/ui's documented Vite dark-mode approach: a `dark` class on <html>,
// persisted in localStorage, with "system" following prefers-color-scheme.
export type Theme = "dark" | "light" | "system"

interface ThemeState {
  theme: Theme
  resolved: "dark" | "light"
  setTheme: (t: Theme) => void
}

const STORAGE_KEY = "tusk-ui-theme"
const ThemeContext = createContext<ThemeState | null>(null)

function systemTheme(): "dark" | "light" {
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(
    () => (localStorage.getItem(STORAGE_KEY) as Theme | null) ?? "system",
  )
  const [sys, setSys] = useState<"dark" | "light">(systemTheme)

  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)")
    const onChange = () => setSys(mq.matches ? "dark" : "light")
    mq.addEventListener("change", onChange)
    return () => mq.removeEventListener("change", onChange)
  }, [])

  const resolved = theme === "system" ? sys : theme

  useEffect(() => {
    const root = document.documentElement
    root.classList.remove("light", "dark")
    root.classList.add(resolved)
    root.style.colorScheme = resolved
  }, [resolved])

  const setTheme = (t: Theme) => {
    localStorage.setItem(STORAGE_KEY, t)
    setThemeState(t)
  }

  return <ThemeContext.Provider value={{ theme, resolved, setTheme }}>{children}</ThemeContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useTheme(): ThemeState {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error("useTheme must be used within ThemeProvider")
  return ctx
}
