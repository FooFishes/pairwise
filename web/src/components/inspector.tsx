import { useState, type ReactNode } from "react"
import { AnimatePresence, motion } from "motion/react"
import { XIcon } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Field, FieldLabel } from "@/components/ui/field"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { FeedbackKind, FeedbackState, type Feedback, type Session } from "@/gen/pair/v1/pair_pb"
import { useNav } from "@/lib/nav"
import { progressLabels } from "@/lib/rpc"
import { useSessions, useStageRemove, useTrigger } from "@/lib/queries"

const rowMotion = {
  initial: { opacity: 0, height: 0 },
  animate: { opacity: 1, height: "auto" },
  exit: { opacity: 0, height: 0 },
  transition: { duration: 0.18, ease: "easeOut" },
} as const

/** Inspector 是右侧检查器: 暂存区与发送、已发送待闭环的反馈、Agent 会话. */
export function Inspector({ feedback }: { feedback: Feedback[] }) {
  const staged = feedback.filter((f) => f.state === FeedbackState.STAGED)
  const sent = feedback.filter((f) => f.state === FeedbackState.TRIGGERED || f.state === FeedbackState.DRAINED)
  const sessions = useSessions()

  return (
    <aside className="flex min-h-0 flex-col border-l bg-sidebar text-sidebar-foreground">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <Panel title="暂存区" count={staged.length}>
          <Rows items={staged} empty="暂存区为空" render={(f) => <FeedbackRow item={f} removable />} />
        </Panel>
        <SendBar count={staged.length} />
        <Panel title="待闭环" count={sent.length}>
          <Rows items={sent} empty="没有待闭环的反馈" render={(f) => <FeedbackRow item={f} />} />
        </Panel>
        <Panel title="会话" count={sessions.data?.length ?? 0}>
          <Rows items={sessions.data ?? []} empty="没有活跃会话" render={(s) => <SessionRow session={s} />} />
        </Panel>
      </div>
    </aside>
  )
}

function Panel({ title, count, children }: { title: string; count: number; children: ReactNode }) {
  return (
    <section className="px-2 pt-3 pb-1">
      <h2 className="flex items-center px-2 pb-1 text-[11px] font-medium text-muted-foreground">
        {title}
        <span className="ml-auto tabular-nums">{count || ""}</span>
      </h2>
      {children}
    </section>
  )
}

function Rows<T extends { id: string }>({
  items,
  empty,
  render,
}: {
  items: T[]
  empty: string
  render: (item: T) => ReactNode
}) {
  return (
    <div>
      <AnimatePresence initial={false}>
        {items.map((item) => (
          <motion.div key={item.id} {...rowMotion} className="overflow-hidden">
            {render(item)}
          </motion.div>
        ))}
      </AnimatePresence>
      {items.length === 0 && (
        // 等最后一项收起后再出现, 避免与退场动画重叠.
        <motion.p
          initial={{ opacity: 0 }}
          animate={{ opacity: 1, transition: { delay: 0.18 } }}
          className="px-2 py-1.5 text-xs text-muted-foreground"
        >
          {empty}
        </motion.p>
      )}
    </div>
  )
}

function SendBar({ count }: { count: number }) {
  const trigger = useTrigger()
  const [steer, setSteer] = useState(false)
  const send = () =>
    trigger.mutate(steer, {
      onSuccess: (res) => toast.success(`已发送 ${res.items.length} 条反馈`),
    })
  return (
    <div className="mx-4 mt-1 mb-2 flex items-center gap-3 border-b pb-3">
      <Field orientation="horizontal" className="w-auto">
        <Switch id="steer" size="sm" checked={steer} onCheckedChange={setSteer} />
        <FieldLabel htmlFor="steer" className="text-xs font-normal">
          立即转向
        </FieldLabel>
      </Field>
      <Button className="ml-auto" disabled={count === 0 || trigger.isPending} onClick={send}>
        {trigger.isPending && <Spinner data-icon="inline-start" />}
        发送
      </Button>
    </div>
  )
}

const stateLabels: Partial<Record<FeedbackState, string>> = {
  [FeedbackState.TRIGGERED]: "已发送",
  [FeedbackState.DRAINED]: "已读取",
}

function FeedbackRow({ item: f, removable }: { item: Feedback; removable?: boolean }) {
  const { go, openTarget } = useNav()
  const remove = useStageRemove()
  const decision = f.kind === FeedbackKind.DECISION
  const open = () => (decision ? go({ kind: "decision", id: f.id }) : openTarget(f.target))

  return (
    <div className="group/row relative rounded-md px-2 py-1.5 hover:bg-sidebar-accent/60">
      <button type="button" onClick={open} className="block w-full text-left">
        <div className="flex items-center gap-1.5 pr-5">
          <span className="truncate font-mono text-[11px] text-muted-foreground">{decision ? f.id : f.target}</span>
          {stateLabels[f.state] && (
            <Badge variant={f.state === FeedbackState.DRAINED ? "secondary" : "outline"} className="ml-auto">
              {stateLabels[f.state]}
            </Badge>
          )}
        </div>
        <p className="mt-0.5 text-xs leading-relaxed">
          {decision ? `选择 ${f.pickTitle || f.pick}` : f.note}
          {f.steer && <span className="ml-1.5 text-muted-foreground">立即转向</span>}
        </p>
        {decision && f.note && <p className="text-xs text-muted-foreground">{f.note}</p>}
        {f.assumptionText && <p className="line-clamp-2 text-xs text-muted-foreground">假设: {f.assumptionText}</p>}
      </button>
      {decision && f.state !== FeedbackState.STAGED && f.progress.length > 0 && (
        <ul className="mt-1 flex flex-col gap-0.5 text-[11px]">
          {f.progress.map((p) => (
            <li key={p.target} className="flex flex-wrap items-baseline gap-x-1.5">
              <button type="button" className="font-mono hover:underline" onClick={() => openTarget(p.target)}>
                {p.target}
              </button>
              <span className={p.status === "pending" ? "text-muted-foreground" : "text-confirmed"}>
                {progressLabels[p.status]}
              </span>
              {p.reason && <span className="w-full text-muted-foreground">{p.reason}</span>}
            </li>
          ))}
        </ul>
      )}
      {removable && (
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label="移除"
          disabled={remove.isPending}
          className="absolute top-1 right-1 opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100"
          onClick={() => remove.mutate(f.id)}
        >
          <XIcon />
        </Button>
      )}
    </div>
  )
}

const sessionStates: Record<string, string> = { idle: "空闲", running: "执行中", waiting: "等待" }

function SessionRow({ session: s }: { session: Session }) {
  const { openTarget } = useNav()
  return (
    <div className="rounded-md px-2 py-1.5 text-xs">
      <div className="flex items-center gap-1.5">
        <span className="font-mono">{s.shortCode || s.id}</span>
        <Badge variant={s.state === "running" ? "default" : "secondary"} className="ml-auto">
          {sessionStates[s.state]}
        </Badge>
      </div>
      {s.bindings.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-x-2 gap-y-0.5 font-mono text-[11px] text-muted-foreground">
          {s.bindings.flatMap((b) =>
            (b.sections.length ? b.sections.map((sec) => `${b.docId}#${sec}`) : [b.docId]).map((t) => (
              <button key={t} type="button" className="hover:text-foreground hover:underline" onClick={() => openTarget(t)}>
                {t}
              </button>
            )),
          )}
        </div>
      )}
      {s.preserve && <p className="mt-0.5 text-muted-foreground">保持规格: {s.preserve}</p>}
    </div>
  )
}
