// 动态面板区的布局（前端方案第六节）：两个停靠区（右侧、底部）各一棵布局树，外加浮动面板。纯函数，便于测试与持久化。
//
// 树的节点：
//   - group：一组标签（面板 id），有一个当前标签；
//   - split：方向 + 子节点 + 各自占比（和为 1）。
// 规则：
//   - 一个面板同一时刻只在一处出现（打开已在别处的面板 = 激活那里的标签）；
//   - 组里最后一个标签移走后组删除，split 只剩一个子节点时折叠成那个子节点；
//   - 拖到组的上 / 下 / 左 / 右边缘：在该方向拆出新组；拖到中央：移入该组。

export type Dir = "row" | "column";

export interface GroupNode {
  type: "group";
  id: string;
  tabs: string[];
  active: string;
}

export interface SplitNode {
  type: "split";
  id: string;
  dir: Dir;
  children: LayoutNode[];
  sizes: number[];
}

export type LayoutNode = GroupNode | SplitNode;

export type DockId = "right" | "bottom";

export interface FloatPanel {
  panel: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface LayoutState {
  docks: Record<DockId, LayoutNode | null>;
  /** 停靠区是否收起（树保留，只是不显示）。 */
  hidden: Record<DockId, boolean>;
  /** 停靠区尺寸：右侧为宽度、底部为高度（px）。 */
  size: Record<DockId, number>;
  floats: FloatPanel[];
  /** 最大化的面板（占满面板区）。 */
  maximized: string | null;
  /** 最近关闭的面板（⌘⇧T 恢复），最新在前。 */
  closed: { panel: string; dock: DockId }[];
  /** 面板的打开参数（openPanel(id, params)，如用量面板定位到某回合）。 */
  params: Record<string, unknown>;
}

export const EMPTY_LAYOUT: LayoutState = {
  docks: { right: null, bottom: null },
  hidden: { right: false, bottom: false },
  size: { right: 380, bottom: 260 },
  floats: [],
  maximized: null,
  closed: [],
  params: {},
};

let seq = 0;
const nid = (p: string) => `${p}${Date.now().toString(36)}${(seq++).toString(36)}`;

export type DropEdge = "center" | "top" | "bottom" | "left" | "right";

export type LayoutAction =
  | { type: "open"; panel: string; dock?: DockId; group?: string; params?: unknown }
  | { type: "close"; panel: string }
  | { type: "toggle"; panel: string; dock?: DockId }
  | { type: "activate"; group: string; panel: string }
  | { type: "move"; panel: string; group: string; edge: DropEdge; index?: number }
  | { type: "moveToDock"; panel: string; dock: DockId }
  | { type: "float"; panel: string; x: number; y: number; w?: number; h?: number }
  | { type: "moveFloat"; panel: string; x: number; y: number; w?: number; h?: number }
  | { type: "resize"; split: string; sizes: number[] }
  | { type: "equalize"; split: string }
  | { type: "dockSize"; dock: DockId; size: number }
  | { type: "toggleDock"; dock: DockId }
  | { type: "maximize"; panel: string | null }
  | { type: "reopen" }
  | { type: "replace"; state: LayoutState };

// ---- 查询 ----

/** 面板在哪：停靠区里的组，或浮动。 */
export function locate(s: LayoutState, panel: string): { dock: DockId; group: GroupNode } | { float: FloatPanel } | null {
  for (const d of ["right", "bottom"] as DockId[]) {
    const g = findGroupWith(s.docks[d], panel);
    if (g) return { dock: d, group: g };
  }
  const f = s.floats.find((x) => x.panel === panel);
  return f ? { float: f } : null;
}

/** 面板是否可见（在可见停靠区的组里是当前标签，或浮动）。 */
export function isVisible(s: LayoutState, panel: string): boolean {
  const at = locate(s, panel);
  if (!at) return false;
  if ("float" in at) return true;
  return !s.hidden[at.dock] && at.group.active === panel;
}

/** 面板是否打开（任何地方，含收起的停靠区与非当前标签）。 */
export function isOpen(s: LayoutState, panel: string): boolean {
  return locate(s, panel) !== null;
}

export function allPanels(n: LayoutNode | null): string[] {
  if (!n) return [];
  return n.type === "group" ? [...n.tabs] : n.children.flatMap(allPanels);
}

function findGroupWith(n: LayoutNode | null, panel: string): GroupNode | null {
  if (!n) return null;
  if (n.type === "group") return n.tabs.includes(panel) ? n : null;
  for (const c of n.children) {
    const g = findGroupWith(c, panel);
    if (g) return g;
  }
  return null;
}

function findGroup(n: LayoutNode | null, id: string): GroupNode | null {
  if (!n) return null;
  if (n.type === "group") return n.id === id ? n : null;
  for (const c of n.children) {
    const g = findGroup(c, id);
    if (g) return g;
  }
  return null;
}

function firstGroup(n: LayoutNode | null): GroupNode | null {
  if (!n) return null;
  return n.type === "group" ? n : firstGroup(n.children[0] ?? null);
}

// ---- 树变换（都返回新树）----

function mapTree(n: LayoutNode | null, f: (g: GroupNode) => LayoutNode | null): LayoutNode | null {
  if (!n) return null;
  if (n.type === "group") return f(n);
  const kids: LayoutNode[] = [];
  const sizes: number[] = [];
  n.children.forEach((c, i) => {
    const m = mapTree(c, f);
    if (m) {
      kids.push(m);
      sizes.push(n.sizes[i] ?? 1 / n.children.length);
    }
  });
  return normalizeSplit({ ...n, children: kids, sizes });
}

/** split 清理：空了删除、只剩一个折叠、同方向嵌套展平、占比归一。 */
function normalizeSplit(n: SplitNode): LayoutNode | null {
  if (n.children.length === 0) return null;
  if (n.children.length === 1) return n.children[0];
  const kids: LayoutNode[] = [];
  const sizes: number[] = [];
  n.children.forEach((c, i) => {
    const sz = n.sizes[i] ?? 1 / n.children.length;
    if (c.type === "split" && c.dir === n.dir) {
      const tot = c.sizes.reduce((a, b) => a + b, 0) || 1;
      c.children.forEach((cc, j) => {
        kids.push(cc);
        sizes.push((sz * (c.sizes[j] ?? 0)) / tot);
      });
    } else {
      kids.push(c);
      sizes.push(sz);
    }
  });
  const tot = sizes.reduce((a, b) => a + b, 0) || 1;
  return { ...n, children: kids, sizes: sizes.map((x) => x / tot) };
}

function removePanel(n: LayoutNode | null, panel: string): LayoutNode | null {
  return mapTree(n, (g) => {
    if (!g.tabs.includes(panel)) return g;
    const i = g.tabs.indexOf(panel);
    const tabs = g.tabs.filter((t) => t !== panel);
    if (!tabs.length) return null;
    const active = g.active === panel ? tabs[Math.max(0, i - 1)] : g.active;
    return { ...g, tabs, active };
  });
}

function withoutPanel(s: LayoutState, panel: string): LayoutState {
  return {
    ...s,
    docks: { right: removePanel(s.docks.right, panel), bottom: removePanel(s.docks.bottom, panel) },
    floats: s.floats.filter((f) => f.panel !== panel),
    maximized: s.maximized === panel ? null : s.maximized,
  };
}

function withParams(s: LayoutState, panel: string, params: unknown): LayoutState {
  if (params === undefined) return s;
  return { ...s, params: { ...s.params, [panel]: params } };
}

function dropParams(s: LayoutState, panel: string): LayoutState {
  if (!(panel in s.params)) return s;
  const params = { ...s.params };
  delete params[panel];
  return { ...s, params };
}

function addToGroup(n: LayoutNode | null, group: string | null, panel: string, index?: number): LayoutNode {
  const target = group ? findGroup(n, group) : firstGroup(n);
  if (!n || !target) {
    const g: GroupNode = { type: "group", id: nid("g"), tabs: [panel], active: panel };
    if (!n) return g;
    return normalizeSplit({ type: "split", id: nid("s"), dir: "column", children: [n, g], sizes: [0.5, 0.5] })!;
  }
  return mapTree(n, (g) => {
    if (g.id !== target.id) return g;
    const tabs = [...g.tabs];
    tabs.splice(index ?? tabs.length, 0, panel);
    return { ...g, tabs, active: panel };
  })!;
}

/** 在组的某条边上拆出新组。 */
function splitAt(n: LayoutNode, group: string, edge: Exclude<DropEdge, "center">, panel: string): LayoutNode {
  const dir: Dir = edge === "left" || edge === "right" ? "row" : "column";
  const before = edge === "left" || edge === "top";
  const fresh: GroupNode = { type: "group", id: nid("g"), tabs: [panel], active: panel };
  const walk = (x: LayoutNode): LayoutNode => {
    if (x.type === "group") {
      if (x.id !== group) return x;
      return {
        type: "split",
        id: nid("s"),
        dir,
        children: before ? [fresh, x] : [x, fresh],
        sizes: [0.5, 0.5],
      };
    }
    return normalizeSplit({ ...x, children: x.children.map(walk) })!;
  };
  return walk(n);
}

function dockOf(s: LayoutState, group: string): DockId | null {
  for (const d of ["right", "bottom"] as DockId[]) if (findGroup(s.docks[d], group)) return d;
  return null;
}

function pushClosed(s: LayoutState, panel: string): LayoutState["closed"] {
  const at = locate(s, panel);
  const dock: DockId = at && "dock" in at ? at.dock : "right";
  return [{ panel, dock }, ...s.closed.filter((c) => c.panel !== panel)].slice(0, 10);
}

// ---- reducer ----

export function layoutReducer(s: LayoutState, a: LayoutAction): LayoutState {
  switch (a.type) {
    case "open": {
      const at = locate(s, a.panel);
      if (at) {
        if ("float" in at) return withParams(s, a.panel, a.params);
        const docks = {
          ...s.docks,
          [at.dock]: mapTree(s.docks[at.dock], (g) => (g.id === at.group.id ? { ...g, active: a.panel } : g)),
        };
        return withParams({ ...s, docks, hidden: { ...s.hidden, [at.dock]: false } }, a.panel, a.params);
      }
      const dock = a.dock ?? "right";
      return withParams({
        ...s,
        docks: { ...s.docks, [dock]: addToGroup(s.docks[dock], a.group ?? null, a.panel) },
        hidden: { ...s.hidden, [dock]: false },
        closed: s.closed.filter((c) => c.panel !== a.panel),
      }, a.panel, a.params);
    }
    case "close":
      if (!isOpen(s, a.panel)) return s;
      return dropParams({ ...withoutPanel(s, a.panel), closed: pushClosed(s, a.panel) }, a.panel);
    case "toggle":
      return isVisible(s, a.panel) ? layoutReducer(s, { type: "close", panel: a.panel }) : layoutReducer(s, { type: "open", panel: a.panel, dock: a.dock });
    case "activate":
      for (const d of ["right", "bottom"] as DockId[]) {
        if (findGroup(s.docks[d], a.group))
          return { ...s, docks: { ...s.docks, [d]: mapTree(s.docks[d], (g) => (g.id === a.group && g.tabs.includes(a.panel) ? { ...g, active: a.panel } : g)) } };
      }
      return s;
    case "move": {
      const dock = dockOf(s, a.group);
      if (!dock) return s;
      const target = findGroup(s.docks[dock], a.group)!;
      // 拖回自己所在且是唯一标签的组的边缘：无事可做
      if (a.edge !== "center" && target.tabs.length === 1 && target.tabs[0] === a.panel) return s;
      if (a.edge === "center" && target.tabs.includes(a.panel)) {
        // 组内换序
        const tabs = target.tabs.filter((t) => t !== a.panel);
        tabs.splice(Math.min(a.index ?? tabs.length, tabs.length), 0, a.panel);
        return { ...s, docks: { ...s.docks, [dock]: mapTree(s.docks[dock], (g) => (g.id === a.group ? { ...g, tabs, active: a.panel } : g)) } };
      }
      const cleared = withoutPanel(s, a.panel);
      const root = cleared.docks[dock];
      if (!root || !findGroup(root, a.group)) {
        // 目标组因移走这个面板而消失（不会发生在上面的判断之后，保险起见放回原处）
        return layoutReducer(cleared, { type: "open", panel: a.panel, dock });
      }
      const next = a.edge === "center" ? addToGroup(root, a.group, a.panel, a.index) : splitAt(root, a.group, a.edge, a.panel);
      return { ...cleared, docks: { ...cleared.docks, [dock]: next }, hidden: { ...cleared.hidden, [dock]: false } };
    }
    case "moveToDock": {
      const cleared = withoutPanel(s, a.panel);
      return {
        ...cleared,
        docks: { ...cleared.docks, [a.dock]: addToGroup(cleared.docks[a.dock], null, a.panel) },
        hidden: { ...cleared.hidden, [a.dock]: false },
      };
    }
    case "float": {
      const cleared = withoutPanel(s, a.panel);
      return { ...cleared, floats: [...cleared.floats, { panel: a.panel, x: a.x, y: a.y, w: a.w ?? 360, h: a.h ?? 420 }] };
    }
    case "moveFloat":
      return {
        ...s,
        floats: s.floats.map((f) => (f.panel === a.panel ? { ...f, x: a.x, y: a.y, w: a.w ?? f.w, h: a.h ?? f.h } : f)),
      };
    case "resize": {
      const walk = (n: LayoutNode | null): LayoutNode | null => {
        if (!n || n.type === "group") return n;
        if (n.id === a.split && a.sizes.length === n.children.length) {
          const tot = a.sizes.reduce((x, y) => x + y, 0) || 1;
          return { ...n, sizes: a.sizes.map((x) => x / tot) };
        }
        return { ...n, children: n.children.map((c) => walk(c)!) };
      };
      return { ...s, docks: { right: walk(s.docks.right), bottom: walk(s.docks.bottom) } };
    }
    case "equalize": {
      const walk = (n: LayoutNode | null): LayoutNode | null => {
        if (!n || n.type === "group") return n;
        if (n.id === a.split) return { ...n, sizes: n.children.map(() => 1 / n.children.length) };
        return { ...n, children: n.children.map((c) => walk(c)!) };
      };
      return { ...s, docks: { right: walk(s.docks.right), bottom: walk(s.docks.bottom) } };
    }
    case "dockSize":
      return { ...s, size: { ...s.size, [a.dock]: Math.round(a.size) } };
    case "toggleDock":
      return { ...s, hidden: { ...s.hidden, [a.dock]: !s.hidden[a.dock] } };
    case "maximize":
      return { ...s, maximized: a.panel };
    case "reopen": {
      const [first, ...rest] = s.closed;
      if (!first) return s;
      return { ...layoutReducer({ ...s, closed: rest }, { type: "open", panel: first.panel, dock: first.dock }) };
    }
    case "replace":
      return a.state;
  }
}

/** 持久化前清理：去掉已不存在的面板（插件停用后），坏数据退回空布局。 */
export function sanitize(s: unknown, known: Set<string>): LayoutState {
  try {
    const x = s as LayoutState;
    if (!x || typeof x !== "object" || !x.docks) return EMPTY_LAYOUT;
    let st: LayoutState = {
      ...EMPTY_LAYOUT,
      ...x,
      hidden: { ...EMPTY_LAYOUT.hidden, ...x.hidden },
      size: { ...EMPTY_LAYOUT.size, ...x.size },
      floats: Array.isArray(x.floats) ? x.floats : [],
      closed: Array.isArray(x.closed) ? x.closed.filter((c) => known.has(c.panel)) : [],
      params:
        x.params && typeof x.params === "object"
          ? Object.fromEntries(Object.entries(x.params).filter(([k]) => known.has(k)))
          : {},
    };
    for (const d of ["right", "bottom"] as DockId[])
      for (const p of allPanels(st.docks[d] ?? null)) if (!known.has(p)) st = withoutPanel(st, p);
    st = { ...st, floats: st.floats.filter((f) => known.has(f.panel)) };
    if (st.maximized && !known.has(st.maximized)) st = { ...st, maximized: null };
    return st;
  } catch {
    return EMPTY_LAYOUT;
  }
}
