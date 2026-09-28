import { useNavigate } from "react-router"
import { useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"

import { api, qk } from "@/lib/api"

export function useLogout() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  return async () => {
    try {
      await api.logout()
    } catch {
      // Older servers without /auth/logout: fall back to the server-rendered /logout page.
      window.location.assign("/logout")
      return
    }
    qc.setQueryData(qk.me, null)
    qc.removeQueries({ queryKey: ["links"] })
    qc.removeQueries({ queryKey: ["admin"] })
    toast.success("Logged out")
    navigate("/")
  }
}
