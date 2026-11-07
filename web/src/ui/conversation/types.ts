// 会话组件的视图类型。@ybase/ui 不依赖协议客户端（分层规则），所以这里定义组件需要的最小形状；
// wire.gen.ts 的 Entry / Interaction 等在结构上满足它们，shell 直接传进来即可，别的协议也可以自己适配。

import type { ReactNode } from "react";

export interface UIPart {
  kind: string;
  text?: string;
  media?: { ref: string; mime: string; size?: number };
}

export interface UIToolCall {
  id: string;
  name: string;
  args: unknown;
}

export interface UIUsage {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
}

/** 一条条目（只列组件用到的字段）。 */
export interface UIEntry {
  id: string;
  idx: number;
  turn: number;
  at: number;
  item: {
    kind: string; // user / assistant / tool_result / compaction / context / unknown
    content?: UIPart[];
    reasoning?: { text: string }[];
    tool_calls?: UIToolCall[];
    model?: string;
    usage?: UIUsage;
    interrupted?: boolean;
    call_id?: string;
    output?: UIPart[];
    status?: string;
    truncated?: boolean;
    output_ref?: string;
    upto?: number;
    summary?: UIPart[];
    source?: string;
    text?: string;
  };
}

/** 正在流入的一段输出。 */
export interface UIStream {
  stream_id: string;
  entry_id: string;
  part: string; // text / reasoning
  text: string;
  startedAt: number;
}

/** 一个工具调用的展示状态（条目里的调用与正在进行的调用合成后）。 */
export type ToolPhase = "args" | "running" | "awaiting" | "ok" | "error" | "cancelled";

export interface UITool {
  call_id: string;
  name: string;
  args: string; // JSON 文本（流入中可能是半截）
  phase: ToolPhase;
  output?: string; // 结果文本或预览
  truncated?: boolean;
  output_ref?: string;
  /** 执行中的输出尾部（替换显示，结束后不再给）。 */
  live?: string;
  startedAt?: number;
  endedAt?: number;
}

/** 一次交互（等人回答）。 */
export interface UIInteraction {
  id: string;
  session: string;
  kind: string;
  title: string;
  approval?: { tool: string; args?: unknown; reason?: string; session_rules?: string[]; blocked?: string[] };
  question?: { text: string; options?: string[] };
  deadline?: number;
  opened_at: number;
}

export type UIResponse = { approve?: boolean; scope?: string; text?: string; value?: unknown };

/** 回合结束信息（尾巴的异常状态行用）。 */
export interface UITurnEnd {
  turn: number;
  kind: string; // done / cancelled / max_steps / denied / failed
  code?: string;
  message?: string;
}

/** 消息上的一个操作（悬停条或 ⋯ 菜单里）。 */
export interface UIAction {
  id: string;
  label: string;
  icon?: ReactNode;
  placement: "hover" | "menu";
  keybind?: string;
  danger?: boolean;
  disabled?: boolean;
  reason?: string;
  run: () => void;
}
