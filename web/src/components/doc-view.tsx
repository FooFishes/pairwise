import { useEffect, useMemo } from "react"
import { AnimatePresence, motion } from "motion/react"

import { Blocks } from "@/components/blocks"
import { SectionView } from "@/components/section"
import { Badge } from "@/components/ui/badge"
import { Empty, EmptyHeader, EmptyTitle } from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import {
  BlockKind,
  DocKind,
  FeedbackKind,
  FeedbackState,
  SectionStatus,
  Severity,
  type Doc,
  type Feedback,
  type Section,
} from "@/gen/pair/v1/pair_pb"
import { DocContext, useNav, type Route } from "@/lib/nav"
import { useDoc } from "@/lib/queries"
import { anchorId, errorText, hasBlock, isNotFound, kindNames, progressLabels } from "@/lib/rpc"

export type Filters = { review: boolean; workaround: boolean }

const needsReview = (s: Section) =>
  s.status !== SectionStatus.CONFIRMED || s.assumptions.length > 0 || s.retracted.length > 0

export function DocView({ route, filters, feedback }: { route: Route; filters: Filters; feedback: Feedback[] }) {
  const kind = kindNames[route.kind]
  const q = useDoc(kind, route.id)

  // 文档渲染完成后再定位区块, 重复跳转到同一区块时依靠 seq 再次触发.
  const docId = q.data?.id
  useEffect(() => {
    if (!docId || !route.sec) return
    const frame = requestAnimationFrame(() => {
      const el = document.getElementById(anchorId(route.sec!))
      if (!el) return
      el.scrollIntoView({ behavior: "smooth", block: "start" })
      el.animate(
        [{ backgroundColor: "color-mix(in oklch, var(--primary) 9%, transparent)" }, { backgroundColor: "transparent" }],
        { duration: 1400, easing: "ease-out" },
      )
    })
    return () => cancelAnimationFrame(frame)
  }, [docId, route.sec, route.seq])

  const ctx = useMemo(() => q.data && { doc: q.data, feedback }, [q.data, feedback])

  if (q.isPending) {
    return (
      <div className="flex flex-col gap-3 px-8 py-6">
        <Skeleton className="h-5 w-48" />
        <Skeleton className="h-4 w-72" />
        <Skeleton className="mt-4 h-32" />
      </div>
    )
  }
  if (q.isError) {
    const gone = isNotFound(q.error)
    return (
      <Empty>
        <EmptyHeader>
          <EmptyTitle>
            {gone ? (kind === DocKind.DECISION ? "该问题已结案" : "文档不存在") : errorText(q.error)}
          </EmptyTitle>
        </EmptyHeader>
      </Empty>
    )
  }

  const doc = q.data
  const visible = doc.sections.filter(
    (s) =>
      s.id === route.sec ||
      ((!filters.review || needsReview(s)) && (!filters.workaround || hasBlock(s.blocks, BlockKind.WORKAROUND))),
  )

  return (
    <DocContext value={ctx!}>
      <header className="flex h-11 shrink-0 items-center gap-2 border-b px-5">
        <h1 className="max-w-[70%] shrink-0 truncate text-[13px] font-semibold">{doc.title || doc.id}</h1>
        <span className="min-w-0 truncate font-mono text-[11px] text-muted-foreground">
          {doc.id} {doc.path}
        </span>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <motion.div
          initial={{ opacity: 0, y: 4 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.2, ease: "easeOut" }}
          className="mx-auto flex max-w-3xl flex-col px-8 pt-5 pb-24"
        >
          <DocMeta doc={doc} />
          {doc.decision && <DecisionSummary doc={doc} feedback={feedback} />}
          {doc.preambleBlocks.length > 0 && (
            // 前言区的一级标题即文档标题, 已在顶栏显示.
            <div className="mt-4 [&_.md>h1]:hidden">
              <Blocks blocks={doc.preambleBlocks} />
            </div>
          )}
          <div className="mt-2 flex flex-col">
            <AnimatePresence initial={false}>
              {visible.map((s) => (
                <SectionView key={s.id} section={s} />
              ))}
            </AnimatePresence>
          </div>
          {visible.length === 0 && <p className="py-10 text-center text-xs text-muted-foreground">没有符合筛选的区块</p>}
        </motion.div>
      </div>
    </DocContext>
  )
}

function DocMeta({ doc }: { doc: Doc }) {
  const { openTarget } = useNav()
  if (doc.scope.length + doc.refs.length + doc.diagnostics.length === 0) return null
  return (
    <div className="flex flex-col gap-2">
      {(doc.scope.length > 0 || doc.refs.length > 0) && (
        <div className="flex flex-wrap items-center gap-1">
          {doc.scope.map((g) => (
            <Badge key={g} variant="outline" className="font-mono">
              {g}
            </Badge>
          ))}
          {doc.refs.map((r) => (
            <Badge key={r} variant="secondary" asChild>
              <button type="button" onClick={() => openTarget(r)}>
                @{r}
              </button>
            </Badge>
          ))}
        </div>
      )}
      {doc.diagnostics.length > 0 && (
        <ul className="flex flex-col gap-0.5 rounded-md border px-3 py-2 text-xs">
          {doc.diagnostics.map((d, i) => (
            <li key={i} className={d.severity === Severity.ERROR ? "text-destructive" : "text-unconfirmed"}>
              {d.line > 0 && <span className="mr-1.5 font-mono text-muted-foreground">L{d.line}</span>}
              {d.message}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** DecisionSummary 汇总问题的受影响目标与闭环进度、默认方案与当前选择. */
function DecisionSummary({ doc, feedback }: { doc: Doc; feedback: Feedback[] }) {
  const { openTarget } = useNav()
  const d = doc.decision!
  const title = (id: string) => d.options.find((o) => o.id === id)?.title || id
  const pick = feedback.find((f) => f.kind === FeedbackKind.DECISION && f.id === doc.id)
  return (
    <dl className="mt-1 grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-5 gap-y-2 rounded-lg border px-4 py-3 text-xs">
      <dt className="text-muted-foreground">受影响目标</dt>
      <dd className="flex flex-col gap-1">
        {d.targets.map((t) => {
          const p = d.progress.find((p) => p.target === t)
          return (
            <div key={t} className="flex flex-wrap items-baseline gap-x-2">
              <button type="button" className="font-mono text-primary hover:underline" onClick={() => openTarget(t)}>
                {t}
              </button>
              {p && (
                <span className={p.status === "pending" ? "text-muted-foreground" : "text-confirmed"}>
                  {progressLabels[p.status]}
                </span>
              )}
              {p?.reason && <span className="text-muted-foreground">{p.reason}</span>}
            </div>
          )
        })}
      </dd>
      <dt className="text-muted-foreground">默认方案</dt>
      <dd>{title(d.defaultOption)}</dd>
      <dt className="text-muted-foreground">当前选择</dt>
      <dd>
        <AnimatePresence mode="wait" initial={false}>
          <motion.span
            key={pick ? `${pick.pick}-${pick.state}` : "none"}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.15 }}
            className="flex flex-wrap items-baseline gap-x-2"
          >
            {pick ? (
              <>
                <span className="font-medium">{pick.pickTitle || title(pick.pick)}</span>
                <span className="text-muted-foreground">{pick.state === FeedbackState.STAGED ? "已暂存" : "已发送"}</span>
                {pick.note && <span className="text-muted-foreground">{pick.note}</span>}
              </>
            ) : (
              <span className="text-muted-foreground">未选择</span>
            )}
          </motion.span>
        </AnimatePresence>
      </dd>
    </dl>
  )
}
