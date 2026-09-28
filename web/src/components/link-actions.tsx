import { useState } from "react"
import { Link as RouterLink } from "react-router"
import { useQueryClient } from "@tanstack/react-query"
import { BarChart3, Ban, Copy, MoreHorizontal, Pencil, QrCode, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button, buttonVariants } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog, QRDialog } from "@/components/dialogs"
import { EditLinkDialog, type EditableLink } from "@/components/edit-link"
import { api, invalidateLinks } from "@/lib/api"
import { copyText, stripProtocol } from "@/lib/format"
import { cn } from "@/lib/utils"

type Dialog = "qr" | "edit" | "delete" | "ban" | null

/**
 * Per-link actions (Kutt partials/links/actions.hbs): stats, QR, copy, edit, ban (admin), delete.
 * Compact icon buttons on wide screens; a dropdown on narrow ones.
 */
export function LinkActions({
  link,
  banned,
  canBan,
  admin,
}: {
  link: EditableLink
  banned: boolean
  canBan: boolean
  admin?: boolean
}) {
  const qc = useQueryClient()
  const [dialog, setDialog] = useState<Dialog>(null)
  const close = (o: boolean) => !o && setDialog(null)
  const short = stripProtocol(link.link)

  const icon = (label: string, onClick: () => void, children: React.ReactNode, destructive?: boolean) => (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={label}
            onClick={onClick}
            className={cn(destructive && "text-destructive hover:text-destructive")}
          />
        }
      >
        {children}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )

  return (
    <>
      <div className="hidden items-center justify-end gap-0.5 md:flex">
        <Tooltip>
          <TooltipTrigger
            render={
              <RouterLink
                to={`/stats?id=${link.id}`}
                aria-label="Stats"
                className={buttonVariants({ variant: "ghost", size: "icon-sm" })}
              />
            }
          >
            <BarChart3 />
          </TooltipTrigger>
          <TooltipContent>Stats</TooltipContent>
        </Tooltip>
        {icon("QR code", () => setDialog("qr"), <QrCode />)}
        {!banned && icon("Edit", () => setDialog("edit"), <Pencil />)}
        {canBan && !banned && icon("Ban", () => setDialog("ban"), <Ban />, true)}
        {icon("Delete", () => setDialog("delete"), <Trash2 />, true)}
      </div>
      <div className="flex justify-end md:hidden">
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={`Actions for ${short}`} />}>
            <MoreHorizontal />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem render={<RouterLink to={`/stats?id=${link.id}`} />}>
              <BarChart3 /> Stats
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => void copyText(link.link)}>
              <Copy /> Copy
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => setDialog("qr")}>
              <QrCode /> QR code
            </DropdownMenuItem>
            {!banned && (
              <DropdownMenuItem onClick={() => setDialog("edit")}>
                <Pencil /> Edit
              </DropdownMenuItem>
            )}
            <DropdownMenuSeparator />
            {canBan && !banned && (
              <DropdownMenuItem variant="destructive" onClick={() => setDialog("ban")}>
                <Ban /> Ban
              </DropdownMenuItem>
            )}
            <DropdownMenuItem variant="destructive" onClick={() => setDialog("delete")}>
              <Trash2 /> Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {dialog === "qr" && <QRDialog url={link.link} open onOpenChange={close} />}
      {dialog === "edit" && <EditLinkDialog link={link} admin={admin} open onOpenChange={close} />}
      {dialog === "delete" && (
        <ConfirmDialog
          open
          onOpenChange={close}
          title="Delete link?"
          description={short}
          confirmLabel="Delete"
          onConfirm={async () => {
            await api.deleteLink(link.id)
            await invalidateLinks(qc)
            toast.success("Link deleted")
          }}
        />
      )}
      {dialog === "ban" && (
        <ConfirmDialog
          open
          onOpenChange={close}
          title="Ban link?"
          description={short}
          confirmLabel="Ban"
          options={[
            { key: "user", label: "Ban link owner" },
            { key: "userLinks", label: "Ban all links of the owner" },
            { key: "host", label: "Ban target host" },
            { key: "domain", label: "Ban custom domain" },
          ]}
          onConfirm={async (v) => {
            await api.banLink(link.id, v)
            await invalidateLinks(qc)
            toast.success("Link banned")
          }}
        />
      )}
    </>
  )
}
