import { useEffect, useEffectEvent, useRef, useState, useSyncExternalStore } from "react"
import type { PanZoom } from "panzoom"
import { MinusIcon, PlusIcon, ScanIcon } from "lucide-react"

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

const MAX_HEIGHT = 520
const MIN_SCALE = 0.75
let renderSeq = 0

/** Mermaid 把图表渲染为可缩放平移的画布: 拖动平移, ⌘ 或双指捏合缩放, 点击与 links 同名的节点回调 onLink. */
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
  const size = useRef({ w: 0, h: 0 })
  const linkOf = useEffectEvent((el: Element) => nodeTarget(el, links))
  const follow = useEffectEvent((id: string) => onLink(id))
  const dark = useDark()
  const [height, setHeight] = useState(0)
  const [error, setError] = useState("")

  // fit 把整张图缩放到画布内居中, 图太宽时保持可读的最小比例并靠左, 由用户平移查看.
  const fit = () => {
    const cw = canvas.current!.clientWidth
    const { w, h } = size.current
    const scale = Math.max(MIN_SCALE, Math.min(1, (cw - 32) / w, (MAX_HEIGHT - 32) / h))
    const ch = Math.min(h * scale + 32, MAX_HEIGHT)
    setHeight(ch)
    pz.current!.zoomAbs(0, 0, scale)
    pz.current!.moveTo(Math.max(16, (cw - w * scale) / 2), Math.max(16, (ch - h * scale) / 2))
  }

  const zoom = (factor: number) => {
    const c = canvas.current!
    pz.current!.smoothZoom(c.clientWidth / 2, c.clientHeight / 2, factor)
  }

  useEffect(() => {
    let alive = true
    const id = `mermaid-${++renderSeq}`
    const resize = new ResizeObserver(() => pz.current && fit())
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
      const svgEl = el.querySelector("svg")!
      const { width, height } = svgEl.viewBox.baseVal
      size.current = { w: width, h: height }
      svgEl.style.maxWidth = "none"
      svgEl.setAttribute("width", String(width))
      svgEl.setAttribute("height", String(height))
      for (const g of svgEl.querySelectorAll("g.node, g.cluster")) {
        if (linkOf(g)) g.classList.add("pw-link")
      }
      setError("")
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
      // 观察开始时与画布尺寸变化时都会适应画布.
      resize.observe(canvas.current!)
    })()
    return () => {
      alive = false
      resize.disconnect()
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
        className="mermaid-canvas group/canvas relative cursor-grab overflow-hidden rounded-lg border bg-muted/30 active:cursor-grabbing"
        style={{ height: height || 160 }}
      >
        {!height && <Skeleton className="absolute inset-3" />}
        <div ref={stage} className="w-max" />
        {height > 0 && (
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
