import { describe, expect, it } from "vitest";
import { applySnapshot, groupTurns, initialState, mergeEntries, reduce, turnEnd, type SessionState } from "./session-reducer";
import type { Entry, Envelope, SessionSnapshot } from "./wire.gen";

const ev = (kind: string, data: unknown, cursor?: number): Envelope => ({ v: 2, session: "s", kind, at: 0, cursor, data });

const entry = (idx: number, turn: number, kind = "assistant", extra: Partial<Entry["item"]> = {}): Entry => ({
  id: `e${idx}`,
  idx,
  turn,
  at: 0,
  item: { kind, ...extra },
});

const snap = (over: Partial<SessionSnapshot> = {}): SessionSnapshot => ({
  id: "s",
  origin: "new",
  title: "",
  run_state: "idle",
  settings: { max_steps: 0 },
  log: { base: 0, len: 0 },
  interactions: [],
  cursor: 5,
  ...over,
});

const run = (s: SessionState, ...evs: Envelope[]) => evs.reduce((x, e) => reduce(x, e, 1000), s);

describe("session reducer", () => {
  it("快照带 live：晚连上的客户端立刻看到正在输出的内容", () => {
    const s = applySnapshot(
      initialState("s"),
      snap({
        run_state: "running",
        live: { turn: 2, streams: [{ stream_id: "st", entry_id: "a", part: "text", text: "半句" }], tools: [] },
      }),
    );
    expect(s.runState).toBe("running");
    expect(s.cursor).toBe(5);
    expect(s.streams[0].text).toBe("半句");
    expect(s.turn?.turn).toBe(2);
  });

  it("流式按 stream_id 累积，entry.appended 到达后以条目为准", () => {
    let s = run(
      initialState("s"),
      ev("message.started", { stream_id: "st", entry_id: "e1", part: "text", attempt: 1 }),
      ev("message.delta", { stream_id: "st", text: "你" }),
      ev("message.delta", { stream_id: "st", text: "好" }),
    );
    expect(s.streams[0].text).toBe("你好");
    s = run(s, ev("entry.appended", { entry: entry(1, 1) }, 7));
    expect(s.streams).toHaveLength(0);
    expect(s.entries.map((e) => e.idx)).toEqual([1]);
    expect(s.cursor).toBe(7);
  });

  it("模型重试：旧段 discarded，新段 attempt 加一、同一 entry_id", () => {
    const s = run(
      initialState("s"),
      ev("message.started", { stream_id: "a", entry_id: "e1", part: "text", attempt: 1 }),
      ev("message.delta", { stream_id: "a", text: "错" }),
      ev("message.discarded", { stream_id: "a" }),
      ev("message.started", { stream_id: "b", entry_id: "e1", part: "text", attempt: 2 }),
    );
    expect(s.streams.map((x) => [x.stream_id, x.attempt])).toEqual([["b", 2]]);
  });

  it("model.retrying 记到 retry；message.started 后清掉", () => {
    let s = run(
      initialState("s"),
      ev("turn.started", { turn: 1, inputs: ["c1"] }),
      ev("model.retrying", { attempt: 2, max: 3, wait_ms: 1500, reason: "HTTP 500" }),
    );
    expect(s.retry).toEqual({ attempt: 2, max: 3, wait_ms: 1500, reason: "HTTP 500", at: 1000 });
    s = run(s, ev("model.retrying", { attempt: 3, max: 3, wait_ms: 3000, reason: "HTTP 502" }));
    expect(s.retry?.attempt).toBe(3);
    s = run(s, ev("message.started", { stream_id: "a", entry_id: "e1", part: "text", attempt: 3 }));
    expect(s.retry).toBeNull();
  });

  it("model.retrying：回合结束 / 被拒 / 变 idle 时清掉；快照不带", () => {
    const r = ev("model.retrying", { attempt: 2, max: 2, wait_ms: 10, reason: "x" });
    expect(run(initialState("s"), r, ev("turn.ended", { turn: 1, kind: "error" })).retry).toBeNull();
    expect(run(initialState("s"), r, ev("turn.rejected", { reason: "busy" })).retry).toBeNull();
    expect(run(initialState("s"), r, ev("session.state_changed", { run_state: "idle" })).retry).toBeNull();
    expect(run(initialState("s"), r, ev("session.state_changed", { run_state: "running" })).retry?.attempt).toBe(2);
    expect(applySnapshot(run(initialState("s"), r), snap({ run_state: "running" })).retry).toBeNull();
  });

  it("工具按 call_id 合成，tool_result 条目落库后移除", () => {
    let s = run(
      initialState("s"),
      ev("tool.started", { call_id: "c", name: "write_file", entry_id: "e1" }),
      ev("tool.args_delta", { call_id: "c", text: '{"path"' }),
    );
    expect(s.tools[0]).toMatchObject({ phase: "args", args: '{"path"' });
    s = run(s, ev("tool.running", { call_id: "c", args: { path: "a" } }), ev("tool.ended", { call_id: "c", status: "ok", preview: "done" }));
    expect(s.tools[0]).toMatchObject({ phase: "ended", status: "ok", args: '{"path":"a"}' });
    s = run(s, ev("entry.appended", { entry: entry(2, 1, "tool_result", { call_id: "c" }) }));
    expect(s.tools).toHaveLength(0);
  });

  it("log.rewound 截断；条目去重并按 idx 排序", () => {
    let s = mergeEntries(initialState("s"), [entry(3, 2), entry(1, 1), entry(2, 1)]);
    s = mergeEntries(s, [entry(2, 1)]);
    expect(s.entries.map((e) => e.idx)).toEqual([1, 2, 3]);
    s = run(s, ev("log.rewound", { to: 2, voided_interactions: [] }));
    expect(s.entries.map((e) => e.idx)).toEqual([1]);
    expect(s.len).toBe(2);
  });

  it("turn.ended 清掉残留的流与工具，记下结束原因", () => {
    const s = run(
      initialState("s"),
      ev("turn.started", { turn: 1, inputs: ["c1"] }),
      ev("message.started", { stream_id: "a", entry_id: "e1", part: "text", attempt: 1 }),
      ev("tool.started", { call_id: "c", name: "x", entry_id: "e1" }),
      ev("turn.ended", { turn: 1, kind: "cancelled" }),
    );
    expect(s.runState).toBe("idle");
    expect(s.streams).toHaveLength(0);
    expect(s.tools).toHaveLength(0);
    expect(s.lastEnd?.kind).toBe("cancelled");
  });

  it("别的会话的事件不影响状态", () => {
    const s0 = initialState("s");
    expect(reduce(s0, { ...ev("turn.started", { turn: 1, inputs: [] }), session: "other" })).toBe(s0);
  });

  it("按回合分组，进行中的回合标为未结束", () => {
    let s = mergeEntries(initialState("s"), [entry(0, 1, "user"), entry(1, 1), entry(2, 2, "user")]);
    s = run(s, ev("turn.started", { turn: 2, inputs: [] }));
    const g = groupTurns(s);
    expect(g.map((x) => [x.turn, x.entries.length, x.done])).toEqual([
      [1, 2, true],
      [2, 1, false],
    ]);
  });
  it("session.updated 改标题与置顶；快照带 pinned", () => {
    const s0 = applySnapshot(initialState("s"), snap({ title: "旧", pinned: true }));
    expect([s0.title, s0.pinned]).toEqual(["旧", true]);
    const s1 = run(s0, ev("session.updated", { title: "新", pinned: false }, 6));
    expect([s1.title, s1.pinned]).toEqual(["新", false]);
  });
  it("settings 带 model：快照与 settings_changed", () => {
    const s0 = applySnapshot(initialState("s"), snap({ settings: { max_steps: 3, model: "a" } }));
    expect([s0.maxSteps, s0.model]).toEqual([3, "a"]);
    const s1 = run(s0, ev("session.settings_changed", { settings: { max_steps: 3, model: "b" } }, 6));
    expect(s1.model).toBe("b");
  });
  it("tool.progress：运行中替换显示的尾部，结束后清掉；未运行的不收", () => {
    let st = run(initialState("s"), ev("tool.started", { call_id: "c1", name: "shell", entry_id: "e1" }));
    st = run(st, ev("tool.progress", { call_id: "c1", text: "早到的" }));
    expect(st.tools[0].live).toBeUndefined();
    st = run(st, ev("tool.running", { call_id: "c1", args: {} }), ev("tool.progress", { call_id: "c1", text: "a" }), ev("tool.progress", { call_id: "c1", text: "a\nb" }));
    expect(st.tools[0].live).toBe("a\nb");
    st = run(st, ev("tool.ended", { call_id: "c1", status: "ok", preview: "done" }));
    expect(st.tools[0].live).toBeUndefined();
  });
});

describe("last_end（回合结束原因持久化，docs/50 M2 协议T3）", () => {
  const failed = (turn: number, code = "AUTH") => ({ turn, kind: "failed", code, message: "401" });

  it("快照先到：idle 快照带 last_end → lastEnd，按回合号给尾巴", () => {
    const s = applySnapshot(mergeEntries(initialState("s"), [entry(0, 1, "user")]), snap({ last_end: failed(1) }));
    expect(s.lastEnd).toMatchObject({ turn: 1, kind: "failed", code: "AUTH" });
    expect(turnEnd(s, 1)?.code).toBe("AUTH");
    expect(turnEnd(s, 2)).toBeNull(); // 回合号不匹配不显示
  });

  it("快照先到，之后的回合：turn.started 清空，state_changed（idle + last_end）记下新回合的", () => {
    let s = applySnapshot(initialState("s"), snap({ last_end: failed(1) }));
    s = run(s, ev("turn.started", { turn: 2, inputs: [] }));
    expect(s.lastEnd).toBeNull();
    s = run(s, ev("session.state_changed", { run_state: "idle", last_end: failed(2, "RATE_LIMIT") }, 9));
    expect([s.runState, s.lastEnd?.turn, s.lastEnd?.code]).toEqual(["idle", 2, "RATE_LIMIT"]);
  });

  it("事件先到：turn.ended 已记下，随后的旧服务端快照（没有 last_end）不清掉它", () => {
    let s = run(initialState("s"), ev("turn.ended", failed(3)));
    s = applySnapshot(s, snap());
    expect(s.lastEnd?.turn).toBe(3);
    // 新服务端快照带 last_end：以快照为准
    s = applySnapshot(s, snap({ last_end: { turn: 3, kind: "done" } }));
    expect(s.lastEnd?.kind).toBe("done");
  });

  it("没有 last_end（旧服务端）：快照与 state_changed 都不动 lastEnd，退回只靠 turn.ended", () => {
    let s = applySnapshot(initialState("s"), snap());
    expect(s.lastEnd).toBeNull();
    s = run(s, ev("session.state_changed", { run_state: "idle" }, 6));
    expect(s.lastEnd).toBeNull();
    s = run(s, ev("turn.ended", failed(1)), ev("session.state_changed", { run_state: "idle" }, 7));
    expect(s.lastEnd?.turn).toBe(1); // idle 但没带 last_end：保留
  });

  it("运行中：running 的快照与 state_changed 都清空 lastEnd", () => {
    const s0 = run(initialState("s"), ev("turn.ended", failed(1)));
    expect(applySnapshot(s0, snap({ run_state: "running", last_end: failed(1) })).lastEnd).toBeNull();
    expect(run(s0, ev("session.state_changed", { run_state: "running" }, 6)).lastEnd).toBeNull();
  });

  it("log.rewound 截掉结束原因所在的回合：lastEnd 清空；截在它之后：保留", () => {
    const base = mergeEntries(initialState("s"), [entry(0, 1, "user"), entry(1, 1), entry(2, 2, "user")]);
    const s = run(base, ev("turn.ended", failed(2)));
    expect(run(s, ev("log.rewound", { to: 2, voided_interactions: [] })).lastEnd).toBeNull();
    const s1 = run(base, ev("turn.ended", failed(1)));
    expect(run(s1, ev("log.rewound", { to: 2, voided_interactions: [] })).lastEnd?.turn).toBe(1);
  });
});
