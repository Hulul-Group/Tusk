import { Navigate } from "react-router"

import { Shortener } from "@/components/shortener"
import { LinksTable } from "@/components/links-table"
import { Skeleton } from "@/components/ui/skeleton"
import { useConfig, useMe } from "@/lib/api"

export default function HomePage() {
  const { data: cfg } = useConfig()
  const { data: me, isPending } = useMe()

  if (cfg?.needs_admin_setup) return <Navigate to="/create-admin" replace />
  if (!cfg || isPending) {
    return (
      <div className="mx-auto grid max-w-3xl gap-4">
        <Skeleton className="mx-auto h-12 w-2/3" />
        <Skeleton className="h-11 w-full" />
      </div>
    )
  }
  return (
    <div className="grid gap-12">
      <Shortener cfg={cfg} user={me} />
      {me && <LinksTable />}
    </div>
  )
}
