// 左栏（前端方案第五节 ① ② ⑧）：顶部菜单区（新建会话、搜索、插件市场 + 插件贡献的 nav.primary 菜单项），
// 中间会话列表（按置顶 / 今天 / 昨天 / 本周 / 更早分组，运行中转圈、待确认强调色圆点），底部项目名与设置入口（avatar.menu 贡献仍从齿轮打开）。
// 可折叠成图标条（Mod+B），宽度可拖。

import { useMemo, useRef, useState, type ReactNode } from "react";
import { FolderClosed, Info, Keyboard, MoreHorizontal, PanelLeftClose, PanelLeftOpen, Plus, Puzzle, Search, Settings, Sun } from "lucide-react";
import { groupByDate, type SessionInfo } from "../../client";
import { useInteractions, useSessions, useStore } from "../../client/react";
import { Count, IconButton, Kbd, Menu, MenuList, Popover, Spinner, Tooltip, cx, type MenuGroupSpec } from "../../ui";
import { Slot, useCommandContext, useRegistry, useShell, whenState } from "../context";
import type { MenuItem, MenuPlace } from "../contributions";
import { bindingOf } from "../commands";
import { useViewport } from "../viewport";
import s from "./sidebar.module.css";

/** 某个菜单位置的菜单项 → Menu 的分组（按 group 分组）。 */
export function useMenuGroups(place: MenuPlace, extra?: { row?: SessionInfo }): () => MenuGroupSpec[] {
  const sh = useShell();
  const items = useRegistry(sh.reg.menus);
  const cx0 = useCommandContext();
  return () => {
    const cx1 = { ...cx0, ...extra };
    const groups = new Map<string, MenuGroupSpec>();
    for (const it of items.filter((x) => x.menu === place)) {
      const w = whenState(it.when?.(cx1));
      if (!w.show) continue;
      const g = it.group ?? "default";
      if (!groups.has(g)) groups.set(g, { id: g, items: [] });
      groups.get(g)!.items.push({
        id: it.id,
        label: it.title,
        icon: it.icon,
        danger: it.danger,
        disabled: w.disabled,
        reason: w.reason,
        keybind: it.command ? bindingOf(sh, it.command) : undefined,
        run: () => runMenuItem(it, cx1),
      });
    }
    return [...groups.values()];
  };
}

export function runMenuItem(it: MenuItem, cx: ReturnType<typeof useCommandContext> & { row?: SessionInfo }) {
  if (it.run) return it.run(cx);
  if (it.command) return cx.shell.run(it.command);
  if (it.page) return cx.shell.openPage(it.page);
  if (it.panel) return cx.shell.openPanel(it.panel);
}

export function Sidebar() {
  const sh = useShell();
  const prefs = useStore(sh.prefs);
  const vp = useViewport();
  // md 档位强制图标条；sm 档位左栏只出现在抽屉里，抽屉里总是展开。
  const collapsed = vp === "md" ? true : vp === "sm" ? false : prefs.sidebarCollapsed;
  return (
    <aside
      className={cx(s.sidebar, collapsed && s.collapsed)}
      style={collapsed ? undefined : { width: prefs.sidebarWidth }}
      aria-label={sh.t("sidebar.label")}
    >
      <Brand collapsed={collapsed} />
      <NavMenu collapsed={collapsed} />
      <Slot at="sidebar.section" />
      {!collapsed && <SessionList />}
      <div style={{ flex: collapsed ? 1 : 0 }} />
      <Footer collapsed={collapsed} />
      {!collapsed && vp === "lg" && <ResizeEdge />}
    </aside>
  );
}

function Brand({ collapsed }: { collapsed: boolean }) {
  const sh = useShell();
  return (
    <div className={s.brand}>
      {!collapsed && (
        <Slot
          at="nav.brand"
          kind="single"
          fallback={
            <>
              {sh.brand.mark}
              <span className={s.brandName}>{sh.brand.name}</span>
            </>
          }
        />
      )}
      <span style={{ flex: 1 }} />
      <Tooltip label={collapsed ? sh.t("sidebar.expand") : sh.t("sidebar.collapse")} keybind={bindingOf(sh, "shell.toggleSidebar")}>
        <IconButton label={sh.t("sidebar.toggle")} size="sm" onClick={() => sh.api.run("shell.toggleSidebar")}>
          {collapsed ? <PanelLeftOpen size={16} /> : <PanelLeftClose size={16} />}
        </IconButton>
      </Tooltip>
    </div>
  );
}

/** 菜单区：固定项 + 插件的 nav.primary 菜单项。 */
function NavMenu({ collapsed }: { collapsed: boolean }) {
  const sh = useShell();
  const t = sh.t;
  const items = useRegistry(sh.reg.menus).filter((x) => x.menu === "nav.primary");
  const cx0 = useCommandContext();
  return (
    <nav className={s.nav} aria-label={t("sidebar.menu")}>
      <NavItem collapsed={collapsed} primary icon={<Plus size={16} />} label={t("nav.new")} keybind={bindingOf(sh, "session.new")} onClick={() => sh.api.run("session.new")} />
      <NavItem collapsed={collapsed} icon={<Search size={16} />} label={t("nav.search")} keybind={bindingOf(sh, "shell.searchSessions")} onClick={() => sh.api.run("shell.searchSessions")} />
      {items.map((it) => {
        const w = whenState(it.when?.(cx0));
        if (!w.show) return null;
        const n = it.badge?.(cx0);
        return (
          <NavItem
            key={it.id}
            collapsed={collapsed}
            icon={it.icon ?? <Puzzle size={16} />}
            label={it.title}
            keybind={it.command ? bindingOf(sh, it.command) : undefined}
            disabled={w.disabled}
            reason={w.reason}
            badge={n}
            onClick={() => runMenuItem(it, cx0)}
          />
        );
      })}
    </nav>
  );
}

function NavItem(p: {
  collapsed: boolean;
  primary?: boolean;
  icon: ReactNode;
  label: string;
  keybind?: string;
  badge?: number;
  disabled?: boolean;
  reason?: string;
  onClick: () => void;
}) {
  const btn = (
    <button type="button" className={cx(s.navItem, p.primary && s.navPrimary)} onClick={p.onClick} disabled={p.disabled} title={p.disabled ? p.reason : undefined} aria-label={p.label}>
      {p.icon}
      {!p.collapsed && <span className={s.navLabel}>{p.label}</span>}
      {!p.collapsed && p.badge ? <Count n={p.badge} /> : null}
      {!p.collapsed && p.keybind && <Kbd bind={p.keybind} className={s.navKey} />}
    </button>
  );
  return p.collapsed ? (
    <Tooltip label={p.label} keybind={p.keybind} side="right">
      {btn}
    </Tooltip>
  ) : (
    btn
  );
}

function SessionList() {
  const sh = useShell();
  const t = sh.t;
  const v = useSessions();
  const inter = useInteractions();
  const current = useStore(sh.nav).session;

  const pendingBy = useMemo(() => {
    const m = new Map<string, number>();
    for (const it of inter.open) {
      const root = it.session.split("/")[0];
      m.set(root, (m.get(root) ?? 0) + 1);
    }
    return m;
  }, [inter.open]);

  const groups = [...(v.pinned.length ? [{ bucket: "pinned", items: v.pinned }] : []), ...groupByDate(v.items)];
  const label: Record<string, string> = {
    pinned: t("sessions.pinned"),
    today: t("sessions.today"),
    yesterday: t("sessions.yesterday"),
    week: t("sessions.week"),
    older: t("sessions.older"),
  };

  return (
    <div className={s.list} role="navigation" aria-label={t("sidebar.sessions")}>
      {groups.length === 0 && !v.loading && <div className={s.empty}>{v.error ?? t("sessions.empty")}</div>}
      {groups.map((g) => (
        <div key={g.bucket}>
          <div className={s.bucket}>{label[g.bucket]}</div>
          {g.items.map((row) => (
            <SessionRow key={row.id} row={row} active={row.id === current} pending={pendingBy.get(row.id) ?? 0} />
          ))}
        </div>
      ))}
      {v.next > 0 && (
        <button type="button" className={s.more} onClick={() => void sh.client.sessions.more()}>
          {t("sessions.more")}
        </button>
      )}
      <Slot at="sidebar.empty" />
    </div>
  );
}

function SessionRow({ row, active, pending }: { row: SessionInfo; active: boolean; pending: number }) {
  const sh = useShell();
  const groups = useMenuGroups("session.row.menu", { row });
  const running = row.run_state === "running";
  const child = !!row.parent;
  // 右键菜单：锚点放在鼠标位置，与悬停的 ⋯ 用同一份菜单项。
  const [ctx, setCtx] = useState<{ x: number; y: number } | null>(null);
  const anchor = useRef<HTMLDivElement>(null);
  const closeCtx = () => setCtx(null);
  return (
    <>
      <div
        className={cx(s.row, child && s.rowChild)}
        aria-current={active ? "page" : undefined}
        role="button"
        tabIndex={0}
        onClick={() => sh.api.openSession(row.id)}
        onKeyDown={(e) => {
          if (e.key === "Enter") sh.api.openSession(row.id);
        }}
        onContextMenu={(e) => {
          e.preventDefault();
          setCtx({ x: e.clientX, y: e.clientY });
        }}
        title={row.preview || row.id}
      >
        <Slot
          at="session.row.leading"
          props={{ row }}
          fallback={
            pending ? (
              <span className={s.rowPending} title={sh.t("sidebar.pending")} aria-label={sh.t("sidebar.pending")} />
            ) : running ? (
              <Spinner size={12} />
            ) : null
          }
        />
        <span className={s.rowTitle}>{row.title || row.id}</span>
        <span className={s.rowBadges}>
          <Slot at="session.row.badge" props={{ row }} />
        </span>
        <span className={s.rowHover} onClick={(e) => e.stopPropagation()}>
          <Slot at="session.row.hover" props={{ row }} />
          <Menu
            placement="bottom-end"
            groups={groups}
            trigger={({ ref, toggle }) => (
              <IconButton ref={ref} label={sh.t("sidebar.more")} size="sm" onClick={toggle}>
                <MoreHorizontal size={14} />
              </IconButton>
            )}
          />
        </span>
      </div>
      {ctx && (
        <>
          <div ref={anchor} className={s.ctxAnchor} style={{ left: ctx.x, top: ctx.y }} />
          <Popover anchor={anchor} open placement="bottom-start" onClose={closeCtx}>
            <MenuList groups={groups()} onDone={closeCtx} />
          </Popover>
        </>
      )}
    </>
  );
}

function Footer({ collapsed }: { collapsed: boolean }) {
  const sh = useShell();
  const t = sh.t;
  const hello = useStore(sh.client.hello);
  const extra = useMenuGroups("avatar.menu");
  const groups = (): MenuGroupSpec[] => [
    {
      id: "core",
      items: [
        { id: "settings", label: t("avatar.settings"), icon: <Settings size={14} />, keybind: bindingOf(sh, "shell.settings"), run: () => sh.api.openSettings() },
        { id: "shortcuts", label: t("avatar.shortcuts"), icon: <Keyboard size={14} />, keybind: bindingOf(sh, "shell.shortcuts"), run: () => sh.api.run("shell.shortcuts") },
        { id: "theme", label: t("avatar.theme"), icon: <Sun size={14} />, run: () => sh.api.run("shell.cycleTheme") },
      ],
    },
    ...extra(),
    { id: "about", items: [{ id: "about", label: t("avatar.about"), icon: <Info size={14} />, run: () => sh.api.openSettings("about") }] },
  ];
  return (
    <div className={s.footer}>
      {!collapsed && (
        <span className={s.project} title={hello?.workdir ?? undefined}>
          <FolderClosed size={14} className={s.projectIcon} />
          <span className={s.projectName}>{projectName(hello?.workdir)}</span>
        </span>
      )}
      <Menu
        placement="top-end"
        groups={groups}
        trigger={({ ref, toggle }) => (
          <IconButton ref={ref} label={t("avatar.menu")} size="sm" onClick={toggle}>
            <Settings size={16} />
          </IconButton>
        )}
      />
      {!collapsed && <Slot at="sidebar.footer" />}
    </div>
  );
}

/** 工作目录只显示最后一级（06 §3.2）。 */
function projectName(workdir?: string): string {
  if (!workdir) return "…";
  const parts = workdir.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] ?? workdir;
}

/** 右缘拖动改宽度（264–420）。 */
function ResizeEdge() {
  const sh = useShell();
  const start = useRef<{ x: number; w: number } | null>(null);
  return (
    <div
      className={s.resize}
      role="separator"
      aria-orientation="vertical"
      onPointerDown={(e) => {
        start.current = { x: e.clientX, w: sh.prefs.get().sidebarWidth };
        (e.target as HTMLElement).setPointerCapture(e.pointerId);
      }}
      onPointerMove={(e) => {
        if (!start.current) return;
        const w = Math.max(220, Math.min(420, start.current.w + e.clientX - start.current.x));
        sh.prefs.set("sidebarWidth", w);
      }}
      onPointerUp={() => (start.current = null)}
      onDoubleClick={() => sh.prefs.set("sidebarWidth", 280)}
    />
  );
}
