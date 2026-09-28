import { useState, type FormEvent } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { TextField } from "@/components/controls"
import { ApiError, api, invalidateLinks, type EditLinkInput } from "@/lib/api"
import { msLong, stripProtocol } from "@/lib/format"

const PASSWORD_MASK = "••••••••"

export interface EditableLink {
  id: string
  target: string
  address: string
  description: string | null
  expire_in: string | null
  password: boolean
  link: string
}

export function EditLinkDialog({
  link,
  admin,
  open,
  onOpenChange,
}: {
  link: EditableLink
  admin?: boolean
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const qc = useQueryClient()
  const [target, setTarget] = useState(link.target)
  const [address, setAddress] = useState(link.address)
  const [password, setPassword] = useState(link.password ? PASSWORD_MASK : "")
  const [description, setDescription] = useState(link.description ?? "")
  const [expireIn, setExpireIn] = useState(msLong(link.expire_in))
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    // Kutt semantics: an empty password removes it; the untouched mask keeps it.
    const input: EditLinkInput = { target, address, description, expire_in: expireIn, password }
    try {
      await (admin ? api.adminEditLink(link.id, input) : api.editLink(link.id, input))
      await invalidateLinks(qc)
      toast.success("Link updated")
      onOpenChange(false)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "An error occurred.")
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <DialogHeader>
            <DialogTitle>Edit link</DialogTitle>
            <DialogDescription className="break-all">{stripProtocol(link.link)}</DialogDescription>
          </DialogHeader>
          <TextField id="edit-target" label="Target" placeholder="Target..." value={target} onChange={(e) => setTarget(e.target.value)} />
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField id="edit-address" label="Address" placeholder="Custom URL..." value={address} onChange={(e) => setAddress(e.target.value)} />
            <TextField
              id="edit-password"
              label="Password"
              type="password"
              autoComplete="new-password"
              placeholder="Password..."
              value={password}
              onFocus={(e) => e.target.select()}
              onChange={(e) => setPassword(e.target.value)}
              description={link.password ? "Clear to remove the password." : undefined}
            />
            <TextField id="edit-expire" label="Expire in" placeholder="2 minutes/hours/days" value={expireIn} onChange={(e) => setExpireIn(e.target.value)} />
            <TextField id="edit-description" label="Description" placeholder="Description..." value={description} onChange={(e) => setDescription(e.target.value)} />
          </div>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? "Saving…" : "Save"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
