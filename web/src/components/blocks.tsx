import { useState } from "react"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import { motion } from "motion/react"
import { cn } from "cn"

import { Compose } from "@/components/compose"
import { Mermaid } from "@/components/mermaid"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  BlockKind,
  DocKind,
  FeedbackKind,
  FeedbackState,
  type Block,
  type LayoutNode,
} from "@/gen/pair/v1/pair_pb"
import { kindName } from "@/lib/rpc"
import { useDocCtx, useNav } from "@/lib/nav"
import { useStageDecide } from "@/lib/queries"

/** Markdown 渲染普通正文; 跳过 HTML, 让 pw: 注释与锚点不显示. */
export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={cn("md", className)}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} skipHtml>
        {text}
      </ReactMarkdown>
    </div>
  )
}

export function Blocks({ blocks }: { blocks: Block[] }) {
  return (
    <div className="flex flex-col gap-3">
      {blocks.map((b, i) => (
        <BlockView key={b.id || i} block={b} />
      ))}
    </div>
  )
}

function BlockView({ block }: { block: Block }) {
  switch (block.kind) {
    case BlockKind.MERMAID:
      return <DocMermaid code={block.code} />
    case BlockKind.LAYOUT:
      return <Layout nodes={block.layout} />
    case BlockKind.COMPARE:
      return <Compare block={block} />
    case BlockKind.SCENARIOS:
      return <Scenarios block={block} />
    case BlockKind.WORKAROUND:
      return <Workaround block={block} />
    default:
      return <Markdown text={block.markdown} />
  }
}

/** DocMermaid 把与本文档区块同名的节点连到该区块. */
function DocMermaid({ code }: { code: string }) {
  const { doc } = useDocCtx()
  const { go } = useNav()
  return (
    <Mermaid
      code={code}
      links={doc.sections.map((s) => s.id)}
      onLink={(sec) => go({ kind: kindName(doc.kind), id: doc.id, sec })}
    />
  )
}

/** Layout 把 pw-layout 缩进树画成统一线框: 直角、单一字号, 子节点嵌在父节点内. */
function Layout({ nodes }: { nodes: LayoutNode[] }) {
  return (
    <div className="flex flex-col gap-1.5 text-xs">
      {nodes.map((n, i) =>
        n.children.length === 0 ? (
          <div key={i} className="border border-foreground/20 bg-muted/40 px-2 py-1.5 text-muted-foreground">
            {n.label}
          </div>
        ) : (
          <div key={i} className="border border-foreground/20 p-1.5">
            <div className="px-0.5 pb-1.5">{n.label}</div>
            <Layout nodes={n.children} />
          </div>
        ),
      )}
    </div>
  )
}

/** Compare 并排展示方案; 在问题文档中, 方案即问题选项时可直接选择并暂存. */
function Compare({ block }: { block: Block }) {
  const { doc, feedback } = useDocCtx()
  const decide = useStageDecide()
  const decision = doc.kind === DocKind.DECISION ? doc.decision : undefined
  const choosable = !!decision && block.items.every((i) => decision.options.some((o) => o.id === i.id))
  const pick = feedback.find((f) => f.kind === FeedbackKind.DECISION && f.id === doc.id)

  return (
    <div className="grid grid-cols-[repeat(auto-fit,minmax(15rem,1fr))] gap-3">
      {block.items.map((item) => {
        const picked = choosable && pick?.pick === item.id
        return (
          <motion.div
            key={item.id}
            layout
            className={cn(
              "flex flex-col rounded-lg border bg-card transition-[border-color,box-shadow]",
              picked && "border-primary/60 ring-1 ring-primary/40",
            )}
          >
            <div className="flex min-h-9 items-center gap-1.5 border-b px-3 py-1.5">
              <span className="text-[13px] font-medium">{item.title}</span>
              {decision?.defaultOption === item.id && <Badge variant="secondary">默认</Badge>}
              {picked && <Badge>{pick.state === FeedbackState.STAGED ? "已选择" : "已发送"}</Badge>}
            </div>
            <div className="flex-1 px-3 py-2.5">
              <Blocks blocks={item.blocks} />
            </div>
            {choosable && (
              <div className="flex items-center gap-2 border-t px-3 py-1.5">
                <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{picked && pick.note}</span>
                <Compose
                  quote={item.title}
                  placeholder="说明 (可选)"
                  action="选择"
                  optional
                  pending={decide.isPending}
                  onSubmit={(note, done) =>
                    decide.mutate({ decisionId: doc.id, pick: item.id, note }, { onSuccess: done })
                  }
                  trigger={
                    <Button size="sm" variant={picked ? "outline" : "default"}>
                      {picked ? "修改说明" : "选择此方案"}
                    </Button>
                  }
                />
              </div>
            )}
          </motion.div>
        )
      })}
    </div>
  )
}

function Scenarios({ block }: { block: Block }) {
  const [tab, setTab] = useState(block.items[0]?.id)
  return (
    <Tabs value={tab} onValueChange={setTab} className="gap-3">
      <TabsList variant="line">
        {block.items.map((i) => (
          <TabsTrigger key={i.id} value={i.id}>
            {i.title}
          </TabsTrigger>
        ))}
      </TabsList>
      {block.items.map((i) => (
        <TabsContent key={i.id} value={i.id}>
          <motion.div initial={{ opacity: 0, y: 3 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.18 }}>
            <Blocks blocks={i.blocks} />
          </motion.div>
        </TabsContent>
      ))}
    </Tabs>
  )
}

// 引用块开头的「**过渡做法（解除条件：…）**：」已由卡片标题展示.
const workaroundLead = /\*\*过渡做法[（(]解除条件[：:][^*]*[）)]\*\*[：:]?\s*/

function Workaround({ block }: { block: Block }) {
  return (
    <div className="rounded-lg border bg-unconfirmed/5 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
        <Badge variant="unconfirmed">过渡做法</Badge>
        <span className="text-muted-foreground">解除条件</span>
        <span className="font-medium">{block.condition}</span>
      </div>
      <Markdown
        text={block.markdown.replace(workaroundLead, "")}
        className="mt-1.5 [&_blockquote]:border-0 [&_blockquote]:p-0 [&_blockquote]:text-foreground"
      />
    </div>
  )
}
