import { Code, ConnectError, createClient } from "@connectrpc/connect"
import { createConnectTransport } from "@connectrpc/connect-web"

import { DocKind, FeedbackState, PairService, type Block } from "@/gen/pair/v1/pair_pb"

/** 同源调用常驻进程, 认证由会话 Cookie 完成. */
export const client = createClient(
  PairService,
  createConnectTransport({ baseUrl: window.location.origin }),
)

export type KindName = "module" | "flow" | "decision"

export const kindNames: Record<KindName, DocKind> = {
  module: DocKind.MODULE,
  flow: DocKind.FLOW,
  decision: DocKind.DECISION,
}

export function kindName(kind: DocKind): KindName {
  return kind === DocKind.DECISION ? "decision" : kind === DocKind.FLOW ? "flow" : "module"
}

/** errorText 取出面向用户的错误文案. */
export function errorText(err: unknown): string {
  return ConnectError.from(err).rawMessage
}

export function isNotFound(err: unknown): boolean {
  return ConnectError.from(err).code === Code.NotFound
}

/** anchorId 是区块与容器在页面中的锚点, 文件内共享命名空间. */
export function anchorId(id: string): string {
  return `pw-${id}`
}

/** hasBlock 递归判断正文是否含某类预设组件. */
export function hasBlock(blocks: Block[], kind: Block["kind"]): boolean {
  return blocks.some((b) => b.kind === kind || b.items.some((i) => hasBlock(i.blocks, kind)))
}

/** progressLabels 是问题 target 闭环进度的文案. */
export const progressLabels: Record<string, string> = {
  pending: "待更新",
  updated: "已更新",
  no_change: "无需改动",
}

/** sentLabels 是已发送反馈在 Agent 读取前后的文案. */
export const sentLabels: Partial<Record<FeedbackState, string>> = {
  [FeedbackState.TRIGGERED]: "已发送",
  [FeedbackState.DRAINED]: "已读取",
}
