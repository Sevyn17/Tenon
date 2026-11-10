// 会话状态的纯 reducer（protocol 2，docs/01 - 设计文档/06 - 应用协议契约.md 10.2、10.5）。不做 I/O：SessionStore 负责取快照、订阅、
// 续订，把收到的快照、日志页与事件交给这里合成界面要的状态。纯函数，便于测试与在别的框架里复用。
//
// 合成规则：
//   - 条目按 idx 排序去重；log.rewound 截到 to；log.compacted 前移 base。
//   - 流式：message.started 建流（带 entry_id、attempt），delta 追加，discarded 丢掉；entry.appended 到达时删掉
//     entry_id 等于条目 id 的流，以条目为准。
//   - 工具：tool.started / args_delta / running / ended 按 call_id 合成；对应的 tool_result 条目落库后删掉。
//   - turn.ended / turn.rejected：清掉本回合残留的流与工具。

import type {
  Entry,
  Envelope,
  EntryAppended,
  SessionUpdated,
  LogCompacted,
  LogRewound,
  MessageDelta,
  MessageStarted,
  Mode,
  SessionSnapshot,
  SettingsChanged,
  StateChanged,
  StreamRef,
  ToolArgsDelta,
  ToolEnded,
  ToolProgress,
  ToolRunning,
  ToolStarted,
  TurnEnded,
  TurnRejected,
  TurnStarted,
} from "./wire.gen";

export interface StreamState {
  stream_id: string;
  entry_id: string;
  part: string; // text / reasoning
  attempt: number;
  text: string;
  startedAt: number;
}

export type ToolPhase = "args" | "running" | "ended";

export interface ToolState {
  call_id: string;
  name: string;
  entry_id: string;
  args: string; // 参数文本（流入中的半截 JSON，或完整参数的 JSON）
  phase: ToolPhase;
  status?: string; // ended：ok / error / cancelled
  preview?: string;
  output_ref?: string;
  /** 执行中的输出尾部（tool.progress，最新一段，替换显示）；结束后清掉。 */
  live?: string;
  startedAt: number;
  endedAt?: number;
}

export interface TurnInfo {
  turn: number;
  inputs: string[];
  startedAt: number;
}

export type LoadStatus = "idle" | "loading" | "ready" | "error";

export interface SessionState {
  id: string;
  status: LoadStatus;
  error?: string;
  title: string;
  pinned: boolean;
  origin: string;
  parent?: string;
  runState: "idle" | "running";
  mode?: Mode;
  maxSteps: number;
  /** 会话的模型（别名或 provider/model）；空 = 快照里没给。 */
  model: string;
  /** 推理强度（low / medium / high）；空 = 厂商缺省。 */
  reasoning: string;
  base: number; // 压缩点
  len: number; // 日志长度（已知的最大）
  cursor: number; // 最后收到的持久游标
  entries: Entry[];
  streams: StreamState[];
  tools: ToolState[];
  turn: TurnInfo | null; // 进行中的回合
  lastEnd: TurnEnded | null; // 最近一次回合结束（尾巴的异常状态行用）
  rejected: TurnRejected | null;
  closed?: string; // 会话已关闭的原因
}

export function initialState(id: string): SessionState {
  return {
    id,
    status: "idle",
    title: "",
    pinned: false,
    origin: "",
    runState: "idle",
    maxSteps: 0,
    model: "",
    reasoning: "",
    base: 0,
    len: 0,
    cursor: 0,
    entries: [],
    streams: [],
    tools: [],
    turn: null,
    lastEnd: null,
    rejected: null,
  };
}

/** 快照：除 live 外都来自已提交数据；live 给晚连上的客户端看正在输出的内容。 */
export function applySnapshot(s: SessionState, snap: SessionSnapshot, now = Date.now()): SessionState {
  const live = snap.live;
  return {
    ...s,
    title: snap.title,
    pinned: !!snap.pinned,
    origin: snap.origin,
    parent: snap.parent,
    runState: snap.run_state === "running" ? "running" : "idle",
    mode: snap.mode,
    maxSteps: snap.settings.max_steps,
    model: snap.settings.model ?? "",
    reasoning: snap.settings.reasoning ?? "",
    base: snap.log.base,
    len: Math.max(s.len, snap.log.len),
    cursor: snap.cursor,
    streams: (live?.streams ?? []).map((x) => ({ ...x, attempt: 1, startedAt: now })),
    tools: (live?.tools ?? []).map((t) => ({
      call_id: t.call_id,
      name: t.name,
      entry_id: "",
      args: t.args === undefined ? "" : JSON.stringify(t.args),
      phase: t.args === undefined ? "args" : "running",
      startedAt: now,
    })),
    turn: snap.run_state === "running" ? (s.turn ?? { turn: live?.turn ?? 0, inputs: [], startedAt: now }) : null,
  };
}

/** 合并一页日志（尾部或更早的一页）。 */
export function mergeEntries(s: SessionState, page: Entry[], base?: number, len?: number): SessionState {
  const entries = mergeSorted(s.entries, page);
  return {
    ...s,
    entries,
    base: base ?? s.base,
    len: Math.max(s.len, len ?? 0, entries.length ? entries[entries.length - 1].idx + 1 : 0),
  };
}

function mergeSorted(a: Entry[], b: Entry[]): Entry[] {
  if (b.length === 0) return a;
  const m = new Map<number, Entry>();
  for (const e of a) m.set(e.idx, e);
  for (const e of b) m.set(e.idx, e);
  return [...m.values()].sort((x, y) => x.idx - y.idx);
}

/** 是否还有更早的条目没取（第一条之前还有日志）。 */
export function hasOlder(s: SessionState): boolean {
  return s.entries.length > 0 && s.entries[0].idx > 0;
}

/** 一条事件 → 新状态。不属于本会话的事件原样返回。 */
export function reduce(s: SessionState, ev: Envelope, now = Date.now()): SessionState {
  if (ev.session !== s.id) return s;
  if (ev.cursor && ev.cursor > s.cursor) s = { ...s, cursor: ev.cursor };
  switch (ev.kind) {
    case "entry.appended": {
      const e = (ev.data as EntryAppended).entry;
      const st = mergeEntries(s, [e]);
      return {
        ...st,
        streams: s.streams.filter((x) => x.entry_id !== e.id),
        tools: e.item.kind === "tool_result" ? s.tools.filter((t) => t.call_id !== e.item.call_id) : s.tools,
      };
    }
    case "log.rewound": {
      const to = (ev.data as LogRewound).to;
      return { ...s, entries: s.entries.filter((e) => e.idx < to), len: to, base: Math.min(s.base, to) };
    }
    case "log.compacted":
      return { ...s, base: (ev.data as LogCompacted).upto };
    case "session.state_changed": {
      const running = (ev.data as StateChanged).run_state === "running";
      return { ...s, runState: running ? "running" : "idle", turn: running ? s.turn : null };
    }
    case "session.settings_changed": {
      const st = (ev.data as SettingsChanged).settings;
      return { ...s, maxSteps: st.max_steps, model: st.model ?? s.model, reasoning: st.reasoning ?? "" };
    }
    case "session.mode_changed":
      return { ...s, mode: ev.data as Mode };
    case "session.updated": {
      const d = ev.data as SessionUpdated;
      return { ...s, title: d.title, pinned: d.pinned };
    }
    case "session.closed":
      return { ...s, closed: (ev.data as { reason?: string }).reason ?? "closed" };
    case "turn.started": {
      const d = ev.data as TurnStarted;
      return { ...s, runState: "running", turn: { turn: d.turn, inputs: d.inputs ?? [], startedAt: now }, lastEnd: null, rejected: null };
    }
    case "turn.ended":
      return { ...s, runState: "idle", turn: null, streams: [], tools: [], lastEnd: ev.data as TurnEnded };
    case "turn.rejected":
      return { ...s, runState: "idle", turn: null, streams: [], tools: [], rejected: ev.data as TurnRejected };
    case "message.started": {
      const d = ev.data as MessageStarted;
      return {
        ...s,
        streams: [...s.streams.filter((x) => x.stream_id !== d.stream_id), { ...d, text: "", startedAt: now }],
      };
    }
    case "message.delta": {
      const d = ev.data as MessageDelta;
      return { ...s, streams: s.streams.map((x) => (x.stream_id === d.stream_id ? { ...x, text: x.text + d.text } : x)) };
    }
    case "message.discarded": {
      const d = ev.data as StreamRef;
      return { ...s, streams: s.streams.filter((x) => x.stream_id !== d.stream_id) };
    }
    case "tool.started": {
      const d = ev.data as ToolStarted;
      if (s.tools.some((t) => t.call_id === d.call_id)) return s;
      return {
        ...s,
        tools: [...s.tools, { call_id: d.call_id, name: d.name, entry_id: d.entry_id, args: "", phase: "args", startedAt: now }],
      };
    }
    case "tool.args_delta": {
      const d = ev.data as ToolArgsDelta;
      return { ...s, tools: s.tools.map((t) => (t.call_id === d.call_id ? { ...t, args: t.args + d.text } : t)) };
    }
    case "tool.running": {
      const d = ev.data as ToolRunning;
      const args = JSON.stringify(d.args ?? {});
      const exists = s.tools.some((t) => t.call_id === d.call_id);
      const tools = exists
        ? s.tools.map((t) => (t.call_id === d.call_id ? { ...t, args, phase: "running" as const } : t))
        : [...s.tools, { call_id: d.call_id, name: "", entry_id: "", args, phase: "running" as const, startedAt: now }];
      return { ...s, tools };
    }
    case "tool.progress": {
      const d = ev.data as ToolProgress;
      if (d.text === undefined) return s;
      return { ...s, tools: s.tools.map((t) => (t.call_id === d.call_id && t.phase === "running" ? { ...t, live: d.text } : t)) };
    }
    case "tool.ended": {
      const d = ev.data as ToolEnded;
      return {
        ...s,
        tools: s.tools.map((t) =>
          t.call_id === d.call_id
            ? { ...t, phase: "ended", status: d.status, preview: d.preview, output_ref: d.output_ref, endedAt: now, live: undefined }
            : t,
        ),
      };
    }
  }
  return s;
}

// ---- 派生：按回合分组（界面渲染与回合尾巴用） ----

export interface TurnGroup {
  turn: number;
  entries: Entry[];
  /** 回合是否已结束（后面有新回合，或会话空闲且不是进行中的回合）。 */
  done: boolean;
}

/** 把条目按回合号分组（回合号相同、连续的条目为一组）。 */
export function groupTurns(s: SessionState): TurnGroup[] {
  const out: TurnGroup[] = [];
  for (const e of s.entries) {
    const last = out[out.length - 1];
    if (last && last.turn === e.turn) last.entries.push(e);
    else out.push({ turn: e.turn, entries: [e], done: true });
  }
  const last = out[out.length - 1];
  if (last && s.turn && s.turn.turn === last.turn) last.done = false;
  return out;
}

/** 某个调用 id 的工具结果条目（工具卡显示结果用）。 */
export function resultIndex(s: SessionState): Map<string, Entry> {
  const m = new Map<string, Entry>();
  for (const e of s.entries) if (e.item.kind === "tool_result" && e.item.call_id) m.set(e.item.call_id, e);
  return m;
}
