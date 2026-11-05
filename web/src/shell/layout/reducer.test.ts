import { describe, expect, it } from "vitest";
import { EMPTY_LAYOUT, allPanels, isOpen, isVisible, layoutReducer, locate, sanitize, type GroupNode, type LayoutState } from "./reducer";

const run = (s: LayoutState, ...as: Parameters<typeof layoutReducer>[1][]) => as.reduce(layoutReducer, s);
const groupOf = (s: LayoutState, p: string) => {
  const at = locate(s, p);
  return at && "group" in at ? at.group : null;
};

describe("layout reducer", () => {
  it("open 新建组、再开加进同组并激活；已开的面板只激活不重复", () => {
    let s = run(EMPTY_LAYOUT, { type: "open", panel: "a" }, { type: "open", panel: "b" });
    expect(s.docks.right?.type).toBe("group");
    expect((s.docks.right as GroupNode).tabs).toEqual(["a", "b"]);
    expect((s.docks.right as GroupNode).active).toBe("b");
    s = run(s, { type: "open", panel: "a" });
    expect((s.docks.right as GroupNode).tabs).toEqual(["a", "b"]);
    expect((s.docks.right as GroupNode).active).toBe("a");
  });

  it("close 移走标签、最后一个移走组删除，并记入最近关闭；reopen 恢复", () => {
    let s = run(EMPTY_LAYOUT, { type: "open", panel: "a" }, { type: "close", panel: "a" });
    expect(s.docks.right).toBeNull();
    expect(s.closed[0]).toEqual({ panel: "a", dock: "right" });
    s = run(s, { type: "reopen" });
    expect(isOpen(s, "a")).toBe(true);
    expect(s.closed).toEqual([]);
  });

  it("拖到组下边缘拆成上下两组；拖回中央合并，split 折叠", () => {
    let s = run(EMPTY_LAYOUT, { type: "open", panel: "a" }, { type: "open", panel: "b" });
    const g = groupOf(s, "a")!;
    s = run(s, { type: "move", panel: "b", group: g.id, edge: "bottom" });
    expect(s.docks.right?.type).toBe("split");
    if (s.docks.right?.type === "split") {
      expect(s.docks.right.dir).toBe("column");
      expect(s.docks.right.children.map((c) => (c as GroupNode).tabs)).toEqual([["a"], ["b"]]);
    }
    expect(isVisible(s, "a") && isVisible(s, "b")).toBe(true);
    s = run(s, { type: "move", panel: "b", group: groupOf(s, "a")!.id, edge: "center" });
    expect(s.docks.right?.type).toBe("group");
    expect(allPanels(s.docks.right)).toEqual(["a", "b"]);
  });

  it("左右拆分后同方向再拆会展平，占比归一", () => {
    let s = run(EMPTY_LAYOUT, { type: "open", panel: "a" }, { type: "open", panel: "b" }, { type: "open", panel: "c" });
    s = run(s, { type: "move", panel: "b", group: groupOf(s, "a")!.id, edge: "right" });
    s = run(s, { type: "move", panel: "c", group: groupOf(s, "b")!.id, edge: "right" });
    const r = s.docks.right!;
    expect(r.type).toBe("split");
    if (r.type === "split") {
      expect(r.children).toHaveLength(3);
      expect(r.sizes.reduce((x, y) => x + y, 0)).toBeCloseTo(1);
    }
  });

  it("组内换序、浮动与放回停靠区", () => {
    let s = run(EMPTY_LAYOUT, { type: "open", panel: "a" }, { type: "open", panel: "b" });
    s = run(s, { type: "move", panel: "b", group: groupOf(s, "a")!.id, edge: "center", index: 0 });
    expect((s.docks.right as GroupNode).tabs).toEqual(["b", "a"]);
    s = run(s, { type: "float", panel: "b", x: 10, y: 20 });
    expect(allPanels(s.docks.right)).toEqual(["a"]);
    expect(isVisible(s, "b")).toBe(true);
    s = run(s, { type: "moveToDock", panel: "b", dock: "bottom" });
    expect(s.floats).toEqual([]);
    expect(allPanels(s.docks.bottom)).toEqual(["b"]);
  });

  it("toggle：可见则关，不可见则开；收起停靠区后面板不可见但仍打开", () => {
    let s = run(EMPTY_LAYOUT, { type: "toggle", panel: "a" });
    expect(isVisible(s, "a")).toBe(true);
    s = run(s, { type: "toggleDock", dock: "right" });
    expect(isVisible(s, "a")).toBe(false);
    expect(isOpen(s, "a")).toBe(true);
    s = run(s, { type: "toggle", panel: "a" }); // 不可见 → 打开（展开停靠区）
    expect(isVisible(s, "a")).toBe(true);
    s = run(s, { type: "toggle", panel: "a" });
    expect(isOpen(s, "a")).toBe(false);
  });

  it("sanitize 去掉已不存在的面板，坏数据退回空布局", () => {
    let s = run(EMPTY_LAYOUT, { type: "open", panel: "a" }, { type: "open", panel: "gone" });
    s = sanitize(JSON.parse(JSON.stringify(s)), new Set(["a"]));
    expect(allPanels(s.docks.right)).toEqual(["a"]);
    expect(sanitize("bad", new Set())).toEqual(EMPTY_LAYOUT);
    expect(sanitize(null, new Set())).toEqual(EMPTY_LAYOUT);
  });
});
