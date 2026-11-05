// @ybase/shell：可编排的工作台（L3）。createShell 组装一个工作台实例：客户端、注册表、贡献包、偏好、布局、导航、文案，
// 返回可挂载的 React 组件。嵌入方的定制入口都在这里（前端方案第一节"三档定制"）：
//   - 配置：theme / brand / messages / prefs / disable；
//   - 贡献：contributions（贡献包数组，同 id 低 priority 替换内置）；
//   - 布局：preset（或贡献自己的 LayoutPreset / Region）。

import { useEffect, useState, type ReactNode } from "react";
import { createClient, type Client } from "../client";
import { ClientProvider, useStore } from "../client/react";
import { Button, Dialog, Input, ToastProvider, applyDensity, applyTheme, isMac, useToast, type ThemePref } from "../ui";
import { createShellBus } from "./bus";
import { useKeybindings } from "./commands";
import { ShellProvider, type Brand, type ShellState } from "./context";
import { createRegistries, type ShellApi, type ShellNotice } from "./contributions";
import { createT, type Locale, type Messages } from "./i18n";
import { createLayoutStore } from "./layout/store";
import { createNav } from "./nav";
import { createNotices } from "./notices";
import { watchActivity } from "./notify";
import { createPrefs, type Prefs } from "./prefs";
import { createPackHost, type ContributionPack } from "./registry";
import { corePack } from "./builtin/core";
import { settingsPack } from "./builtin/settings";
import { panelsPack } from "./builtin/panels";
import { Frame } from "./regions/frame";

export * from "./registry";
export * from "./contributions";
export * from "./context";
export type { Prefs } from "./prefs";

export interface ShellOptions {
  client?: Client;
  /** 存储命名空间（偏好、布局、草稿的 localStorage 键前缀）。 */
  ns?: string;
  brand?: Partial<Brand>;
  /** 额外的贡献包。 */
  contributions?: ContributionPack[];
  /** 关掉的内置包 id。 */
  disable?: string[];
  /** 偏好初值（用户改过的以存储为准）。 */
  prefs?: Partial<Prefs>;
  messages?: Partial<Record<Locale, Messages>>;
}

export interface Shell {
  state: ShellState;
  /** 可挂载的根组件。 */
  Root: () => ReactNode;
}

type Asker =
  | { kind: "confirm"; title: string; body?: ReactNode; confirm?: string; danger?: boolean; resolve: (v: boolean) => void }
  | { kind: "prompt"; title: string; label?: string; initial?: string; resolve: (v: string | null) => void };

export function createShell(o: ShellOptions = {}): Shell {
  const ns = o.ns ?? "ybase";
  const client = o.client ?? createClient();
  const reg = createRegistries();
  const packs = createPackHost();
  const prefs = createPrefs(ns, o.prefs);
  const layout = createLayoutStore(ns);
  const nav = createNav();
  const bus = createShellBus();
  const brand: Brand = { name: "YBase", ...o.brand };
  let toastFn: (text: string, tone?: ShellNotice["tone"]) => void = () => {};
  let askFn: (a: Asker) => void = () => {};
  const notices = createNotices();

  const api: ShellApi = {
    current: () => nav.get().session,
    openSession: (id) => nav.update((v) => ({ ...v, session: id, page: null, palette: null })),
    async newSession() {
      const snap = await client.call("session.create", {});
      client.sessions.upsert({ id: snap.id, title: snap.title, preview: "", run_state: snap.run_state, origin: snap.origin, updated_at: Date.now() });
      api.openSession(snap.id);
      setTimeout(() => api.focusComposer(), 0);
    },
    openPanel: (id, params) => layout.dispatch({ type: "open", panel: id, dock: reg.panels.byId(id)?.dock, params }),
    togglePanel: (id) => layout.dispatch({ type: "toggle", panel: id, dock: reg.panels.byId(id)?.dock }),
    closePanel: (id) => layout.dispatch({ type: "close", panel: id }),
    openPage: (id) => nav.update((v) => ({ ...v, page: id })),
    openSettings: (page = "general", entry) => nav.update((v) => ({ ...v, settings: { page, entry }, palette: null })),
    closeSettings: () => nav.update((v) => ({ ...v, settings: null })),
    openPalette: (mode = "commands") => nav.update((v) => ({ ...v, palette: mode })),
    insert: (text) => bus.emit("insert", { session: "*", text }),
    focusComposer: () => bus.emit("focusComposer", {}),
    run(id) {
      const c = reg.commands.byId(id);
      if (!c) return;
      const cx = { client, session: api.current(), shell: api };
      const w = c.when?.(cx);
      if (w === false || typeof w === "string") return;
      void c.run(cx);
    },
    notify(n) {
      notices.add(n);
      toastFn(n.title, n.tone);
    },
    toast: (text, tone) => toastFn(text, tone),
    confirm: (x) => new Promise((resolve) => askFn({ kind: "confirm", ...x, resolve })),
    prompt: (x) => new Promise((resolve) => askFn({ kind: "prompt", ...x, resolve })),
  };

  const t = createT(prefs.get().locale, o.messages);
  const state: ShellState = {
    client,
    reg,
    packs,
    prefs,
    layout,
    nav,
    bus,
    notices,
    t,
    api,
    brand,
    async runDecl(a, values) {
      switch (a.kind) {
        case "call":
          await client.call(a.cmd as never, { ...(a.args as object), ...(values ?? {}) } as never);
          return;
        case "openPanel":
          api.openPanel(a.panel, a.params);
          return;
        case "insert":
          api.insert(a.text);
          return;
        case "respond":
          await client.interactions.respond(a.interaction, a.response as never);
          return;
      }
    },
  };

  packs.setDisabled(new Set([...(o.disable ?? []), ...prefs.get().disabledPacks]));
  packs.register(corePack(reg));
  packs.register(shellPack(state));
  packs.register(settingsPack(state));
  packs.register(panelsPack(state));
  for (const p of o.contributions ?? []) packs.register(p);

  // 后端环境：握手后用 capabilities；plugins.list 拿到已启用的后端插件（不可用时不检查插件）。
  void client
    .start()
    .then(async (h) => {
      let plugins: Set<string> | null = null;
      try {
        const r = await client.call("plugins.list", {});
        plugins = new Set(r.plugins.filter((p) => p.on).map((p) => p.name));
      } catch {
        plugins = null;
      }
      packs.setEnvironment({ ready: true, capabilities: h.capabilities, plugins });
    })
    .catch(() => packs.setEnvironment({ ready: true, capabilities: [], plugins: null }));

  function Root() {
    return (
      <ClientProvider client={client}>
        <ShellProvider value={state}>
          <ToastProvider>
            <Effects />
            <Frame />
            <Asker />
          </ToastProvider>
        </ShellProvider>
      </ClientProvider>
    );
  }

  /** 主题 / 密度 / 快捷键 / 提示条接线。 */
  function Effects() {
    const p = useStore(prefs);
    const toast = useToast();
    useEffect(() => applyTheme(p.theme), [p.theme]);
    useEffect(() => applyDensity(p.density), [p.density]);
    useEffect(() => {
      document.documentElement.style.setProperty("--conv-width", `${p.chatWidth}px`);
    }, [p.chatWidth]);
    useEffect(() => {
      toastFn = (text, tone) => toast.show(text, { tone: tone ?? "info" });
    }, [toast]);
    useEffect(() => {
      if (p.theme !== "system") return;
      const mq = matchMedia("(prefers-color-scheme: dark)");
      const f = () => applyTheme("system");
      mq.addEventListener("change", f);
      return () => mq.removeEventListener("change", f);
    }, [p.theme]);
    useKeybindings(state, isMac);
    useEffect(() => watchActivity(state), []);
    return null;
  }

  /** confirm / prompt 的对话框。 */
  function Asker() {
    const [a, setA] = useState<Asker | null>(null);
    const [v, setV] = useState("");
    useEffect(() => {
      askFn = (x) => {
        setV(x.kind === "prompt" ? (x.initial ?? "") : "");
        setA(x);
      };
    }, []);
    if (!a) return null;
    const done = (ok: boolean) => {
      if (a.kind === "confirm") a.resolve(ok);
      else a.resolve(ok ? v : null);
      setA(null);
    };
    return (
      <Dialog
        open
        onClose={() => done(false)}
        title={a.title}
        actions={
          <>
            <Button variant="ghost" onClick={() => done(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant={a.kind === "confirm" && a.danger ? "danger" : "primary"} onClick={() => done(true)}>
              {(a.kind === "confirm" && a.confirm) || t("common.confirm")}
            </Button>
          </>
        }
      >
        {a.kind === "confirm" ? (
          a.body
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              done(true);
            }}
          >
            {a.label && <div style={{ marginBottom: 8, color: "var(--fg-2)" }}>{a.label}</div>}
            <Input autoFocus value={v} onChange={(e) => setV(e.target.value)} />
          </form>
        )}
      </Dialog>
    );
  }

  return { state, Root };
}

/** 工作台自身的命令（左栏、面板区、主题、速查、专注模式）：需要偏好、布局、导航，所以在这里而不在 core。 */
function shellPack(sh: ShellState): ContributionPack {
  return {
    id: "shell",
    title: "工作台",
    builtin: true,
    setup(ctx) {
      const cmd = (c: Parameters<typeof sh.reg.commands.add>[0]) => ctx.add(sh.reg.commands, c);
      cmd({
        id: "shell.toggleSidebar",
        title: "切换左栏",
        section: "general",
        keybind: "Mod+B",
        global: true,
        run: () => sh.prefs.set("sidebarCollapsed", !sh.prefs.get().sidebarCollapsed),
      });
      cmd({
        id: "shell.togglePanels",
        title: "切换右侧面板区",
        section: "panels",
        keybind: "Mod+\\",
        global: true,
        run: () => sh.layout.dispatch({ type: "toggleDock", dock: "right" }),
      });
      cmd({
        id: "shell.toggleBottom",
        title: "切换底部面板区",
        section: "panels",
        keybind: "Mod+J",
        global: true,
        run: () => sh.layout.dispatch({ type: "toggleDock", dock: "bottom" }),
      });
      cmd({
        id: "shell.reopenPanel",
        title: "重开最近关闭的面板",
        section: "panels",
        keybind: "Mod+Shift+T",
        global: true,
        run: () => sh.layout.dispatch({ type: "reopen" }),
      });
      cmd({
        id: "shell.focus",
        title: "专注模式",
        section: "general",
        keybind: "Mod+.",
        global: true,
        run: () => sh.nav.update((v) => ({ ...v, focus: !v.focus })),
      });
      cmd({
        id: "shell.shortcuts",
        title: "快捷键速查",
        section: "general",
        keybind: "?",
        run: () => sh.nav.update((v) => ({ ...v, shortcuts: !v.shortcuts })),
      });
      cmd({
        id: "conversation.find",
        title: "对话内查找",
        section: "navigation",
        keybind: "Mod+F",
        global: true,
        when: () => (sh.nav.get().session ? true : "没有打开的会话"),
        run: () => sh.bus.emit("find", {}),
      });
      cmd({
        id: "conversation.prevTurn",
        title: "上一回合",
        section: "navigation",
        keybind: "Alt+ArrowUp",
        when: () => (sh.nav.get().session ? true : "没有打开的会话"),
        run: () => sh.bus.emit("jumpTurn", { dir: -1 }),
      });
      cmd({
        id: "conversation.nextTurn",
        title: "下一回合",
        section: "navigation",
        keybind: "Alt+ArrowDown",
        when: () => (sh.nav.get().session ? true : "没有打开的会话"),
        run: () => sh.bus.emit("jumpTurn", { dir: 1 }),
      });
      cmd({
        id: "shell.cycleTheme",
        title: "切换主题",
        section: "general",
        run: () => {
          const order: ThemePref[] = ["system", "light", "dark"];
          const cur = sh.prefs.get().theme;
          sh.prefs.set("theme", order[(order.indexOf(cur) + 1) % order.length]);
        },
      });
      // 每个面板一个开关命令 panel.<id>（快捷键取面板声明的 keybind，可在设置里改绑），随面板注册增删。
      const panelCmds = new Map<string, () => void>();
      const syncPanels = () => {
        const list = sh.reg.panels.get();
        const ids = new Set(list.map((p) => p.id));
        for (const [id, undo] of panelCmds) {
          if (!ids.has(id)) {
            undo();
            panelCmds.delete(id);
          }
        }
        for (const p of list) {
          if (panelCmds.has(p.id)) continue;
          panelCmds.set(
            p.id,
            sh.reg.commands.add({
              id: `panel.${p.id}`,
              title: `切换面板：${p.title}`,
              section: "panels",
              keybind: p.keybind,
              global: true,
              pack: "shell",
              run: () => sh.api.togglePanel(p.id),
            }),
          );
        }
      };
      syncPanels();
      ctx.onDispose(sh.reg.panels.subscribe(syncPanels));
      ctx.onDispose(() => {
        for (const undo of panelCmds.values()) undo();
        panelCmds.clear();
      });
      cmd({
        id: "shell.escape",
        title: "关闭覆盖层",
        hidden: true,
        keybind: "Escape",
        global: true,
        run: () => {
          const v = sh.nav.get();
          if (v.palette || v.shortcuts || v.page || v.focus) {
            sh.nav.update((x) => ({ ...x, palette: null, shortcuts: false, page: null, focus: false }));
            return;
          }
          return false;
        },
      });
    },
  };
}
