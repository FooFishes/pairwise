import { useEffect, useEffectEvent, useRef, useState, useSyncExternalStore } from "react"
import type { PanZoom } from "panzoom"
import { MinusIcon, PlusIcon, ScanIcon } from "lucide-react"
import { cn } from "cn"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"

const darkQuery = window.matchMedia("(prefers-color-scheme: dark)")

function useDark() {
  return useSyncExternalStore(
    (cb) => {
      darkQuery.addEventListener("change", cb)
      return () => darkQuery.removeEventListener("change", cb)
    },
    () => darkQuery.matches,
  )
}

// 流程图与状态图节点的 DOM id 形如 [<前缀>-]flowchart-<节点 ID>-<序号>.
const nodeDomId = /(?:^|-)(?:flowchart|state)-(.+)-\d+$/

/** nodeTarget 返回点击位置所在节点中与 ids 同名的那个 ID. */
function nodeTarget(el: Element, ids: string[]): string | undefined {
  const g = el.closest("g.node, g.cluster")
  if (!g) return
  const names = [g.getAttribute("data-id"), g.id.match(nodeDomId)?.[1]]
  return ids.find((id) => names.includes(id))
}

let renderSeq = 0

/** Mermaid 把图表按容器宽度等比渲染为可缩放平移的画布: 拖动平移, ⌘ 或双指捏合缩放, 点击与 links 同名的节点回调 onLink. */
export function Mermaid({
  code,
  links,
  onLink,
}: {
  code: string
  links: string[]
  onLink: (id: string) => void
}) {
  const canvas = useRef<HTMLDivElement>(null)
  const stage = useRef<HTMLDivElement>(null)
  const pz = useRef<PanZoom | null>(null)
  const linkOf = useEffectEvent((el: Element) => nodeTarget(el, links))
  const follow = useEffectEvent((id: string) => onLink(id))
  const dark = useDark()
  const [ready, setReady] = useState(false)
  const [error, setError] = useState("")

  // 未缩放时图表由 CSS 适应画布宽度, 适应画布即回到原始变换.
  const fit = () => {
    pz.current?.zoomAbs(0, 0, 1)
    pz.current?.moveTo(0, 0)
  }

  const zoom = (factor: number) => {
    const c = canvas.current!
    pz.current?.smoothZoom(c.clientWidth / 2, c.clientHeight / 2, factor)
  }

  useEffect(() => {
    let alive = true
    const id = `mermaid-${++renderSeq}`
    void (async () => {
      const [{ default: mermaid }, { default: panzoom }] = await Promise.all([
        import("mermaid"),
        import("panzoom"),
      ])
      mermaid.initialize({
        startOnLoad: false,
        theme: dark ? "dark" : "neutral",
        fontFamily: getComputedStyle(document.body).fontFamily,
        suppressErrorRendering: true,
      })
      let svg: string
      try {
        ;({ svg } = await mermaid.render(id, code))
      } catch (err) {
        if (alive) setError(err instanceof Error ? err.message : String(err))
        return
      }
      if (!alive) return
      const el = stage.current!
      el.innerHTML = svg
      // 宽度随画布收缩但不超过原始宽度, 高度按 viewBox 比例; 各类图表的 useMaxWidth 默认值不一, 统一在此设置.
      const svgEl = el.querySelector("svg")!
      svgEl.removeAttribute("height")
      svgEl.setAttribute("width", "100%")
      svgEl.style.maxWidth = `${svgEl.viewBox.baseVal.width}px`
      for (const g of svgEl.querySelectorAll("g.node, g.cluster")) {
        if (linkOf(g)) g.classList.add("pw-link")
      }
      setError("")
      setReady(true)
      // 每次渲染重建实例, 新实例以原始变换开始, 换主题后同样回到适应画布.
      pz.current = panzoom(el, {
        minZoom: 0.2,
        maxZoom: 5,
        zoomDoubleClickSpeed: 1,
        beforeMouseDown: (e) => (e.target as Element).closest("button") !== null,
        // 普通滚轮留给页面滚动, 只有 ⌘ 滚轮或触控板捏合 (ctrlKey) 才缩放.
        beforeWheel: (e) => !(e.ctrlKey || e.metaKey),
        onClick: (e) => {
          const target = linkOf(e.target as Element)
          if (target) follow(target)
        },
      })
    })()
    return () => {
      alive = false
      pz.current?.dispose()
      pz.current = null
    }
  }, [code, dark])

  return (
    <>
      {error && (
        <div className="rounded-lg border px-3 py-2" title={error}>
          <p className="text-xs text-destructive">图表无法渲染</p>
          <pre className="mt-1.5 font-mono text-[12px] whitespace-pre-wrap text-muted-foreground">{code}</pre>
        </div>
      )}
      {/* 渲染失败时画布只隐藏不卸载, 图表源码修正后仍可在原处重新渲染. */}
      <div
        ref={canvas}
        hidden={!!error}
        className={cn(
          "mermaid-canvas group/canvas relative cursor-grab overflow-hidden rounded-lg border bg-muted/30 active:cursor-grabbing",
          !ready && "h-40",
        )}
      >
        {!ready && <Skeleton className="absolute inset-3" />}
        <div ref={stage} className="p-4 [&>svg]:mx-auto" />
        {ready && (
          <div className="absolute right-2 bottom-2 flex gap-0.5 rounded-md border bg-background/90 p-0.5 opacity-0 shadow-xs backdrop-blur transition-opacity group-hover/canvas:opacity-100">
            <Button variant="ghost" size="icon-xs" aria-label="缩小" onClick={() => zoom(0.8)}>
              <MinusIcon />
            </Button>
            <Button variant="ghost" size="icon-xs" aria-label="放大" onClick={() => zoom(1.25)}>
              <PlusIcon />
            </Button>
            <Button variant="ghost" size="icon-xs" aria-label="适应画布" onClick={fit}>
              <ScanIcon />
            </Button>
          </div>
        )}
      </div>
    </>
  )
}
