import { useState, type ComponentProps } from "react"
import ReactMarkdown, { type Components, type ExtraProps } from "react-markdown"
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
import { kindName, sentLabels } from "@/lib/rpc"
import { routeHash, useDocCtx, useNav } from "@/lib/nav"
import { useStageDecide } from "@/lib/queries"

type MdNode = { type: string; value?: string; url?: string; children?: MdNode[] }

// 与 internal/doc 的 refRe 一致, 另外接受只写文档 ID 的 @doc; 前一个字符是字母数字时不算引用 (如邮箱).
const refRe = /(?<![A-Za-z0-9])@[A-Za-z0-9_-]+(?:#[A-Za-z0-9_-]+(?:\/[A-Za-z0-9_-]+)?)?/g

/** splitRefs 把文本节点中的引用切成以引用原文为地址的链接节点. */
function splitRefs(node: MdNode): MdNode[] {
  const text = node.value!
  const out: MdNode[] = []
  let last = 0
  for (const m of text.matchAll(refRe)) {
    if (m.index > last) out.push({ type: "text", value: text.slice(last, m.index) })
    out.push({ type: "link", url: m[0], children: [{ type: "text", value: m[0] }] })
    last = m.index + m[0].length
  }
  if (last === 0) return [node]
  if (last < text.length) out.push({ type: "text", value: text.slice(last) })
  return out
}

/** remarkRefs 把正文中的引用转为链接; 只处理文本节点, 代码与已有链接内的引用保持原样. */
function remarkRefs() {
  const walk = (node: MdNode) => {
    if (node.type === "link" || node.type === "linkReference" || !node.children) return
    node.children = node.children.flatMap((c) => {
      if (c.type === "text") return splitRefs(c)
      walk(c)
      return [c]
    })
  }
  return walk
}

/** MdLink 渲染正文链接: 引用解析到文档时在工作台内跳转并定位区块, 解析不到时按原文显示. */
function MdLink({ node, href = "", children, ...props }: ComponentProps<"a"> & ExtraProps) {
  const { resolve, go } = useNav()
  if (!href.startsWith("@")) {
    return (
      <a href={href} {...props}>
        {children}
      </a>
    )
  }
  const route = resolve(href)
  if (!route) return children
  return (
    <a
      href={routeHash(route)}
      onClick={(e) => {
        // 经 go 跳转才会递增 seq, 重复点击同一引用也会再次定位.
        e.preventDefault()
        go(route)
      }}
    >
      {children}
    </a>
  )
}

const remarkPlugins = [remarkGfm, remarkRefs]
const components: Components = { a: MdLink }

/** Markdown 渲染普通正文; 跳过 HTML, 让 pw: 注释与锚点不显示. */
export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={cn("md", className)}>
      <ReactMarkdown remarkPlugins={remarkPlugins} components={components} skipHtml>
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
              {picked && <Badge>{pick.state === FeedbackState.STAGED ? "已选择" : sentLabels[pick.state]}</Badge>}
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
