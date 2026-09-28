import type { ReactNode } from "react"
import { ChevronLeft, ChevronRight } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Field, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"

const PAGE_SIZES = [10, 25, 50, 100]

/** Kutt table nav: limit 10/25/50/100 + prev/next over skip. */
export function TablePager({
  total,
  limit,
  skip,
  onChange,
  loading,
}: {
  total: number
  limit: number
  skip: number
  onChange: (next: { limit: number; skip: number }) => void
  loading?: boolean
}) {
  const from = total === 0 ? 0 : skip + 1
  const to = Math.min(skip + limit, total)
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
      <div className="flex items-center gap-2">
        <span className="text-muted-foreground" id="page-size-label">
          Rows
        </span>
        <Select
          value={String(limit)}
          onValueChange={(v) => onChange({ limit: Number(v), skip: 0 })}
        >
          <SelectTrigger size="sm" aria-labelledby="page-size-label" className="w-18">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PAGE_SIZES.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <nav aria-label="Pagination" className="flex items-center gap-2">
        <span className="text-muted-foreground tabular-nums" aria-live="polite">
          {from.toLocaleString("en-US")}–{to.toLocaleString("en-US")} of {total.toLocaleString("en-US")}
        </span>
        <Button
          variant="outline"
          size="icon-sm"
          aria-label="Previous page"
          disabled={loading || skip === 0}
          onClick={() => onChange({ limit, skip: Math.max(0, skip - limit) })}
        >
          <ChevronLeft />
        </Button>
        <Button
          variant="outline"
          size="icon-sm"
          aria-label="Next page"
          disabled={loading || skip + limit >= total}
          onClick={() => onChange({ limit, skip: skip + limit })}
        >
          <ChevronRight />
        </Button>
      </nav>
    </div>
  )
}

/** Labelled input with inline error, built from shadcn Field primitives. */
export function TextField({
  id,
  label,
  error,
  description,
  ...input
}: React.ComponentProps<typeof Input> & { id: string; label: ReactNode; error?: string | null; description?: ReactNode }) {
  return (
    <Field data-invalid={!!error || undefined}>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input id={id} name={id} aria-invalid={!!error || undefined} {...input} />
      {description && <p className="text-sm text-muted-foreground">{description}</p>}
      {error && <FieldError>{error}</FieldError>}
    </Field>
  )
}

/** A tri-state (any/true/false) filter select used by the admin tables. */
export function TriSelect({
  label,
  value,
  onChange,
  any,
  yes,
  no,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  any: string
  yes: string
  no: string
}) {
  const items = [
    { value: "", label: any },
    { value: "true", label: yes },
    { value: "false", label: no },
  ]
  return (
    <Select items={items} value={value} onValueChange={(v) => onChange((v as string) ?? "")}>
      <SelectTrigger size="sm" aria-label={label} className="min-w-32">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {items.map((i) => (
          <SelectItem key={i.value || "any"} value={i.value}>
            {i.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
