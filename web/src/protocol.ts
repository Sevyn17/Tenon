// 应用接口层协议的 TypeScript 类型（对应 kernel/appapi，协议版本见 PROTOCOL）。
// 命令名清单 COMMANDS 由 Go 测试 kernel/apps/desktop/service_test.go（TestProtocolCommands） 校验与 appapi.Commands() 一致；
// 改 appapi 命令时同步改这里。

export const PROTOCOL = 1;

export const COMMANDS = [
  "artifact.get",
  "hello",
  "mode.get",
  "mode.list",
  "mode.set",
  "mode.show",
  "packages.list",
  "plugins.list",
  "plugins.set",
  "session.answer",
  "session.cancel",
  "session.compact",
  "session.confirm",
  "session.entries",
  "session.fork",
  "session.max_steps",
  "session.open",
  "session.reply",
  "session.rewind",
  "session.send",
  "session.stats",
  "session.steer",
  "sessions.list",
  "trace.explain",
] as const;

export type Command = (typeof COMMANDS)[number];

// 错误码（appapi/errors.go）。
export type ErrorCode =
  | "bad_request"
  | "not_found"
  | "busy"
  | "not_idle"
  | "conflict"
  | "unauthorized"
  | "unsupported"
  | "internal"
  | "need_confirm";

export interface ApiErrorBody {
  code: ErrorCode | string;
  message: string;
  retry?: boolean;
}

export class ApiError extends Error {
  code: string;
  retry: boolean;
  constructor(b: ApiErrorBody) {
    super(b.message);
    this.code = b.code;
    this.retry = !!b.retry;
  }
}

// ---- 事件 ----

// bus.Envelope。
export interface Envelope<T = unknown> {
  v: number;
  seq: number;
  at: number;
  session: string;
  kind: string;
  data?: T;
}

// 持久事件：entry / rewind；易失事件：其余。resync 为桌面端订阅重连时的提示（需要按持久数据补齐）。
export const EV = {
  entry: "entry",
  rewind: "rewind",
  turnStart: "turn_start",
  turnEnd: "turn_end",
  turnDenied: "turn_denied",
  humanRequested: "human_requested",
  textDelta: "text_delta",
  textReset: "text_reset",
  toolCall: "tool_call",
  toolResult: "tool_result",
  ask: "ask",
  notice: "notice",
  resync: "resync",
} as const;

export interface EntryEvent { idx: number; entry: Entry }
export interface RewindEvent { to: number }
export interface TextDeltaEvent { text: string }
export interface ToolCallEvent { call_id: string; name: string; args: string }
export interface ToolResultEvent { call_id: string; preview: string; error?: boolean }
export interface AskEvent { ask_id: string; question: string; options?: string[] }
export interface NoticeEvent { text: string }
export interface HumanRequestedEvent { call_id: string; tool: string; reason?: string; args: string }
export interface TurnEndEvent { reason: string }
export interface TurnDeniedEvent { unconsumed?: unknown }

// ---- 会话日志条目（runtime/session/types.go；内层结构无 json tag，键为 Go 字段名） ----

export interface Part {
  kind: "text" | "media" | "data" | "unknown" | string;
  text?: string;
  media?: { URI?: string; MIME?: string; Bytes?: string; SHA256?: string };
  data?: unknown;
  raw?: unknown;
}
export type Content = Part[] | null;

export interface ToolCall { ID: string; WireID?: string; Name: string; Args: unknown }
export interface Usage { Input: number; Output: number; CacheRead: number; CacheWrite: number; Source?: string }

export interface Item {
  kind: "user" | "assistant" | "tool_result" | "compaction" | "unknown" | string;
  user?: { Content: Content };
  assistant?: {
    Reasoning?: { Text?: string | null; Opaque?: unknown }[] | null;
    Content: Content;
    ToolCalls?: ToolCall[] | null;
    Model?: string;
    Usage?: Usage;
    Interrupted?: boolean;
  };
  tool_result?: { CallID: string; Output: Content; Status: { State: string; CancelReason?: string } };
  compaction?: { Upto: number; Summary: Content };
  unknown?: { Raw: unknown };
}

export interface Entry { v: number; id: string; turn: number; at: number; meta?: unknown; item: Item }

// ---- 命令参数 / 结果 ----

export interface HelloArgs { client: string; version: number }
export interface HelloResult { protocol: number; server: string; model: string; workdir: string; trusted: boolean }

export interface SessionInfo {
  id: string;
  title?: string;
  preview?: string;
  run_state?: string;
  origin?: string;
  parent?: string;
  updated_at?: number;
}
export interface ListArgs { cursor?: number; limit?: number; all?: boolean }
export interface ListResult { items: SessionInfo[] | null; next: number }

export interface SessionArgs { session: string }
export interface OpenResult { id: string; run_state: string; log_len: number; base: number; mode?: string }

export interface EntriesArgs { session: string; from?: number; to?: number; tail?: number }
export interface EntryRow { idx: number; entry: Entry }
export interface EntriesResult { entries: EntryRow[] | null; base: number; log_len: number }

export interface SendArgs { session: string; text: string; item_id?: string }
export interface TextArgs { session: string; text: string }

export type Decision = "once" | "session" | "deny";
export interface ConfirmArgs { session: string; call_id: string; decision: Decision; tool?: string; args?: unknown }
export interface ConfirmResult { rules?: string[] }
export interface AnswerArgs { ask_id: string; text: string }

export interface RewindArgs { session: string; turns?: number }
export interface RewindResult { to: number }
export interface CompactResult { summary: string; upto: number; tokens_before?: number; tokens_after?: number }
export interface ForkArgs { session: string; to: string; upto?: number; origin?: "fork" | "side" }
export interface ForkResult { id: string }

export interface Stats {
  attempts: number;
  reported: number;
  missing: number;
  errors: number;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  hit_rate: number; // < 0 = 无数据
  tool_calls: number;
  compacts: number;
  composition?: unknown;
  last_request?: unknown;
}
export interface MaxStepsArgs { session: string; n?: number }
export interface MaxStepsResult { n: number }

export interface ModeInfo { id: string; label: string; builtin: boolean; project: boolean; widened?: string[] }
export interface ModeState { mode: string; label: string; sandbox: string }
export interface ModeItem { item: string; title: string; value: string; source: string; bounded: boolean }
export interface ModeArgs { session: string; mode?: string; confirm?: boolean }
export interface ShowArgs { session?: string; mode?: string }

export interface Field {
  key: string;
  type: string;
  default?: string;
  title?: string;
  description?: string;
  group?: string;
  widget?: string;
  override?: boolean;
}
export interface PluginInfo {
  name: string;
  on: boolean;
  default: boolean;
  source?: string;
  effects?: unknown;
  schema?: Field[] | null;
}
export interface PackageInfo {
  name: string;
  version: string;
  description: string;
  scope: "user" | "project";
  source: string;
  contents: string;
}
export interface PluginsSetArgs { name: string; enabled?: boolean; config?: Record<string, unknown>; scope: "user" | "project" }

// 命令 → 参数 / 结果类型映射，transport.call 用它做类型检查。
export interface CommandMap {
  hello: [HelloArgs, HelloResult];
  "sessions.list": [ListArgs, ListResult];
  "session.open": [SessionArgs, OpenResult];
  "session.entries": [EntriesArgs, EntriesResult];
  "session.send": [SendArgs, object];
  "session.steer": [TextArgs, object];
  "session.cancel": [SessionArgs, object];
  "session.confirm": [ConfirmArgs, ConfirmResult];
  "session.answer": [AnswerArgs, object];
  "session.rewind": [RewindArgs, RewindResult];
  "session.compact": [SessionArgs, CompactResult];
  "session.fork": [ForkArgs, ForkResult];
  "session.stats": [SessionArgs, Stats];
  "session.max_steps": [MaxStepsArgs, MaxStepsResult];
  "mode.list": [Record<string, never>, ModeInfo[]];
  "mode.get": [ModeArgs, ModeState];
  "mode.set": [ModeArgs, ModeState];
  "mode.show": [ShowArgs, ModeItem[]];
  "packages.list": [Record<string, never>, PackageInfo[]];
  "plugins.list": [Record<string, never>, PluginInfo[] | null];
  "plugins.set": [PluginsSetArgs, object];
  "artifact.get": [{ session: string; ref: string }, unknown];
}
