import { useEffect, useState } from "react"
import { toast } from "sonner"

/** Debounce a value (Kutt's link search used a 500ms delay). */
export function useDebounced<T>(value: T, ms = 500): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

export async function copyText(text: string, label = "Copied to clipboard") {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(label)
  } catch {
    toast.error("Could not copy. Select the text and copy it manually.")
  }
}

const rtf = typeof Intl !== "undefined" ? new Intl.RelativeTimeFormat("en", { numeric: "auto" }) : null

/** "3 hours ago" / "in 5 days", like Kutt's getTimeAgo / ms(long). */
export function relativeTime(iso: string | null | undefined): string {
  if (!iso) return ""
  const d = parseDate(iso)
  if (!d) return ""
  const diff = (d.getTime() - Date.now()) / 1000
  const abs = Math.abs(diff)
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 31536000],
    ["month", 2592000],
    ["week", 604800],
    ["day", 86400],
    ["hour", 3600],
    ["minute", 60],
    ["second", 1],
  ]
  for (const [unit, secs] of units) {
    if (abs >= secs || unit === "second") {
      const n = Math.round(diff / secs)
      return rtf ? rtf.format(n, unit) : `${n} ${unit}`
    }
  }
  return ""
}

/** Kutt stores UTC datetimes; SQLite rows may come without a zone suffix. */
export function parseDate(s: string): Date | null {
  let v = s
  if (/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/.test(v)) v = v.replace(" ", "T")
  if (/T\d{2}:\d{2}(:\d{2}(\.\d+)?)?$/.test(v)) v += "Z"
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? null : d
}

export function formatNumber(n: number | string | null | undefined): string {
  if (n === null || n === undefined) return "0"
  if (typeof n === "string") return n
  return n.toLocaleString("en-US")
}

/** Short URL string from a link row (JSON API gives a string; admin HTML shape an object). */
export function shortUrl(link: string | { url: string } | undefined | null): string {
  if (!link) return ""
  return typeof link === "string" ? link : link.url
}

export function stripProtocol(url: string): string {
  return url.replace(/^https?:\/\//, "")
}

/** Kutt obfuscates the report/contact email as user[at]domain.com. */
export function obfuscateEmail(email: string): string {
  return email.replace("@", "[at]")
}
/** Same format as the `ms` package's long output (what Kutt pre-fills): "5 days", "3 hours". */
export function msLong(iso: string | null | undefined): string {
  if (!iso) return ""
  const d = parseDate(iso)
  if (!d) return ""
  const ms = d.getTime() - Date.now()
  if (ms <= 0) return ""
  const units: [string, number][] = [
    ["day", 86_400_000],
    ["hour", 3_600_000],
    ["minute", 60_000],
    ["second", 1000],
  ]
  for (const [name, size] of units) {
    if (ms >= size * 1.5 || name === "second") {
      const n = Math.round(ms / size)
      return `${n} ${name}${n === 1 ? "" : "s"}`
    }
    if (ms >= size) return `1 ${name}`
  }
  return ""
}
