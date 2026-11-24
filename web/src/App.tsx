import { useEffect, useMemo, useState } from "react"

import { DocView, type Filters } from "@/components/doc-view"
import { Inspector } from "@/components/inspector"
import { Sidebar } from "@/components/sidebar"
import { Empty, EmptyHeader, EmptyTitle } from "@/components/ui/empty"
import { DocKind } from "@/gen/pair/v1/pair_pb"
import { NavContext, useHashRoute } from "@/lib/nav"
import { useDocs, useEvents, useFeedback } from "@/lib/queries"
import { errorText, kindName } from "@/lib/rpc"

export default function App() {
  const connected = useEvents()
  const [route, go] = useHashRoute()
  const docs = useDocs()
  const feedback = useFeedback()
  const [filters, setFilters] = useState<Filters>({ review: false, workaround: false })

  // 首次打开且地址未指定文档时, 打开分组顺序中的第一篇.
  useEffect(() => {
    if (route || !docs.data) return
    const first = [DocKind.MODULE, DocKind.FLOW, DocKind.DECISION]
      .map((k) => docs.data.find((d) => d.kind === k))
      .find(Boolean)
    if (first) go({ kind: kindName(first.kind), id: first.id }, true)
  }, [route, docs.data, go])

  const nav = useMemo(() => ({ route, go, docs: docs.data ?? [] }), [route, go, docs.data])
  const items = feedback.data ?? []

  return (
    <NavContext value={nav}>
      <div className="grid h-dvh min-w-[60rem] grid-cols-[15.5rem_minmax(0,1fr)_18rem] overflow-hidden">
        <Sidebar filters={filters} setFilters={setFilters} feedback={items} connected={connected} />
        <main className="flex min-h-0 min-w-0 flex-col">
          {route ? (
            <DocView key={`${route.kind}/${route.id}`} route={route} filters={filters} feedback={items} />
          ) : (
            <Empty>
              <EmptyHeader>
                <EmptyTitle>{docs.isError ? errorText(docs.error) : docs.data?.length === 0 && "还没有文档"}</EmptyTitle>
              </EmptyHeader>
            </Empty>
          )}
        </main>
        <Inspector feedback={items} />
      </div>
    </NavContext>
  )
}
