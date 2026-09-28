import { useState, type ReactNode } from "react"
import { QRCodeSVG } from "qrcode.react"
import { Copy, Download } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { copyText, stripProtocol } from "@/lib/format"

/** QR code for a short URL (Kutt: handleQRCode + qrcode.min.js). */
export function QRDialog({ url, open, onOpenChange }: { url: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const download = () => {
    const svg = document.getElementById("qr-svg")
    if (!svg) return
    const blob = new Blob([new XMLSerializer().serializeToString(svg)], { type: "image/svg+xml" })
    const a = document.createElement("a")
    a.href = URL.createObjectURL(blob)
    a.download = `${stripProtocol(url).replace(/[^\w.-]+/g, "_")}.svg`
    a.click()
    URL.revokeObjectURL(a.href)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>QR code</DialogTitle>
          <DialogDescription className="break-all">{stripProtocol(url)}</DialogDescription>
        </DialogHeader>
        <div className="flex justify-center rounded-lg bg-white p-4">
          <QRCodeSVG id="qr-svg" value={url} size={200} marginSize={2} title={`QR code for ${url}`} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => void copyText(url)}>
            <Copy /> Copy link
          </Button>
          <Button onClick={download}>
            <Download /> Download SVG
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export interface ConfirmOption {
  key: string
  label: string
  defaultChecked?: boolean
}

/**
 * Confirmation dialog used for delete/ban actions. `options` renders the
 * checkbox lists Kutt showed in its ban dialogs (e.g. "Ban link owner").
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "Confirm",
  destructive = true,
  options,
  onConfirm,
  children,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  title: string
  description?: ReactNode
  confirmLabel?: string
  destructive?: boolean
  options?: ConfirmOption[]
  onConfirm: (values: Record<string, boolean>) => Promise<unknown>
  children?: ReactNode
}) {
  const [values, setValues] = useState<Record<string, boolean>>(() =>
    Object.fromEntries((options ?? []).map((o) => [o.key, !!o.defaultChecked])),
  )
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const run = async () => {
    setPending(true)
    setError(null)
    try {
      await onConfirm(values)
      onOpenChange(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : "An error occurred.")
    } finally {
      setPending(false)
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          {description && <AlertDialogDescription className="break-all">{description}</AlertDialogDescription>}
        </AlertDialogHeader>
        {children}
        {options && options.length > 0 && (
          <div className="grid gap-3">
            {options.map((o) => (
              <div key={o.key} className="flex items-center gap-2">
                <Checkbox
                  id={`opt-${o.key}`}
                  checked={values[o.key]}
                  onCheckedChange={(c) => setValues((v) => ({ ...v, [o.key]: !!c }))}
                />
                <Label htmlFor={`opt-${o.key}`}>{o.label}</Label>
              </div>
            ))}
          </div>
        )}
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
          <AlertDialogAction variant={destructive ? "destructive" : "default"} disabled={pending} onClick={() => void run()}>
            {pending ? "Working…" : confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
