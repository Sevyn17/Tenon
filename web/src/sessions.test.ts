import { describe, expect, it } from "vitest";
import { applyChanged, listed, type SessionsView } from "./sessions";
import type { SessionInfo } from "./wire.gen";

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
