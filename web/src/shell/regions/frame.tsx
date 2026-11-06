// 工作台外框（前端方案第五节）：左栏 | 主区（对话 + 底部停靠区）| 右侧停靠区 | 面板竖栏，外加浮动面板、
// 命令面板、快捷键速查、连接状态条。左栏与主区的实现取自 Region 注册表（嵌入方可替换），布局预设决定显示哪些。

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { PanelRight, PanelBottom, WifiOff } from "lucide-react";
import { useConnection, useSessions, useStore } from "../../client/react";
import { Count, Dialog, EmptyState, ErrorBoundary, Kbd, Spinner, Tooltip } from "../../ui";
import { Slot, useCommandContext, useRegistry, useShell, whenState } from "../context";
import type { Command, Panel } from "../contributions";
import { bindingOf, effectiveBindings, runCommand } from "../commands";
import { Dock, Floats, useWindowDrop } from "../layout/dock";
import { isOpen } from "../layout/reducer";
import { Settings } from "./settings";
import { useViewport } from "../viewport";
import s from "./frame.module.css";

export function Frame() {
  const sh = useShell();
  const nav = useStore(sh.nav);
  const regions = useRegistry(sh.reg.regions);
  const presets = useRegistry(sh.reg.presets);
  const panels = useRegistry(sh.reg.panels);
  const layout = useStore(sh.layout);
  const prefs = useStore(sh.prefs);
  const preset = presets.find((x) => x.id === (sh.preset || prefs.preset)) ?? presets[0];
  const sidebar = preset?.sidebar ? regions.find((r) => r.id === preset.sidebar) : undefined;
  const main = regions.find((r) => r.id === (preset?.main ?? "main"));
  const panelMap = useMemo(() => new Map(panels.map((p) => [p.id, p])), [panels]);

  // 面板注册变化后清洗布局（插件停用时它的面板消失）。
  useEffect(() => sh.layout.setKnown(new Set(panels.map((p) => p.id))), [sh.layout, panels]);
  useWindowDrop(sh.layout.dispatch);

  const renderPanel = (p: Panel, visible: boolean): ReactNode => (
    <ErrorBoundary label={`面板 ${p.title}`} resetKey={nav.session} onError={(e) => p.pack && sh.packs.fail(p.pack, e.message)}>
      {p.needsSession && !nav.session ? (
        <EmptyState title={sh.t("panel.needsSession")} />
      ) : (
        p.render({ session: nav.session, visible, params: layout.params[p.id] })
      )}
    </ErrorBoundary>
  );

  const focus = nav.focus;
  const vp = useViewport();
  const narrow = vp === "sm";
  // panels：dock = 停靠区 + 竖栏；float = 只有浮动面板（embed）；drawer = 右侧停靠区当抽屉、不显示底部区与竖栏（chat）；null = 不要面板。
  // 窄屏（sm）一律按 drawer：右侧停靠区盖在对话上，没有底部区与竖栏（面板从命令面板 / 对话头部菜单打开）。
  const mode = narrow && preset?.panels === "dock" ? "drawer" : preset ? preset.panels : "dock";
  const docked = mode === "dock";
  const drawer = mode === "drawer";
  const closeDrawer = () => sh.nav.update((v) => ({ ...v, drawer: false }));
  return (
    <div className={s.frame} data-viewport={vp}>
      {!focus && sidebar && !narrow && <ErrorBoundary label="左栏">{sidebar.render()}</ErrorBoundary>}
      {!focus && sidebar && narrow && nav.drawer && (
        <LeftDrawer onClose={closeDrawer}>
          <ErrorBoundary label="左栏">{sidebar.render()}</ErrorBoundary>
        </LeftDrawer>
      )}
      <div className={s.center}>
        <ConnectionBanner />
        <Announcer />
        <div className={s.mainWrap}>
          <ErrorBoundary label="主区" resetKey={nav.session}>
            {main?.render()}
          </ErrorBoundary>
        </div>
        {!focus && docked && (
          <Dock dock="bottom" state={layout} dispatch={sh.layout.dispatch} panels={panelMap} renderPanel={renderPanel} />
        )}
      </div>
      {!focus && docked && (
        <Dock dock="right" state={layout} dispatch={sh.layout.dispatch} panels={panelMap} renderPanel={renderPanel} />
      )}
      {!focus && drawer && layout.docks.right && !layout.hidden.right && (
        // 抽屉盖在对话上：点遮罩或 Esc 收起右侧停靠区（树保留，下次打开面板时原样回来）。
        <RightDrawer onClose={() => sh.layout.dispatch({ type: "toggleDock", dock: "right" })}>
          <Dock dock="right" state={layout} dispatch={sh.layout.dispatch} panels={panelMap} renderPanel={renderPanel} />
        </RightDrawer>
      )}
      {!focus && docked && <Rail panels={panels} />}
      {mode !== null && <Floats state={layout} dispatch={sh.layout.dispatch} panels={panelMap} renderPanel={renderPanel} />}
      <Slot at="shell.overlay" />
      {nav.palette && <Palette mode={nav.palette} />}
      {nav.shortcuts && <Shortcuts />}
      {nav.settings && <Settings />}
    </div>
  );
}

// ---- 窄屏左抽屉 ----

/** 窄屏的会话栏抽屉：模态（全局快捷键分发在模态框打开时让路），所以 Esc 由它自己处理；打开时焦点移进来，关闭时还回去。 */
function LeftDrawer({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  const sh = useShell();
  const ref = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.isComposing) return;
      e.preventDefault();
      e.stopPropagation();
      close.current();
    };
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      prev?.focus?.();
    };
  }, []);
  return (
    <div className={s.drawerScrim} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={ref} tabIndex={-1} className={s.drawerLeft} role="dialog" aria-modal="true" aria-label={sh.t("sidebar.label")}>
        {children}
      </div>
    </div>
  );
}

/** 右侧抽屉（drawer 模式）：遮罩 + Esc 收起。面板里的输入框按 Esc 也会收起，和左栏抽屉一致。 */
function RightDrawer({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.isComposing || document.querySelector('[aria-modal="true"]')) return;
      e.preventDefault();
      e.stopPropagation();
      close.current();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);
  return (
    <>
      <div className={s.drawerScrim} onMouseDown={() => close.current()} />
      <div className={s.drawerRight}>{children}</div>
    </>
  );
}

// ---- 面板竖栏 ----

function Rail({ panels }: { panels: Panel[] }) {
  const sh = useShell();
  const layout = useStore(sh.layout);
  const cx0 = useCommandContext();
  const right = panels.filter((p) => (p.dock ?? "right") === "right");
  const bottom = panels.filter((p) => p.dock === "bottom");
  const btn = (p: Panel) => {
    const on = isOpen(layout, p.id);
    const n = p.badge?.(cx0);
    return (
      <Tooltip key={p.id} label={p.title} keybind={bindingOf(sh, `panel.${p.id}`) ?? p.keybind} side="left">
        <button
          type="button"
          className={s.railBtn}
          aria-pressed={on}
          aria-label={p.title}
          onClick={() => sh.api.togglePanel(p.id)}
          draggable
          onDragStart={(e) => {
            e.dataTransfer.setData("application/x-ybase-panel", p.id);
            e.dataTransfer.effectAllowed = "move";
          }}
        >
          {p.icon}
          {n ? <Count n={n} className={s.railBadge} /> : null}
        </button>
      </Tooltip>
    );
  };
  return (
    <nav className={s.rail} aria-label={sh.t("rail.label")}>
      {right.map(btn)}
      {bottom.length > 0 && <span className={s.railSep} />}
      {bottom.map(btn)}
      <span className={s.railGrow} />
      <Slot at="rail.bottom" />
      <Tooltip label={sh.t("rail.bottom")} keybind={bindingOf(sh, "shell.toggleBottom")} side="left">
        <button type="button" className={s.railBtn} aria-label={sh.t("rail.bottom")} aria-pressed={!!layout.docks.bottom && !layout.hidden.bottom} onClick={() => sh.api.run("shell.toggleBottom")}>
          <PanelBottom size={16} />
        </button>
      </Tooltip>
      <Tooltip label={sh.t("rail.right")} keybind={bindingOf(sh, "shell.togglePanels")} side="left">
        <button type="button" className={s.railBtn} aria-label={sh.t("rail.right")} aria-pressed={!!layout.docks.right && !layout.hidden.right} onClick={() => sh.api.run("shell.togglePanels")}>
          <PanelRight size={16} />
        </button>
      </Tooltip>
    </nav>
  );
}

// ---- 连接状态条 ----

function ConnectionBanner() {
  const sh = useShell();
  const st = useConnection();
  if (st === "open" || st === "connecting") return null;
  return (
    <div className={s.banner} role="status">
      {st === "reconnecting" ? <Spinner size={12} /> : <WifiOff size={14} />}
      {st === "reconnecting" ? sh.t("conv.reconnecting") : sh.t("conn.closed")}
    </div>
  );
}

// ---- 命令面板（⌘K 命令 / ⌘P 会话）----

interface PaletteRow {
  id: string;
  group: string;
  title: string;
  desc?: string;
  keybind?: string;
  disabled?: boolean;
  reason?: string;
  run: () => void;
}

function Palette({ mode }: { mode: "commands" | "sessions" }) {
  const sh = useShell();
  const cmds = useRegistry(sh.reg.commands);
  const sessions = useSessions();
  const cx0 = useCommandContext();
  const [q, setQ] = useState(mode === "commands" ? "" : "");
  const [active, setActive] = useState(0);
  const [m, setM] = useState(mode);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => input.current?.focus(), []);
  const close = () => sh.nav.update((v) => ({ ...v, palette: null }));

  const rows = useMemo<PaletteRow[]>(() => {
    const needle = q.trim().toLowerCase();
    const hit = (...xs: (string | undefined)[]) => !needle || xs.some((x) => x?.toLowerCase().includes(needle));
    if (m === "sessions") {
      return sessions.items
        .filter((x) => hit(x.title, x.id, x.preview))
        .slice(0, 50)
        .map((x) => ({ id: x.id, group: sh.t("palette.group.sessions"), title: x.title || x.id, desc: x.preview, run: () => sh.api.openSession(x.id) }));
    }
    const out: PaletteRow[] = [];
    for (const c of cmds as Command[]) {
      if (c.hidden || !hit(c.title, c.description, c.slash, ...(c.keywords ?? []))) continue;
      const w = whenState(c.when?.(cx0));
      if (!w.show) continue;
      out.push({
        id: c.id,
        group: sh.t(`section.${c.section ?? "general"}`),
        title: c.title,
        desc: c.description,
        keybind: bindingOf(sh, c.id),
        disabled: w.disabled,
        reason: w.reason,
        run: () => void runCommand(c, cx0),
      });
    }
    return out;
  }, [q, m, cmds, sessions.items, cx0, sh]);

  useEffect(() => setActive(0), [q, m]);
  const pick = (r: PaletteRow | undefined) => {
    if (!r || r.disabled) return;
    close();
    r.run();
  };

  let lastGroup = "";
  return (
    <div className={s.palette} onMouseDown={(e) => e.target === e.currentTarget && close()}>
      <div className={s.paletteBox} role="dialog" aria-modal="true" aria-label={sh.t("palette.label")}>
        <input
          ref={input}
          className={s.paletteInput}
          value={q}
          placeholder={m === "sessions" ? sh.t("palette.sessions") : sh.t("palette.placeholder")}
          onChange={(e) => {
            const v = e.target.value;
            if (m === "sessions" && v.startsWith(">")) {
              setM("commands");
              setQ(v.slice(1));
            } else setQ(v);
          }}
          onKeyDown={(e) => {
            if (e.key === "Escape") close();
            else if (e.key === "ArrowDown") {
              e.preventDefault();
              setActive((i) => Math.min(i + 1, rows.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setActive((i) => Math.max(i - 1, 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              pick(rows[active]);
            } else if (e.key === "Backspace" && !q && m === "commands" && mode === "sessions") setM("sessions");
          }}
          aria-activedescendant={rows[active] ? `pal-${rows[active].id}` : undefined}
        />
        <div className={s.paletteList} role="listbox">
          {rows.length === 0 && <div className={s.paletteEmpty}>{sh.t("palette.empty")}</div>}
          {rows.map((r, i) => {
            const head = r.group !== lastGroup ? <div className={s.paletteGroup}>{r.group}</div> : null;
            lastGroup = r.group;
            return (
              <div key={r.id}>
                {head}
                <button
                  id={`pal-${r.id}`}
                  type="button"
                  role="option"
                  aria-selected={i === active}
                  data-active={i === active}
                  className={s.paletteItem}
                  disabled={r.disabled}
                  title={r.reason}
                  onMouseEnter={() => setActive(i)}
                  onClick={() => pick(r)}
                >
                  <span className={s.grow}>
                    {r.title} {r.desc && <span className={s.paletteDesc}>{r.desc}</span>}
                  </span>
                  {r.reason ? <span className={s.paletteDesc}>{r.reason}</span> : r.keybind ? <Kbd bind={r.keybind} /> : null}
                </button>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

// ---- 快捷键速查（?）----

function Shortcuts() {
  const sh = useShell();
  const cmds = useRegistry(sh.reg.commands);
  const binds = effectiveBindings(sh);
  const groups = new Map<string, { title: string; bind: string }[]>();
  for (const c of cmds) {
    const b = binds.get(c.id);
    if (!b) continue;
    const g = sh.t(`section.${c.section ?? "general"}`);
    groups.set(g, [...(groups.get(g) ?? []), { title: c.title, bind: b }]);
  }
  const close = () => sh.nav.update((v) => ({ ...v, shortcuts: false }));
  return (
    <Dialog open onClose={close} title={sh.t("shortcuts.title")} wide>
      <div className={s.shortcuts}>
        {[...groups].map(([g, rows]) => (
          <div key={g} className={s.shortcutGroup}>
            <h4>{g}</h4>
            {rows.map((r) => (
              <div key={r.title} className={s.shortcutRow}>
                <span>{r.title}</span>
                <Kbd bind={r.bind} />
              </div>
            ))}
          </div>
        ))}
      </div>
    </Dialog>
  );
}

// ---- 读屏播报（aria-live）：最新一条通知（回合完成、需要确认）----

function Announcer() {
  const sh = useShell();
  const list = useStore(sh.notices);
  return (
    <div className={s.srOnly} role="status" aria-live="polite">
      {list[0]?.title ?? ""}
    </div>
  );
}
