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

// 缩放下限: Mermaid 标签默认 16px, 适应画布宽度时不缩到约 11px 以下; 更宽的图表左对齐, 平移查看其余部分.
const MIN_SCALE = 0.7
const FADE = "32px"
// 与画布内边距 p-4 一致.
const INSET = 16

/** fadeMask 在还有内容未显示的一侧渐隐, 提示可以平移. */
function fadeMask(left: boolean, right: boolean) {
  if (!left && !right) return undefined
  const from = left ? `transparent, black ${FADE}` : "black"
  const to = right ? `black calc(100% - ${FADE}), transparent` : "black"
  return `linear-gradient(to right, ${from}, ${to})`
}

/** Mermaid 把图表按容器宽度等比渲染为可缩放平移的画布: 拖动或横向滑动平移, ⌘ 或双指捏合缩放, 点击与 links 同名的节点回调 onLink. */
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
  const [edges, setEdges] = useState({ left: false, right: false })

  // 图表左右边界超出画布内边距的距离. 由布局尺寸与 panzoom 的最新变换算出, 不读 DOM 矩形: 变换要到下一帧才应用.
  const overflow = () => {
    const svg = stage.current?.querySelector("svg")
    const t = pz.current?.getTransform()
    if (!svg || !t) return { left: 0, right: 0 }
    const width = svg.clientWidth
    const x = t.x + t.scale * (INSET + Math.max(0, (stage.current!.clientWidth - 2 * INSET - width) / 2))
    return { left: INSET - x, right: x + t.scale * width - (canvas.current!.clientWidth - INSET) }
  }

  // 还有内容未显示的一侧渐隐.
  const measure = useEffectEvent(() => {
    const o = overflow()
    const left = o.left > 1
    const right = o.right > 1
    setEdges((e) => (e.left === left && e.right === right ? e : { left, right }))
  })

  // 图表按缩放下限仍放不下时整体缩小到内边距以内并垂直居中, 否则回到原始变换.
  const fit = () => {
    const el = stage.current!
    const k = Math.min(1, (canvas.current!.clientWidth - 2 * INSET) / el.querySelector("svg")!.clientWidth)
    pz.current?.zoomAbs(0, 0, k)
    pz.current?.moveTo(INSET * (1 - k), (el.clientHeight * (1 - k)) / 2)
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
      // 宽度随画布收缩, 介于缩放下限与原始宽度之间, 高度按 viewBox 比例; 各类图表的 useMaxWidth 默认值不一, 统一在此设置.
      const svgEl = el.querySelector("svg")!
      const width = svgEl.viewBox.baseVal.width
      svgEl.removeAttribute("height")
      svgEl.setAttribute("width", "100%")
      svgEl.style.maxWidth = `${width}px`
      svgEl.style.minWidth = `${width * MIN_SCALE}px`
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
      pz.current.on("transform", measure)
      measure()
    })()
    return () => {
      alive = false
      pz.current?.dispose()
      pz.current = null
    }
  }, [code, dark])

  // 画布宽度变化时重新判断渐隐; 触控板横向滑动平移图表, 并阻止浏览器把它当作前进后退手势.
  useEffect(() => {
    const c = canvas.current!
    const ro = new ResizeObserver(() => measure())
    ro.observe(c)
    const onWheel = (e: WheelEvent) => {
      if (e.ctrlKey || e.metaKey || Math.abs(e.deltaX) <= Math.abs(e.deltaY)) return
      // 图表放得下时不拦截; 放不下时平移到距画布边缘一个内边距为止.
      const o = overflow()
      const min = -Math.max(0, o.right)
      const max = Math.max(0, o.left)
      if (min === 0 && max === 0) return
      e.preventDefault()
      pz.current?.moveBy(Math.min(max, Math.max(min, -e.deltaX)), 0, false)
    }
    c.addEventListener("wheel", onWheel, { passive: false })
    return () => {
      ro.disconnect()
      c.removeEventListener("wheel", onWheel)
    }
  }, [])

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
        <div style={{ maskImage: fadeMask(edges.left, edges.right) }}>
          <div ref={stage} className="p-4 [&>svg]:mx-auto" />
        </div>
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
