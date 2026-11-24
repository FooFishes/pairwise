import { createContext, useCallback, useContext, useEffect, useState } from "react"

import { DocKind, type Doc, type DocSummary, type Feedback } from "@/gen/pair/v1/pair_pb"
import { kindName, kindNames, type KindName } from "@/lib/rpc"

/** Route 是当前打开的文档与要定位的区块, 保存在 location.hash 中. */
export type Route = {
  kind: KindName
  id: string
  sec?: string
  // seq 在每次主动跳转时递增, 重复点击同一目标也会再次定位.
  seq?: number
}

function parseHash(): Route | null {
  const [kind, id, sec] = location.hash.replace(/^#\/?/, "").split("/").map(decodeURIComponent)
  if (!Object.hasOwn(kindNames, kind) || !id) return null
  return { kind: kind as KindName, id, sec: sec || undefined }
}

/** routeHash 是路由在 location.hash 中的写法. */
export function routeHash(r: Route): string {
  return "#/" + [r.kind, r.id, r.sec].filter(Boolean).map((s) => encodeURIComponent(s!)).join("/")
}

let seq = 0

export function useHashRoute() {
  const [route, setRoute] = useState(parseHash)
  useEffect(() => {
    const onChange = () => setRoute(parseHash())
    window.addEventListener("hashchange", onChange)
    return () => window.removeEventListener("hashchange", onChange)
  }, [])
  const go = useCallback((r: Route, replace = false) => {
    const hash = routeHash(r)
    if (replace) history.replaceState(null, "", hash)
    else if (location.hash !== hash) history.pushState(null, "", hash)
    setRoute({ ...r, seq: ++seq })
  }, [])
  return [route, go] as const
}

export type Nav = {
  route: Route | null
  go: (r: Route, replace?: boolean) => void
  docs: DocSummary[]
}

export const NavContext = createContext<Nav | null>(null)

export function useNav() {
  const nav = useContext(NavContext)!
  /** resolve 把 doc、doc#sec 或 doc#container/item 形式的定位路径解析为路由, 文档不存在时返回 undefined. */
  const resolve = useCallback(
    (target: string): Route | undefined => {
      const [id, rest = ""] = target.replace(/^@/, "").split("#")
      const doc = nav.docs.find((d) => d.id === id && d.kind !== DocKind.DECISION)
      return doc && { kind: kindName(doc.kind), id, sec: rest.split("/")[0] || undefined }
    },
    [nav.docs],
  )
  const openTarget = useCallback(
    (target: string) => {
      const r = resolve(target)
      if (r) nav.go(r)
    },
    [nav, resolve],
  )
  return { ...nav, resolve, openTarget }
}

/** DocCtx 是文档视图内各组件共享的当前文档与当前分支的反馈条目. */
export type DocCtx = { doc: Doc; feedback: Feedback[] }

export const DocContext = createContext<DocCtx | null>(null)

export const useDocCtx = () => useContext(DocContext)!
