import { useMemo, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { diffWords } from "diff"

import { Blocks } from "@/components/blocks"
import { Compose } from "@/components/compose"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  DocKind,
  FeedbackKind,
  FeedbackState,
  SectionStatus,
  type Section,
} from "@/gen/pair/v1/pair_pb"
import { useDocCtx } from "@/lib/nav"
import { useAckRetracted, useConfirm, useSectionDiff, useStageNote } from "@/lib/queries"
import { anchorId, errorText } from "@/lib/rpc"

const statuses = {
  [SectionStatus.UNCONFIRMED]: ["unconfirmed", "未确认"],
  [SectionStatus.CONFIRMED]: ["confirmed", "已确认"],
  [SectionStatus.CHANGED]: ["changed", "含未审阅改动"],
} as const

const fade = {
  initial: { opacity: 0 },
  animate: { opacity: 1 },
  exit: { opacity: 0 },
  transition: { duration: 0.15 },
}

/** SectionView 是一个受管区块: 状态、确认与批注、未审阅改动的字词差异、假设与撤回假设. */
export function SectionView({ section: s }: { section: Section }) {
  const { doc, feedback } = useDocCtx()
  const confirm = useConfirm()
  const note = useStageNote()
  const [view, setView] = useState<"diff" | "body">("diff")
  const target = `${doc.id}#${s.id}`
  const managed = doc.kind !== DocKind.DECISION
  const changed = s.status === SectionStatus.CHANGED
  const notes = feedback.filter(
    (f) =>
      f.kind === FeedbackKind.NOTE && f.state === FeedbackState.STAGED && f.target === target && !f.assumptionId,
  )

  return (
    <motion.section
      id={anchorId(s.id)}
      layout="position"
      {...fade}
      className="relative -mx-3 scroll-mt-3 rounded-lg px-3 py-4 before:absolute before:inset-x-3 before:top-0 before:h-px before:bg-border first:before:hidden"
    >
      <header className="flex min-h-7 items-center gap-2">
        <h2 className="truncate text-[15px] font-semibold">{s.title}</h2>
        {managed && <StatusBadge status={s.status} />}
        <div className="ml-auto flex shrink-0 items-center gap-1">
          {changed && (
            <ToggleGroup
              type="single"
              variant="outline"
              spacing={0}
              value={view}
              onValueChange={(v) => v && setView(v as typeof view)}
            >
              <ToggleGroupItem value="diff">差异</ToggleGroupItem>
              <ToggleGroupItem value="body">正文</ToggleGroupItem>
            </ToggleGroup>
          )}
          {managed && (
            <>
              <Compose
                quote={s.title}
                placeholder="修改说明"
                action="暂存"
                pending={note.isPending}
                onSubmit={(message, done) =>
                  note.mutate({ target, assumptionId: "", message }, { onSuccess: done })
                }
                trigger={
                  <Button variant="ghost">批注</Button>
                }
              />
              {s.status !== SectionStatus.CONFIRMED && (
                <Button variant="outline" disabled={confirm.isPending} onClick={() => confirm.mutate(target)}>
                  确认
                </Button>
              )}
            </>
          )}
        </div>
      </header>

      <AnimatePresence initial={false}>
        {notes.map((f) => (
          <motion.p
            key={f.id}
            initial={{ opacity: 0, height: 0 }}
            animate={{ opacity: 1, height: "auto" }}
            exit={{ opacity: 0, height: 0 }}
            className="overflow-hidden text-xs text-muted-foreground"
          >
            <span className="mt-1.5 block">已暂存批注: {f.note}</span>
          </motion.p>
        ))}
      </AnimatePresence>

      <div className="mt-2.5">
        <AnimatePresence mode="wait" initial={false}>
          {changed && view === "diff" ? (
            <motion.div key="diff" {...fade}>
              <SectionDiff target={target} hash={s.hash} />
            </motion.div>
          ) : (
            <motion.div key="body" {...fade}>
              <Blocks blocks={s.blocks} />
            </motion.div>
          )}
        </AnimatePresence>
      </div>

      {s.assumptions.length > 0 && (
        <div className="mt-3 flex flex-col gap-0.5">
          <h3 className="px-2 text-[11px] text-muted-foreground">假设</h3>
          {s.assumptions.map((a) => {
            const staged = feedback.some(
              (f) => f.kind === FeedbackKind.NOTE && f.target === target && f.assumptionId === a.id,
            )
            return (
              <div key={a.id} className="group/row flex items-center gap-2 rounded-md px-2 py-1 hover:bg-muted/60">
                <span className="flex-1 text-[12.5px]">{a.text}</span>
                {staged && <Badge variant="secondary">已批注</Badge>}
                <Compose
                  quote={a.text}
                  placeholder="修改说明"
                  action="暂存"
                  pending={note.isPending}
                  onSubmit={(message, done) =>
                    note.mutate({ target, assumptionId: a.id, message }, { onSuccess: done })
                  }
                  trigger={
                    <Button variant="ghost" size="xs" className="opacity-60 group-hover/row:opacity-100">
                      批注
                    </Button>
                  }
                />
              </div>
            )
          })}
        </div>
      )}

      {s.retracted.length > 0 && <Retracted target={target} section={s} />}
    </motion.section>
  )
}

function StatusBadge({ status }: { status: SectionStatus }) {
  const [variant, label] = statuses[status as keyof typeof statuses]
  return (
    <motion.span key={status} initial={{ opacity: 0, scale: 0.92 }} animate={{ opacity: 1, scale: 1 }}>
      <Badge variant={variant}>{label}</Badge>
    </motion.span>
  )
}

function Retracted({ target, section }: { target: string; section: Section }) {
  const ack = useAckRetracted()
  return (
    <div className="mt-3 flex flex-col gap-0.5">
      <h3 className="px-2 text-[11px] text-retracted">Agent 撤回假设</h3>
      <AnimatePresence initial={false}>
        {section.retracted.map((a) => (
          <motion.div
            key={a.id}
            exit={{ opacity: 0, height: 0 }}
            className="flex items-center gap-2 overflow-hidden rounded-md px-2 py-1 hover:bg-muted/60"
          >
            <span className="flex-1 text-[12.5px] text-muted-foreground line-through decoration-retracted/50">
              {a.text}
            </span>
            <Button
              variant="ghost"
              size="xs"
              disabled={ack.isPending}
              onClick={() => ack.mutate({ target, assumptionId: a.id })}
            >
              知晓
            </Button>
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  )
}

const segmenter = new Intl.Segmenter("zh", { granularity: "word" })

/** SectionDiff 以确认原文为基准高亮当前内容中改动的字词. */
function SectionDiff({ target, hash }: { target: string; hash: string }) {
  const q = useSectionDiff(target, hash)
  const parts = useMemo(
    () => q.data?.available && diffWords(q.data.confirmed, q.data.current, { intlSegmenter: segmenter }),
    [q.data],
  )
  if (q.isPending) return <Skeleton className="h-20" />
  if (q.isError) return <p className="text-xs text-destructive">{errorText(q.error)}</p>
  if (!parts) return <p className="text-xs text-muted-foreground">确认原文未入库</p>
  return (
    <div className="rounded-md bg-muted/50 px-3 py-2 text-[12.5px] leading-relaxed break-words whitespace-pre-wrap">
      {parts.map((p, i) =>
        p.added ? (
          <ins key={i} className="rounded-xs bg-inserted no-underline">
            {p.value}
          </ins>
        ) : p.removed ? (
          <del key={i} className="rounded-xs bg-removed text-muted-foreground decoration-foreground/40">
            {p.value}
          </del>
        ) : (
          <span key={i}>{p.value}</span>
        ),
      )}
    </div>
  )
}
