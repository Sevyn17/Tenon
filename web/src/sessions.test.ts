import { afterEach, describe, expect, it, vi } from "vitest";
import { applyChanged, archivedOf, createSessionsStore, isChildRow, lastEndOf, listed, mergeChanged, workspaceOf, type SessionExtra, type SessionsView } from "./sessions";
import { createStore } from "./store";
import type { Connection } from "./transport";
import type { Envelope, HelloResult, ListArgs, ListResult, SessionInfo, SessionsChanged } from "./wire.gen";

const row = (id: string, updated_at: number, over: Partial<SessionInfo> = {}): SessionInfo => ({
  id,
  title: id,
  preview: "",
  run_state: "idle",
  origin: "new",
  updated_at,
  ...over,
});

const view = (items: SessionInfo[], pinned: SessionInfo[] = []): SessionsView => ({ items, pinned, next: 0, loading: false, all: false });

describe("sessions.changed", () => {
  it("置顶：行移到置顶区，updated_at = 0 时保留原值", () => {
    const v = applyChanged(view([row("a", 20), row("b", 10)]), { id: "b", run_state: "idle", title: "新", updated_at: 0, pinned: true });
    expect(v.pinned.map((x) => [x.id, x.title, x.updated_at])).toEqual([["b", "新", 10]]);
    expect(v.items.map((x) => x.id)).toEqual(["a"]);
    expect(listed(v).map((x) => x.id)).toEqual(["b", "a"]);
  });

  it("取消置顶：回到按时间排序的列表", () => {
    const v = applyChanged(view([row("a", 20), row("c", 5)], [row("b", 10, { pinned: true })]), {
      id: "b",
      run_state: "idle",
      title: "b",
      updated_at: 0,
      pinned: false,
    });
    expect(v.pinned).toEqual([]);
    expect(v.items.map((x) => x.id)).toEqual(["a", "b", "c"]);
  });

  it("删除：连同子会话一起移除", () => {
    const v = applyChanged(view([row("a", 20), row("a/sub", 15), row("ab", 10)]), { id: "a", run_state: "idle", title: "", updated_at: 0, pinned: false, deleted: true });
    expect(v.items.map((x) => x.id)).toEqual(["ab"]);
  });

  it("不在已加载页里且没置顶：忽略；置顶了：补进置顶区", () => {
    const v0 = view([row("a", 20)]);
    expect(applyChanged(v0, { id: "z", run_state: "running", title: "z", updated_at: 30, pinned: false })).toBe(v0);
    const v1 = applyChanged(v0, { id: "z", run_state: "idle", title: "z", updated_at: 0, pinned: true });
    expect(v1.pinned.map((x) => x.id)).toEqual(["z"]);
  });
});

// Mortise 客户端改进 M6 决定 10：没有工作区的旧会话在「未归属」分组（sessions.list {unassigned}）。
describe("未归属的会话", () => {
  /** 假连接：按参数回答 sessions.list，记下每次调用的参数。fail 为真时 unassigned 查询抛错。 */
  function fakeConn(ans: (a: ListArgs) => ListResult, fail = false) {
    const calls: ListArgs[] = [];
    let emit: (ev: Envelope) => void = () => {};
    const conn = {
      call: async (cmd: string, args: ListArgs) => {
        expect(cmd).toBe("sessions.list");
        calls.push(args);
        if (fail && args.unassigned) throw new Error("unsupported");
        return ans(args);
      },
      onEvent: (fn: (ev: Envelope) => void) => {
        emit = fn;
        return () => {};
      },
      state: createStore("open"),
      close() {},
    } as unknown as Connection;
    return { conn, calls, emit: (ev: Envelope) => emit(ev) };
  }
  const answer = (a: ListArgs): ListResult =>
    a.unassigned
      ? a.cursor
        ? { items: [row("u3", 1)], next: 0 }
        : { items: [row("u1", 5), row("u2", 3, { pinned: true })], next: 3 }
      : a.pinned
        ? { items: [], next: 0 }
        : { items: [row("w1", 10)], next: 0 };

  it("有能力：查第一页时一并查未归属，置顶的也留在未归属里；翻页带游标", async () => {
    const f = fakeConn(answer);
    const s = createSessionsStore(f.conn, 50, { unassigned: () => true });
    await s.refresh();
    expect(f.calls.filter((a) => a.unassigned)).toEqual([{ unassigned: true, limit: 50, all: false }]);
    expect(s.get().items.map((x) => x.id)).toEqual(["w1"]);
    expect(s.get().unassigned?.map((x) => x.id)).toEqual(["u1", "u2"]);
    expect(listed(s.get()).map((x) => x.id)).toEqual(["w1"]); // 上下一个会话、命令面板只走当前工作区
    expect(s.find("u2")?.id).toBe("u2");
    await s.moreUnassigned();
    expect(f.calls.at(-1)).toEqual({ unassigned: true, cursor: 3, limit: 50, all: false });
    expect(s.get().unassigned?.map((x) => x.id)).toEqual(["u1", "u2", "u3"]);
    expect(s.get().unassignedNext).toBe(0);
  });

  it("旧服务端（没有能力）：不查未归属，视图照旧", async () => {
    const f = fakeConn(answer);
    const s = createSessionsStore(f.conn, 50, { unassigned: () => false });
    await s.refresh();
    expect(f.calls.some((a) => a.unassigned)).toBe(false);
    expect(s.get().unassigned).toBeUndefined();
    await s.moreUnassigned(); // 没有游标：不发请求
    expect(f.calls).toHaveLength(2);
  });

  it("重连到没有能力的服务端：清空未归属", async () => {
    let can = true;
    const f = fakeConn(answer);
    const s = createSessionsStore(f.conn, 50, { unassigned: () => can });
    await s.refresh();
    can = false;
    await s.refresh();
    expect(s.get().unassigned).toEqual([]);
    expect(s.get().unassignedNext).toBe(0);
  });

  it("未归属查询失败不影响主列表", async () => {
    const f = fakeConn(answer, true);
    const s = createSessionsStore(f.conn, 50, { unassigned: () => true });
    await s.refresh();
    expect(s.get().error).toBeUndefined();
    expect(s.get().items.map((x) => x.id)).toEqual(["w1"]);
    expect(s.get().unassigned).toBeUndefined();
  });

  it("sessions.changed 与本地 upsert：未归属的行原地更新，置顶也不挪进按工作区的列表；删除连同子会话", () => {
    const v0: SessionsView = { ...view([row("w1", 10)]), unassigned: [row("u1", 5), row("u1/sub", 4, { parent: "u1" }), row("u2", 3)] };
    const v1 = applyChanged(v0, { id: "u2", run_state: "running", title: "新", updated_at: 20, pinned: true });
    expect(v1.unassigned?.map((x) => [x.id, x.title, x.run_state, x.pinned])).toEqual([
      ["u2", "新", "running", true],
      ["u1", "u1", "idle", undefined],
      ["u1/sub", "u1/sub", "idle", undefined],
    ]);
    expect(v1.pinned).toEqual([]);
    expect(v1.items.map((x) => x.id)).toEqual(["w1"]);
    const v2 = applyChanged(v1, { id: "u1", run_state: "idle", title: "", updated_at: 0, pinned: false, deleted: true });
    expect(v2.unassigned?.map((x) => x.id)).toEqual(["u2"]);
  });

  it("本地 upsert：未归属里的行改名留在原处，新行照旧进按工作区的列表", async () => {
    const f = fakeConn(answer);
    const s = createSessionsStore(f.conn, 50, { unassigned: () => true });
    await s.refresh();
    s.upsert({ ...s.find("u1")!, title: "改名" });
    expect(s.get().unassigned?.map((x) => x.title)).toEqual(["改名", "u2"]);
    expect(s.get().items.map((x) => x.id)).toEqual(["w1"]);
    s.upsert(row("w2", 30));
    expect(s.get().items.map((x) => x.id)).toEqual(["w2", "w1"]);
    s.remove("u2");
    expect(s.get().unassigned?.map((x) => x.id)).toEqual(["u1"]);
  });
});

// ---- Mortise 客户端改进 M7（协议T11、T14）----

/** 带 M7 字段的 sessions.changed（wire.gen 重录前也能写）。 */
const ch = (id: string, updated_at: number, over: Partial<SessionsChanged> & SessionExtra = {}): SessionsChanged => {
  const d: SessionsChanged & SessionExtra = { id, run_state: "idle", title: id, updated_at, pinned: false, preview: "", origin: "new", workspace: "W", ...over };
  return d;
};
/** 带 M7 字段的列表行。 */
const r7 = (id: string, updated_at: number, over: Partial<SessionInfo> & SessionExtra = {}): SessionInfo => {
  const x: SessionInfo & SessionExtra = { ...row(id, updated_at), workspace: "W", ...over };
  return x;
};
const FULL = { full: true, workspace: "W" };

describe("M7 sessions.changed 插入未知会话（服务端报 sessions.changed.full）", () => {
  it("当前工作区的新会话：带预览与结束原因插入，按更新时间排", () => {
    const r = mergeChanged(view([r7("a", 20), r7("b", 10)]), ch("z", 15, { preview: "它说了什么", last_end: { turn: 1, kind: "done", at: 15 } }), FULL);
    expect(r.refresh).toBe(false);
    expect(r.view.items.map((x) => x.id)).toEqual(["a", "z", "b"]);
    const z = r.view.items[1];
    expect(z.preview).toBe("它说了什么");
    expect(lastEndOf(z)?.kind).toBe("done");
  });

  it("子会话：缺省列表不插；开了「含子会话」才插", () => {
    const d = ch("a/sub", 30, { parent: "a" });
    const v0 = view([r7("a", 20)]);
    expect(mergeChanged(v0, d, FULL)).toEqual({ view: v0, refresh: false });
    expect(mergeChanged({ ...v0, all: true }, d, FULL).view.items.map((x) => x.id)).toEqual(["a/sub", "a"]);
  });

  it("用户分叉的会话（origin=fork）带 parent 也算顶层：缺省列表就插", () => {
    const d = ch("fork-1", 30, { parent: "a", origin: "fork" });
    const r = mergeChanged(view([r7("a", 20)]), d, FULL);
    expect(r.refresh).toBe(false);
    expect(r.view.items.map((x) => x.id)).toEqual(["fork-1", "a"]);
    expect(isChildRow(r.view.items[0])).toBe(false);
    expect(isChildRow({ parent: "a", origin: "subagent" })).toBe(true);
    expect(isChildRow({ parent: "a", origin: "side" })).toBe(true);
    expect(isChildRow({ parent: "a" })).toBe(true);
    expect(isChildRow({})).toBe(false);
  });

  it("别的工作区：不插；没有工作区的进「未归属」（分组已查过时）", () => {
    const v0 = view([r7("a", 20)]);
    expect(mergeChanged(v0, ch("o", 30, { workspace: "OTHER" }), FULL).view).toBe(v0);
    expect(mergeChanged(v0, ch("u", 30, { workspace: "" }), FULL).view).toBe(v0); // 没有未归属分组
    const v1: SessionsView = { ...v0, unassigned: [r7("u0", 5, { workspace: "" })] };
    const r = mergeChanged(v1, ch("u", 30, { workspace: "" }), FULL);
    expect(r.view.unassigned?.map((x) => x.id)).toEqual(["u", "u0"]);
    expect(r.view.items.map((x) => x.id)).toEqual(["a"]);
  });

  it("宿主没有工作区（hello.workspace 为空）：任何 workspace 都算当前", () => {
    const r = mergeChanged(view([r7("a", 20)]), ch("o", 30, { workspace: "X" }), { full: true, workspace: "" });
    expect(r.view.items.map((x) => x.id)).toEqual(["o", "a"]);
  });

  it("已归档：不进缺省列表；「已归档」打开时进那一组", () => {
    const v0 = view([r7("a", 20)]);
    expect(mergeChanged(v0, ch("z", 30, { archived: true }), FULL).view).toBe(v0);
    const v1: SessionsView = { ...v0, archived: { items: [], next: 0, loading: false } };
    const r = mergeChanged(v1, ch("z", 30, { archived: true }), FULL);
    expect(r.view.items.map((x) => x.id)).toEqual(["a"]);
    expect(r.view.archived?.items.map((x) => [x.id, archivedOf(x)])).toEqual([["z", true]]);
  });

  it("比已加载的最后一行还旧且后面还有页：不插（翻页时自然出现）；已到末页就插", () => {
    const v0: SessionsView = { ...view([r7("a", 20), r7("b", 10)]), next: 2 };
    expect(mergeChanged(v0, ch("old", 5), FULL).view).toBe(v0);
    expect(mergeChanged({ ...v0, next: 0 }, ch("old", 5), FULL).view.items.map((x) => x.id)).toEqual(["a", "b", "old"]);
  });

  it("置顶的不看更新时间；updated_at = 0（改名）的未知会话：重查", () => {
    const v0: SessionsView = { ...view([r7("a", 20)]), next: 2 };
    expect(mergeChanged(v0, ch("p", 0, { pinned: true }), FULL).view.pinned.map((x) => x.id)).toEqual(["p"]);
    expect(mergeChanged(v0, ch("r", 0), FULL)).toEqual({ view: v0, refresh: true });
  });

  it("已有的行：全套字段覆盖 preview / last_end（缺省 = 被回退清掉）；updated_at = 0 保留原值", () => {
    const v0 = view([r7("a", 20, { preview: "旧", last_end: { turn: 1, kind: "failed", at: 20 } })]);
    const v1 = applyChanged(v0, ch("a", 0, { title: "改名", preview: "新" }), FULL);
    expect(v1.items[0]).toMatchObject({ title: "改名", preview: "新", updated_at: 20 });
    expect(lastEndOf(v1.items[0])).toBeUndefined();
    const v2 = applyChanged(v1, ch("a", 30, { last_end: { turn: 2, kind: "failed", at: 30 } }), FULL);
    expect(lastEndOf(v2.items[0])).toMatchObject({ kind: "failed", at: 30 });
  });

  it("已有的行归档：从缺省列表与置顶区拿掉（打开着「已归档」时挪进去）；恢复：回到缺省列表", () => {
    const v0: SessionsView = { ...view([r7("a", 20)], [r7("p", 10, { pinned: true })]), archived: { items: [], next: 0, loading: false } };
    const v1 = applyChanged(v0, ch("p", 0, { archived: true }), FULL);
    expect(v1.pinned).toEqual([]);
    expect(v1.archived?.items.map((x) => [x.id, x.updated_at, x.pinned])).toEqual([["p", 10, undefined]]);
    const v2 = applyChanged(v1, ch("p", 0, { archived: false }), FULL);
    expect(v2.archived?.items).toEqual([]);
    expect(v2.items.map((x) => x.id)).toEqual(["a", "p"]);
    // 没打开「已归档」：归档只是拿掉
    const v3 = applyChanged(view([r7("a", 20)]), ch("a", 0, { archived: true }), FULL);
    expect(v3.items).toEqual([]);
    expect(v3.archived).toBeUndefined();
  });

  it("删除：连同子会话，也从「已归档」里删", () => {
    const v0: SessionsView = { ...view([r7("a", 20)]), archived: { items: [r7("z", 5, { archived: true }), r7("z/s", 4)], next: 0, loading: false } };
    const v1 = applyChanged(v0, { id: "z", run_state: "idle", title: "", updated_at: 0, pinned: false, deleted: true }, FULL);
    expect(v1.archived?.items).toEqual([]);
    expect(v1.items.map((x) => x.id)).toEqual(["a"]);
  });
});

describe("M7 旧服务端兜底（没有 sessions.changed.full）", () => {
  it("未知且没置顶：视图不变、要求重查；子会话 id 不重查；置顶的补进置顶区", () => {
    const v0 = view([row("a", 20)]);
    expect(mergeChanged(v0, { id: "z", run_state: "running", title: "z", updated_at: 30, pinned: false })).toEqual({ view: v0, refresh: true });
    expect(mergeChanged(v0, { id: "a/sub", run_state: "running", title: "", updated_at: 30, pinned: false }).refresh).toBe(false);
    expect(mergeChanged(v0, { id: "p", run_state: "idle", title: "p", updated_at: 0, pinned: true }).view.pinned.map((x) => x.id)).toEqual(["p"]);
  });

  it("旧事件里没有的字段：已有行的 preview / last_end 保持不动", () => {
    const v0 = view([r7("a", 20, { preview: "旧", last_end: { turn: 1, kind: "done", at: 20 } })]);
    const v1 = applyChanged(v0, { id: "a", run_state: "running", title: "a", updated_at: 25, pinned: false });
    expect(v1.items[0].preview).toBe("旧");
    expect(lastEndOf(v1.items[0])?.kind).toBe("done");
  });

  it("就算事件里带了 archived，没有能力也不当真（不挪行）", () => {
    const v0 = view([row("a", 20)]);
    expect(applyChanged(v0, ch("a", 0, { archived: true })).items.map((x) => x.id)).toEqual(["a"]);
  });
});

describe("M7 store：防抖重查、已归档、搜索、子会话", () => {
  afterEach(() => vi.useRealTimers());

  type Call = { cmd: string; args: Record<string, unknown> };
  function conn7(ans: (cmd: string, a: Record<string, unknown>) => unknown) {
    const calls: Call[] = [];
    let emit: (ev: Envelope) => void = () => {};
    const conn = {
      call: async (cmd: string, args: Record<string, unknown>) => {
        calls.push({ cmd, args });
        return ans(cmd, args);
      },
      onEvent: (fn: (ev: Envelope) => void) => {
        emit = fn;
        return () => {};
      },
      state: createStore("open"),
      close() {},
    } as unknown as Connection;
    const changed = (d: SessionsChanged) => emit({ kind: "sessions.changed", data: d } as unknown as Envelope);
    return { conn, calls, changed, lists: () => calls.filter((c) => c.cmd === "sessions.list") };
  }
  const empty = { items: [], next: 0 };

  it("旧服务端：一串未知会话事件只触发一次重查（1 秒防抖）", async () => {
    vi.useFakeTimers();
    const f = conn7(() => empty);
    const s = createSessionsStore(f.conn, 50, { full: () => false });
    for (const id of ["x", "y", "z"]) f.changed({ id, run_state: "running", title: id, updated_at: 30, pinned: false });
    expect(f.lists()).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1000);
    expect(f.lists().map((c) => c.args.pinned ?? false)).toEqual([false, true]); // 第一页 + 置顶
    s.close();
  });

  it("新服务端：未知会话直接插入，不重查", async () => {
    vi.useFakeTimers();
    const f = conn7(() => empty);
    const s = createSessionsStore(f.conn, 50, { full: () => true, workspace: () => "W" });
    f.changed(ch("n", 30, { preview: "hi" }));
    await vi.advanceTimersByTimeAsync(1500);
    expect(f.lists()).toHaveLength(0);
    expect(s.get().items.map((x) => [x.id, x.preview])).toEqual([["n", "hi"]]);
    s.close();
  });

  it("已归档：openArchived 查 {archived:only}，行带 archived；setArchived(false) 恢复后重查缺省列表", async () => {
    const f = conn7((cmd, a) => {
      if (cmd === "session.update") return { id: a.session, origin: "new", title: "z", run_state: "idle", archived: a.archived };
      if (a.archived === "only") return { items: [row("z", 5)], next: 0 };
      return a.pinned ? empty : { items: [row("a", 20)], next: 0 };
    });
    const s = createSessionsStore(f.conn, 50, { full: () => true, query: () => true });
    await s.openArchived();
    expect(f.lists()[0].args).toEqual({ archived: "only", cursor: 0, limit: 50, all: false });
    expect(s.get().archived?.items.map((x) => [x.id, archivedOf(x)])).toEqual([["z", true]]);
    expect(listed(s.get()).map((x) => x.id)).toEqual([]); // 已归档不在 listed() 里
    await s.setArchived("z", false);
    expect(f.calls.find((c) => c.cmd === "session.update")?.args).toEqual({ session: "z", archived: false });
    expect(s.get().archived?.items).toEqual([]);
    expect(s.get().items.map((x) => x.id)).toEqual(["a"]); // 重查了第一页
    s.closeArchived();
    expect(s.get().archived).toBeUndefined();
  });

  it("setArchived(true)：从缺省列表拿掉，「已归档」打开时挪进去；失败时抛出、列表不动", async () => {
    let fail = false;
    const f = conn7((cmd, a) => {
      if (cmd === "session.update") {
        if (fail) throw new Error("unsupported");
        return { id: a.session, origin: "new", title: "a", run_state: "idle", archived: true };
      }
      return a.archived === "only" ? empty : a.pinned ? empty : { items: [row("a", 20), row("b", 10)], next: 0 };
    });
    const s = createSessionsStore(f.conn, 50, { full: () => true, query: () => true });
    await s.refresh();
    await s.openArchived();
    await s.setArchived("a", true);
    expect(s.get().items.map((x) => x.id)).toEqual(["b"]);
    expect(s.get().archived?.items.map((x) => x.id)).toEqual(["a"]);
    fail = true;
    await expect(s.setArchived("b", true)).rejects.toThrow("unsupported");
    expect(s.get().items.map((x) => x.id)).toEqual(["b"]);
  });

  it("查询途中收起「已归档」：丢弃结果", async () => {
    let release: (v: unknown) => void = () => {};
    const f = conn7(() => new Promise((res) => (release = res)));
    const s = createSessionsStore(f.conn);
    const p = s.openArchived();
    s.closeArchived();
    release({ items: [row("z", 5)], next: 0 });
    await p;
    expect(s.get().archived).toBeUndefined();
  });

  it("「已归档」查询失败：记在 archived.error", async () => {
    const f = conn7(() => {
      throw new Error("boom");
    });
    const s = createSessionsStore(f.conn);
    await s.openArchived();
    expect(s.get().archived).toMatchObject({ loading: false, error: "boom" });
  });

  it("search：query 去首尾空白后传给服务端", async () => {
    const f = conn7(() => ({ items: [row("hit", 1)], next: 0 }));
    const s = createSessionsStore(f.conn);
    expect((await s.search("  正文词 ")).map((x) => x.id)).toEqual(["hit"]);
    expect(f.lists()[0].args).toEqual({ query: "正文词", limit: 50, all: false });
  });

  it("children：有能力时按 parent 查；没有能力时取 200 条再过滤；失败抛出", async () => {
    const f = conn7((_c, a) => (a.parent ? { items: [row("p/1", 2, { parent: "p" })], next: 0 } : { items: [row("p/1", 2, { parent: "p" }), row("q", 1), row("p/2", 1)], next: 0 }));
    let can = true;
    const s = createSessionsStore(f.conn, 50, { query: () => can });
    expect((await s.children("p")).map((x) => x.id)).toEqual(["p/1"]);
    expect(f.lists()[0].args).toEqual({ parent: "p", all: true, limit: 200 });
    can = false;
    expect((await s.children("p")).map((x) => x.id)).toEqual(["p/1", "p/2"]);
    expect(f.lists()[1].args).toEqual({ all: true, limit: 200 });
    const bad = conn7(() => {
      throw new Error("down");
    });
    await expect(createSessionsStore(bad.conn).children("p")).rejects.toThrow("down");
  });
});

describe("M7 字段守卫", () => {
  it("lastEndOf / archivedOf / workspaceOf：字段缺省或类型不对时安全返回", () => {
    expect(lastEndOf(row("a", 1))).toBeUndefined();
    expect(lastEndOf(r7("a", 1, { last_end: { turn: 1, kind: "failed" } }))?.kind).toBe("failed");
    // 线上来的坏数据（JSON 解析结果不受类型约束）
    const bad: SessionInfo = JSON.parse(JSON.stringify({ ...row("a", 1), last_end: "failed" }));
    expect(lastEndOf(bad)).toBeUndefined();
    expect(archivedOf(row("a", 1))).toBe(false);
    expect(archivedOf(r7("a", 1, { archived: true }))).toBe(true);
    const h: HelloResult = { protocol: 2, server: "s", principal: { id: "u", role: "owner" }, capabilities: [], model: "", workdir: "", trusted: true, max_output: 0 };
    expect(workspaceOf(h)).toBeUndefined();
    const h2: HelloResult = { ...h, workspace: "" };
    expect(workspaceOf(h2)).toBe("");
    expect(workspaceOf(null)).toBeUndefined();
  });
});
