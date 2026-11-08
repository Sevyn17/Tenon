// 动态面板区（前端方案第六节）：把布局树渲染成可拖拽、可拆分、可改大小的面板组。
//
// 拖拽用浏览器原生 drag and drop（数据类型 PANEL_MIME，值是面板 id）：
//   - 拖到标签栏：插到落点处的标签之前（同组换序或从别的组移入）；
//   - 拖到组内容区：按落点离哪条边最近拆分（中间 1/3 区域 = 移入该组），落点实时高亮；
//   - 拖到窗口别处放下：变成浮动面板。
// 分隔条与停靠区边缘用指针拖动（pointer capture），双击分隔条等分。

import { useCallback, useEffect, useRef, useState, type CSSProperties, type DragEvent, type PointerEvent, type ReactNode } from "react";
import { Maximize2, Minimize2, PictureInPicture2, X } from "lucide-react";
import { cx, ErrorBoundary, IconButton } from "../../ui";
import type { Panel } from "../contributions";
import s from "./dock.module.css";
import type { DockId, DropEdge, GroupNode, LayoutNode, LayoutState, SplitNode } from "./reducer";

export const PANEL_MIME = "application/x-tenon-panel";

export interface DockProps {
  dock: DockId;
  state: LayoutState;
  dispatch: (a: import("./reducer").LayoutAction) => void;
  panels: Map<string, Panel>;
  /** 渲染某个面板的内容（宿主包上 ErrorBoundary、给 props）。 */
  renderPanel: (p: Panel, visible: boolean) => ReactNode;
  empty?: ReactNode;
}

/** 一个停靠区（右侧或底部）。收起或没有面板时不渲染。 */
export function Dock(p: DockProps) {
  const root = p.state.docks[p.dock];
  if (!root || p.state.hidden[p.dock]) return null;
  const size = p.state.size[p.dock];
  const style: CSSProperties = p.dock === "right" ? { width: size } : { height: size };
  const max = p.state.maximized;
  return (
    <aside className={cx(s.dock, s[`dock_${p.dock}`])} style={style} aria-label={p.dock === "right" ? "右侧面板" : "底部面板"}>
      <DockResize dock={p.dock} size={size} dispatch={p.dispatch} />
      {max && containsPanel(root, max) ? (
        <MaxView panel={max} {...p} />
      ) : (
        <NodeView node={root} {...p} />
      )}
    </aside>
  );
}

function containsPanel(n: LayoutNode, panel: string): boolean {
  return n.type === "group" ? n.tabs.includes(panel) : n.children.some((c) => containsPanel(c, panel));
}

function MaxView(p: DockProps & { panel: string }) {
  const panel = p.panels.get(p.panel);
  if (!panel) return null;
  return (
    <div className={cx(s.group, s.maximized)}>
      <div className={s.tabs}>
        <div className={s.tabList}>
          <span className={s.tab} data-active="true">
            {panel.icon}
            <span>{panel.title}</span>
          </span>
        </div>
        <div className={s.groupTools}>
          <IconButton label="还原" size="sm" onClick={() => p.dispatch({ type: "maximize", panel: null })}>
            <Minimize2 size={14} />
          </IconButton>
        </div>
      </div>
      <div className={s.body}>{p.renderPanel(panel, true)}</div>
    </div>
  );
}

function NodeView(p: DockProps & { node: LayoutNode }) {
  return p.node.type === "group" ? <GroupView {...p} group={p.node} /> : <SplitView {...p} split={p.node} />;
}

// ---- split ----

function SplitView(p: DockProps & { split: SplitNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const { split } = p;
  const startDrag = (i: number) => (e: PointerEvent<HTMLDivElement>) => {
    const el = ref.current;
    if (!el) return;
    e.preventDefault();
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
    const rect = el.getBoundingClientRect();
    const total = split.dir === "row" ? rect.width : rect.height;
    const start = split.dir === "row" ? e.clientX : e.clientY;
    const base = [...split.sizes];
    const min = 160 / total; // 组最小 160px
    const move = (ev: globalThis.PointerEvent) => {
      const d = ((split.dir === "row" ? ev.clientX : ev.clientY) - start) / total;
      const pair = base[i] + base[i + 1];
      const a = Math.min(Math.max(base[i] + d, min), pair - min);
      const sizes = [...base];
      sizes[i] = a;
      sizes[i + 1] = pair - a;
      p.dispatch({ type: "resize", split: split.id, sizes });
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };
  return (
    <div ref={ref} className={cx(s.split, s[`split_${split.dir}`])}>
      {split.children.map((c, i) => (
        <SplitChild key={c.id} grow={split.sizes[i] ?? 1 / split.children.length}>
          <NodeView {...p} node={c} />
          {i < split.children.length - 1 && (
            <div
              className={s.splitHandle}
              data-dir={split.dir}
              role="separator"
              aria-orientation={split.dir === "row" ? "vertical" : "horizontal"}
              onPointerDown={startDrag(i)}
              onDoubleClick={() => p.dispatch({ type: "equalize", split: split.id })}
            />
          )}
        </SplitChild>
      ))}
    </div>
  );
}

function SplitChild({ grow, children }: { grow: number; children: ReactNode }) {
  return (
    <div className={s.splitChild} style={{ flexGrow: grow, flexBasis: 0 }}>
      {children}
    </div>
  );
}

// ---- group ----

function GroupView(p: DockProps & { group: GroupNode }) {
  const { group } = p;
  const [drop, setDrop] = useState<DropEdge | null>(null);
  const [insertAt, setInsertAt] = useState<number | null>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const active = p.panels.get(group.active) ?? p.panels.get(group.tabs[0]);

  const edgeOf = (e: DragEvent) => {
    const r = bodyRef.current?.getBoundingClientRect();
    if (!r) return "center" as DropEdge;
    const x = (e.clientX - r.left) / r.width;
    const y = (e.clientY - r.top) / r.height;
    if (x > 1 / 3 && x < 2 / 3 && y > 1 / 3 && y < 2 / 3) return "center";
    const d = { left: x, right: 1 - x, top: y, bottom: 1 - y };
    return (Object.entries(d).sort((a, b) => a[1] - b[1])[0][0] as DropEdge) ?? "center";
  };
  const isPanelDrag = (e: DragEvent) => e.dataTransfer.types.includes(PANEL_MIME);

  return (
    <div className={s.group}>
      <div
        className={s.tabs}
        onDragOver={(e) => {
          if (!isPanelDrag(e)) return;
          e.preventDefault();
          const tabs = [...(e.currentTarget.querySelectorAll("[data-tab]") as NodeListOf<HTMLElement>)];
          let i = tabs.findIndex((t) => {
            const r = t.getBoundingClientRect();
            return e.clientX < r.left + r.width / 2;
          });
          if (i < 0) i = tabs.length;
          setInsertAt(i);
        }}
        onDragLeave={() => setInsertAt(null)}
        onDrop={(e) => {
          const id = e.dataTransfer.getData(PANEL_MIME);
          setInsertAt(null);
          if (!id) return;
          e.preventDefault();
          e.stopPropagation();
          p.dispatch({ type: "move", panel: id, group: group.id, edge: "center", index: insertAt ?? group.tabs.length });
        }}
      >
        <div className={s.tabList} role="tablist">
          {group.tabs.map((id, i) => {
            const pn = p.panels.get(id);
            if (!pn) return null;
            return (
              <span key={id} style={{ display: "contents" }}>
                {insertAt === i && <span className={s.tabInsert} />}
                <button
                  type="button"
                  role="tab"
                  data-tab
                  aria-selected={id === group.active}
                  data-active={id === group.active || undefined}
                  className={s.tab}
                  draggable
                  title={pn.title}
                  aria-label={pn.title}
                  onDragStart={(e) => {
                    e.dataTransfer.setData(PANEL_MIME, id);
                    e.dataTransfer.effectAllowed = "move";
                  }}
                  onDragEnd={(e) => {
                    // 没落到任何放置目标（dropEffect = none）且松手点在窗口内：变成浮动面板
                    if (e.dataTransfer.dropEffect === "none" && e.clientX > 0 && e.clientY > 0) {
                      p.dispatch({ type: "float", panel: id, x: Math.max(8, e.clientX - 160), y: Math.max(8, e.clientY - 16) });
                    }
                  }}
                  onClick={() => p.dispatch({ type: "activate", group: group.id, panel: id })}
                  onAuxClick={(e) => e.button === 1 && p.dispatch({ type: "close", panel: id })}
                >
                  {pn.icon}
                  <span className={s.tabTitle}>{pn.title}</span>
                  <span
                    className={s.tabClose}
                    role="button"
                    aria-label={`关闭 ${pn.title}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      p.dispatch({ type: "close", panel: id });
                    }}
                  >
                    <X size={12} />
                  </span>
                </button>
              </span>
            );
          })}
          {insertAt === group.tabs.length && <span className={s.tabInsert} />}
        </div>
        {active && (
          <div className={s.groupTools}>
            <IconButton label="浮动" size="sm" onClick={() => p.dispatch({ type: "float", panel: active.id, x: 120, y: 120 })}>
              <PictureInPicture2 size={14} />
            </IconButton>
            <IconButton label="最大化" size="sm" onClick={() => p.dispatch({ type: "maximize", panel: active.id })}>
              <Maximize2 size={14} />
            </IconButton>
          </div>
        )}
      </div>
      <div
        ref={bodyRef}
        className={s.body}
        onDragOver={(e) => {
          if (!isPanelDrag(e)) return;
          e.preventDefault();
          setDrop(edgeOf(e));
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node)) setDrop(null);
        }}
        onDrop={(e) => {
          const id = e.dataTransfer.getData(PANEL_MIME);
          const edge = edgeOf(e);
          setDrop(null);
          if (!id) return;
          e.preventDefault();
          e.stopPropagation();
          p.dispatch({ type: "move", panel: id, group: group.id, edge });
        }}
      >
        {group.tabs.map((id) => {
          const pn = p.panels.get(id);
          if (!pn) return null;
          const visible = id === (active?.id ?? "");
          // 非当前标签保持挂载、只是隐藏：切回来时滚动位置与输入不丢
          return (
            <div key={id} hidden={!visible} style={{ height: "100%" }}>
              <ErrorBoundary label={pn.title}>{p.renderPanel(pn, visible)}</ErrorBoundary>
            </div>
          );
        })}
        {drop && (
          <div className={s.drop}>
            <div className={s.dropHint} data-edge={drop} />
          </div>
        )}
      </div>
    </div>
  );
}

// ---- 停靠区边缘：拖动改大小 ----

function DockResize({ dock, size, dispatch }: { dock: DockId; size: number; dispatch: DockProps["dispatch"] }) {
  const onDown = (e: PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const start = dock === "right" ? e.clientX : e.clientY;
    const base = size;
    const move = (ev: globalThis.PointerEvent) => {
      const d = start - (dock === "right" ? ev.clientX : ev.clientY);
      const limit = dock === "right" ? window.innerWidth * 0.7 : window.innerHeight * 0.7;
      dispatch({ type: "dockSize", dock, size: Math.round(Math.min(Math.max(base + d, 240), limit)) });
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };
  return <div className={s.dockResize} data-dock={dock} role="separator" onPointerDown={onDown} />;
}

// ---- 浮动面板 ----

export function Floats(p: Omit<DockProps, "dock" | "empty">) {
  return (
    <>
      {p.state.floats.map((f) => {
        const pn = p.panels.get(f.panel);
        if (!pn) return null;
        return <FloatView key={f.panel} f={f} panel={pn} {...p} />;
      })}
    </>
  );
}

function FloatView(p: Omit<DockProps, "dock" | "empty"> & { f: LayoutState["floats"][number]; panel: Panel }) {
  const { f, panel, dispatch } = p;
  const drag = useCallback(
    (mode: "move" | "size") => (e: PointerEvent<HTMLElement>) => {
      if ((e.target as HTMLElement).closest("button") && mode === "move") return;
      e.preventDefault();
      const sx = e.clientX;
      const sy = e.clientY;
      const base = { ...f };
      const move = (ev: globalThis.PointerEvent) => {
        const dx = ev.clientX - sx;
        const dy = ev.clientY - sy;
        if (mode === "move")
          dispatch({
            type: "moveFloat",
            panel: f.panel,
            x: Math.min(Math.max(base.x + dx, 0), window.innerWidth - 80),
            y: Math.min(Math.max(base.y + dy, 0), window.innerHeight - 40),
          });
        else dispatch({ type: "moveFloat", panel: f.panel, x: base.x, y: base.y, w: Math.max(240, base.w + dx), h: Math.max(160, base.h + dy) });
      };
      const up = () => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
    },
    [dispatch, f],
  );
  return (
    <div className={s.float} style={{ left: f.x, top: f.y, width: f.w, height: f.h }} role="dialog" aria-label={panel.title}>
      <div className={s.floatHead} onPointerDown={drag("move")}>
        {panel.icon}
        <span className={s.floatTitle}>{panel.title}</span>
        <IconButton label="停靠到右侧" size="sm" onClick={() => dispatch({ type: "moveToDock", panel: f.panel, dock: "right" })}>
          <PictureInPicture2 size={14} />
        </IconButton>
        <IconButton label="关闭" size="sm" onClick={() => dispatch({ type: "close", panel: f.panel })}>
          <X size={14} />
        </IconButton>
      </div>
      <div className={s.body}>
        <ErrorBoundary label={panel.title}>{p.renderPanel(panel, true)}</ErrorBoundary>
      </div>
      <div className={s.floatResize} onPointerDown={drag("size")} />
    </div>
  );
}

/** 浮动面板放下时也要能接收拖拽：整窗的放置目标，没落在组上的拖拽在这里变成浮动。 */
export function useWindowDrop(dispatch: DockProps["dispatch"]) {
  useEffect(() => {
    const over = (e: globalThis.DragEvent) => {
      if (e.dataTransfer?.types.includes(PANEL_MIME)) e.preventDefault();
    };
    const drop = (e: globalThis.DragEvent) => {
      const id = e.dataTransfer?.getData(PANEL_MIME);
      if (!id) return;
      e.preventDefault();
      dispatch({ type: "float", panel: id, x: Math.max(8, e.clientX - 160), y: Math.max(8, e.clientY - 16) });
    };
    window.addEventListener("dragover", over);
    window.addEventListener("drop", drop);
    return () => {
      window.removeEventListener("dragover", over);
      window.removeEventListener("drop", drop);
    };
  }, [dispatch]);
}
