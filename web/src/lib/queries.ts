import { useEffect, useState } from "react"
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
  type QueryKey,
} from "@tanstack/react-query"
import { toast } from "sonner"

import { DocKind } from "@/gen/pair/v1/pair_pb"
import { client, errorText, isNotFound } from "@/lib/rpc"

export const keys = {
  status: ["status"],
  docs: ["docs"],
  doc: (kind: DocKind, id: string) => ["doc", kind, id],
  // 带上区块哈希, 区块再次改动后重新取确认原文与当前内容.
  diff: (target: string, hash: string) => ["diff", target, hash],
  feedback: ["feedback"],
  sessions: ["sessions"],
} as const

export function useStatus() {
  return useQuery({ queryKey: keys.status, queryFn: () => client.status({}) })
}

export function useDocs() {
  return useQuery({
    queryKey: keys.docs,
    queryFn: async () => (await client.listDocs({})).docs,
  })
}

export function useDoc(kind: DocKind, id: string) {
  return useQuery({
    queryKey: keys.doc(kind, id),
    queryFn: async () => (await client.getDoc({ id, kind })).doc!,
    retry: (count, err) => !isNotFound(err) && count < 1,
  })
}

export function useSectionDiff(target: string, hash: string) {
  return useQuery({
    queryKey: keys.diff(target, hash),
    queryFn: () => client.sectionDiff({ target }),
  })
}

export function useFeedback() {
  return useQuery({
    queryKey: keys.feedback,
    queryFn: async () => (await client.listFeedback({})).items,
  })
}

export function useSessions() {
  return useQuery({
    queryKey: keys.sessions,
    queryFn: async () => (await client.listSessions({})).sessions,
  })
}

const docKeys: QueryKey[] = [["doc"], keys.docs, ["diff"], keys.status]
const feedbackKeys: QueryKey[] = [keys.feedback, keys.status]

/** useAction 包装写操作: 失败时提示服务端原因, 成功后立即失效相关查询, 不等 SSE 事件. */
function useAction<V, R>(fn: (v: V) => Promise<R>, invalidate: QueryKey[]) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      for (const queryKey of invalidate) void qc.invalidateQueries({ queryKey })
    },
    onError: (err) => toast.error(errorText(err)),
  })
}

export const useConfirm = () =>
  useAction((target: string) => client.confirm({ target }), docKeys)

export const useAckRetracted = () =>
  useAction(
    (v: { target: string; assumptionId: string }) => client.ackRetracted(v),
    docKeys,
  )

export const useStageNote = () =>
  useAction(
    (v: { target: string; assumptionId: string; message: string }) => client.stageNote(v),
    feedbackKeys,
  )

export const useStageDecide = () =>
  useAction(
    (v: { decisionId: string; pick: string; note: string }) => client.stageDecide(v),
    feedbackKeys,
  )

export const useStageRemove = () =>
  useAction((id: string) => client.stageRemove({ id }), feedbackKeys)

export const useTrigger = () =>
  useAction((steer: boolean) => client.trigger({ steer }), feedbackKeys)

/** useEvents 订阅常驻进程的 SSE 事件流并使对应查询失效, 返回连接是否正常. */
export function useEvents(): boolean {
  const qc = useQueryClient()
  const [connected, setConnected] = useState(true)
  useEffect(() => {
    // 断线后浏览器自动重连并携带 Last-Event-ID, 由服务端补发错过的事件.
    const es = new EventSource("/events")
    es.onopen = () => setConnected(true)
    es.onerror = () => setConnected(false)
    es.onmessage = (e: MessageEvent<string>) => {
      const ev = JSON.parse(e.data) as { type: string; ids?: string[] }
      invalidateFor(qc, ev.type, (ev.ids ?? []).map((id) => id.split("#")[0]))
    }
    return () => es.close()
  }, [qc])
  return connected
}

function invalidateFor(qc: QueryClient, type: string, ids: string[]) {
  const hit = (id: unknown) => ids.length === 0 || ids.includes(id as string)
  switch (type) {
    case "doc_changed":
      void qc.invalidateQueries({ queryKey: keys.docs })
      void qc.invalidateQueries({ queryKey: keys.status })
      void qc.invalidateQueries({
        predicate: ({ queryKey: [k, a, b] }) =>
          (k === "doc" && hit(b)) || (k === "diff" && hit((a as string).split("#")[0])),
      })
      return
    case "stage_changed":
    case "trigger":
    case "drained":
    case "resolved":
    case "decision_closed":
      void qc.invalidateQueries({ queryKey: keys.feedback })
      void qc.invalidateQueries({ queryKey: keys.status })
      void qc.invalidateQueries({ queryKey: keys.docs })
      void qc.invalidateQueries({ queryKey: ["doc", DocKind.DECISION] })
      return
    case "session_changed":
      void qc.invalidateQueries({ queryKey: keys.sessions })
      return
    case "branch_changed":
      void qc.invalidateQueries()
  }
}
