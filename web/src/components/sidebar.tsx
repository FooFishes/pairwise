import { useEffect, useRef, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { SearchIcon } from "lucide-react"
import { cn } from "cn"

import type { Filters } from "@/components/doc-view"
import { Badge } from "@/components/ui/badge"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Kbd } from "@/components/ui/kbd"
import { Switch } from "@/components/ui/switch"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { DocKind, FeedbackKind, FeedbackState, type DocSummary, type Feedback } from "@/gen/pair/v1/pair_pb"
import { useNav } from "@/lib/nav"
import { useStatus } from "@/lib/queries"
import { kindName } from "@/lib/rpc"

const groups = [
  [DocKind.MODULE, "模块"],
  [DocKind.FLOW, "流程"],
  [DocKind.DECISION, "待确认问题"],
] as const

const needsReview = (d: DocSummary) =>
  d.kind === DocKind.DECISION ||
  d.unconfirmedCount + d.changedCount + d.assumptionCount + d.retractedCount > 0

/** visibleDocs 返回按分组排序、经搜索与过滤后的文档, 也是键盘上下切换的顺序. */
function visibleDocs(docs: DocSummary[], query: string, filters: Filters) {
  const q = query.trim().toLowerCase()
  return groups.flatMap(([kind]) =>
    docs.filter(
      (d) =>
        d.kind === kind &&
        (!q || [d.id, d.title, d.path].some((s) => s.toLowerCase().includes(q))) &&
        (!filters.review || needsReview(d)) &&
        (!filters.workaround || d.workaroundCount > 0),
    ),
  )
}

export function Sidebar({
  filters,
  setFilters,
  feedback,
  connected,
}: {
  filters: Filters
  setFilters: (f: Filters) => void
  feedback: Feedback[]
  connected: boolean
}) {
  const { route, go, docs } = useNav()
  const status = useStatus()
  const search = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState("")
  const list = visibleDocs(docs, query, filters)
  const current = list.findIndex((d) => d.id === route?.id && kindName(d.kind) === route.kind)

  const step = (delta: number) => {
    const next = list[Math.min(Math.max(current + delta, 0), list.length - 1)]
    if (next) go({ kind: kindName(next.kind), id: next.id })
  }

  // ⌘K 聚焦搜索.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        search.current?.focus()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  const root = status.data?.root.split("/").pop()

  return (
    // 焦点在侧栏 (搜索框或文档列表) 时方向键在文档间切换.
    <aside
      className="flex min-h-0 flex-col border-r bg-sidebar text-sidebar-foreground"
      onKeyDown={(e) => {
        if (e.key === "ArrowDown" || e.key === "ArrowUp") {
          e.preventDefault()
          step(e.key === "ArrowDown" ? 1 : -1)
        }
      }}
    >
      <div className="flex h-11 shrink-0 items-center gap-2 px-3">
        <span className="truncate text-[13px] font-semibold">{root}</span>
        <span className="truncate text-xs text-muted-foreground">{status.data?.branch}</span>
        <AnimatePresence>
          {!connected && (
            <motion.span
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              className="ml-auto shrink-0 text-xs text-destructive"
            >
              未连接
            </motion.span>
          )}
        </AnimatePresence>
      </div>
      <div className="px-2 pb-2">
        <InputGroup className="bg-background/60">
          <InputGroupAddon>
            <SearchIcon />
          </InputGroupAddon>
          <InputGroupInput
            ref={search}
            value={query}
            placeholder="搜索"
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setQuery("")
                e.currentTarget.blur()
              }
            }}
          />
          <InputGroupAddon align="inline-end">
            <Kbd>⌘K</Kbd>
          </InputGroupAddon>
        </InputGroup>
      </div>

      <nav className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        {groups.map(([kind, title]) => {
          const items = list.filter((d) => d.kind === kind)
          return (
            <div key={kind} className="mt-2 first:mt-0">
              <h2 className="flex items-center px-2 py-1 text-[11px] font-medium text-muted-foreground">
                {title}
                <span className="ml-auto tabular-nums">{items.length || ""}</span>
              </h2>
              <AnimatePresence initial={false}>
                {items.map((d) => (
                  <motion.div
                    key={d.id}
                    layout="position"
                    initial={{ opacity: 0, height: 0 }}
                    animate={{ opacity: 1, height: "auto" }}
                    exit={{ opacity: 0, height: 0 }}
                    transition={{ duration: 0.16 }}
                    className="overflow-hidden"
                  >
                    <DocItem doc={d} feedback={feedback} active={d === list[current]} />
                  </motion.div>
                ))}
              </AnimatePresence>
            </div>
          )
        })}
        {docs.length > 0 && list.length === 0 && <p className="px-2 py-6 text-center text-xs text-muted-foreground">没有匹配的文档</p>}
      </nav>

      <FieldGroup className="shrink-0 gap-2 border-t px-4 py-3">
        <FilterSwitch id="f-review" label="仅看需审阅" checked={filters.review} onChange={(review) => setFilters({ ...filters, review })} />
        <FilterSwitch id="f-workaround" label="过渡项" checked={filters.workaround} onChange={(workaround) => setFilters({ ...filters, workaround })} />
      </FieldGroup>
    </aside>
  )
}

function FilterSwitch({ id, label, checked, onChange }: { id: string; label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <Field orientation="horizontal">
      <FieldLabel htmlFor={id} className="text-xs font-normal">
        {label}
      </FieldLabel>
      <Switch id={id} size="sm" checked={checked} onCheckedChange={onChange} />
    </Field>
  )
}

const counts = [
  ["unconfirmedCount", "unconfirmed", "未确认"],
  ["changedCount", "changed", "含未审阅改动"],
  ["assumptionCount", "secondary", "假设"],
  ["retractedCount", "retracted", "撤回假设"],
] as const

function DocItem({ doc, feedback, active }: { doc: DocSummary; feedback: Feedback[]; active: boolean }) {
  const { go } = useNav()
  const pick =
    doc.kind === DocKind.DECISION &&
    feedback.find((f) => f.kind === FeedbackKind.DECISION && f.id === doc.id)
  return (
    <button
      type="button"
      title={doc.path}
      onClick={() => go({ kind: kindName(doc.kind), id: doc.id })}
      className={cn(
        "flex h-7 w-full items-center gap-1.5 rounded-md px-2 text-left text-[13px] transition-colors hover:bg-sidebar-accent/60",
        active && "bg-sidebar-accent text-sidebar-accent-foreground hover:bg-sidebar-accent",
      )}
    >
      <span className="min-w-0 flex-1 truncate">{doc.title || doc.id}</span>
      {pick && <Badge variant="outline">{pick.state === FeedbackState.STAGED ? "已选" : "已发送"}</Badge>}
      {counts.map(
        ([field, variant, label]) =>
          doc[field] > 0 && (
            <Tooltip key={field}>
              <TooltipTrigger asChild>
                <Badge variant={variant} className="h-4 min-w-4 px-1 tabular-nums">
                  {doc[field]}
                </Badge>
              </TooltipTrigger>
              <TooltipContent side="right">{label}</TooltipContent>
            </Tooltip>
          ),
      )}
    </button>
  )
}
