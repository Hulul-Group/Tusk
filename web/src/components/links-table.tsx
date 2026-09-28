import { useState } from "react"
import { Copy, Lock, Search } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { TablePager } from "@/components/controls"
import { LinkActions } from "@/components/link-actions"
import { isAdmin, useLinks, useMe } from "@/lib/api"
import { copyText, formatNumber, msLong, relativeTime, stripProtocol, useDebounced } from "@/lib/format"

/** Dashboard links table (Kutt partials/links/table.hbs), shown on the homepage for users. */
export function LinksTable() {
  const { data: me } = useMe()
  const [search, setSearch] = useState("")
  const [page, setPage] = useState({ limit: 10, skip: 0 })
  const debounced = useDebounced(search, 500)
  const q = { ...page, search: debounced || undefined }
  const { data, isPending, isFetching, error } = useLinks(q)
  const admin = isAdmin(me)

  return (
    <Card>
      <CardHeader className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <CardTitle className="text-xl">Recent shortened links</CardTitle>
        <div className="relative w-full sm:w-64">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            aria-label="Search links"
            placeholder="Search..."
            className="pl-8"
            value={search}
            onChange={(e) => {
              setSearch(e.target.value)
              setPage((p) => ({ ...p, skip: 0 }))
            }}
          />
        </div>
      </CardHeader>
      <CardContent className="grid gap-4">
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error.message}</AlertDescription>
          </Alert>
        )}
        <div className="overflow-x-auto">
          <Table aria-busy={isFetching}>
            <TableHeader>
              <TableRow>
                <TableHead className="hidden sm:table-cell">Original URL</TableHead>
                <TableHead className="hidden lg:table-cell">Created</TableHead>
                <TableHead>Short link</TableHead>
                <TableHead className="text-right">Views</TableHead>
                <TableHead className="w-10 text-right">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {isPending &&
                Array.from({ length: 3 }).map((_, i) => (
                  <TableRow key={i}>
                    <TableCell colSpan={5}>
                      <Skeleton className="h-6 w-full" />
                    </TableCell>
                  </TableRow>
                ))}
              {data && data.data.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="py-10 text-center text-muted-foreground">
                    {debounced ? "No links match your search." : "No links yet. Shorten your first link above."}
                  </TableCell>
                </TableRow>
              )}
              {data?.data.map((l) => {
                const exp = msLong(l.expire_in)
                return (
                  <TableRow key={l.id}>
                    <TableCell className="hidden max-w-[12rem] sm:table-cell lg:max-w-xs">
                      <a href={l.target} target="_blank" rel="noopener noreferrer" className="block truncate hover:underline" title={l.target}>
                        {l.target}
                      </a>
                      {l.description && <p className="truncate text-xs text-muted-foreground">{l.description}</p>}
                    </TableCell>
                    <TableCell className="hidden whitespace-nowrap text-muted-foreground lg:table-cell">
                      {relativeTime(l.created_at)}
                      {exp && <p className="text-xs">Expires in {exp}</p>}
                    </TableCell>
                    <TableCell className="max-w-[13rem] sm:max-w-[12rem] lg:max-w-xs">
                      <div className="flex min-w-0 items-center gap-1">
                        <Button variant="ghost" size="icon-xs" aria-label={`Copy ${stripProtocol(l.link)}`} onClick={() => void copyText(l.link)}>
                          <Copy />
                        </Button>
                        <a href={l.link} target="_blank" rel="noopener noreferrer" className="truncate font-medium hover:underline">
                          {stripProtocol(l.link)}
                        </a>
                        {l.password && <Lock className="size-3.5 shrink-0 text-muted-foreground" aria-label="Password protected" />}
                        {l.banned && <Badge variant="destructive" className="shrink-0">Banned</Badge>}
                      </div>
                      {/* Mobile: the target lives under the short link (Original URL column is hidden). */}
                      <a href={l.target} target="_blank" rel="noopener noreferrer" className="block truncate pl-7 text-xs text-muted-foreground sm:hidden" title={l.target}>
                        {stripProtocol(l.target)}
                      </a>
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{formatNumber(l.visit_count)}</TableCell>
                    <TableCell className="text-right">
                      <LinkActions link={l} banned={l.banned} canBan={admin} admin={admin} />
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
        {data && <TablePager total={data.total} limit={page.limit} skip={page.skip} onChange={setPage} loading={isFetching} />}
      </CardContent>
    </Card>
  )
}
