import { describe, expect, it } from "vitest";
import { applyChanged, createSessionsStore, listed, type SessionsView } from "./sessions";
import { createStore } from "./store";
import type { Connection } from "./transport";
import type { Envelope, ListArgs, ListResult, SessionInfo } from "./wire.gen";

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
