// @tenon/shell：可编排的工作台（L3）。createShell 组装一个工作台实例：客户端、注册表、贡献包、偏好、布局、导航、文案，
// 返回可挂载的 React 组件。嵌入方的定制入口都在这里（前端方案第一节"三档定制"）：
//   - 配置：theme / brand / messages / prefs / disable；
//   - 贡献：contributions（贡献包数组，同 id 低 priority 替换内置）；
//   - 布局：preset（或贡献自己的 LayoutPreset / Region）。

import { useEffect, useState, type ReactNode } from "react";
import { createClient, listed, type Client } from "../client";
import { backendPack } from "./backend-ui";
import { ClientProvider, useStore } from "../client/react";
import { Button, Dialog, Input, ToastProvider, applyAccent, applyDensity, applyTheme, isMac, useToast, type ThemePref } from "../ui";
import { createShellBus } from "./bus";
import { createDraftSettings } from "./draft";
import { useKeybindings } from "./commands";
import { ShellProvider, slotDebug, type Brand, type ShellState } from "./context";
import { createRegistries, type ShellApi, type ShellNotice } from "./contributions";
import { createT, type Locale, type Messages } from "./i18n";
import { createLayoutStore } from "./layout/store";
import { sanitize as sanitizeLayout } from "./layout/reducer";
import { createNav } from "./nav";
import { createNotices } from "./notices";
import { watchActivity } from "./notify";
import { currentViewport } from "./viewport";
import { createPrefs, type Prefs } from "./prefs";
import { createPackHost, type ContributionPack } from "./registry";
import { corePack } from "./builtin/core";
import { settingsPack } from "./builtin/settings";
import { panelsPack } from "./builtin/panels";
import { messagesPack } from "./builtin/messages";
import { changesPack, tasksPack, workPanelsPack } from "./builtin/workpanels";
import { modesPagePack, morePagesPack, watchDisabled } from "./builtin/pages";
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
  /** 初始会话（缺省读地址栏 #s=）。 */
  session?: string | null;
  /** 是否把当前会话写进地址栏（缺省是；嵌入到别人页面里时关掉）。 */
  hash?: boolean;
  /** 强制使用的布局预设（LayoutPreset id）；给了就不读偏好里的 preset。 */
  preset?: string;
}

export interface Shell {
  state: ShellState;
  /** 可挂载的根组件。 */
  Root: () => ReactNode;
}

type Asker =
  | { kind: "confirm"; title: string; body?: ReactNode; confirm?: string; danger?: boolean; resolve: (v: boolean) => void }
  | { kind: "prompt"; title: string; label?: string; initial?: string; resolve: (v: string | null) => void };

/** 声明式按钮调用前要确认的命令。 */
const DESTRUCTIVE = new Set(["session.delete", "session.rewind", "session.compact", "plugins.set", "mode.set"]);

export function createShell(o: ShellOptions = {}): Shell {
  const ns = o.ns ?? "tenon";
  const client = o.client ?? createClient();
  const reg = createRegistries();
  const packs = createPackHost();
  const prefs = createPrefs(ns, o.prefs);
  const layout = createLayoutStore(ns);
  const nav = createNav(o.session, { hash: o.hash });
  const bus = createShellBus();
  const brand: Brand = { name: "Tenon", ...o.brand };
  let toastFn: (text: string, tone?: ShellNotice["tone"]) => void = () => {};
  let askFn: (a: Asker) => void = () => {};
  const notices = createNotices();
  const draft = createDraftSettings();

  const api: ShellApi = {
    current: () => nav.get().session,
    openSession: (id) => nav.update((v) => ({ ...v, session: id, page: null, palette: null, drawer: false })),
    closeSession: () => nav.update((v) => ({ ...v, session: null, page: null })),
    // 新建会话 = 回到首页的草稿输入框；发第一条消息时才真正 session.create（不留下空会话）。
    async newSession() {
      nav.update((v) => ({ ...v, session: null, page: null, palette: null, drawer: false }));
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
    draft,
    t,
    api,
    brand,
    preset: o.preset,
    async runDecl(a, values) {
      switch (a.kind) {
        case "call": {
          // 声明式按钮（插件包的界面贡献）能调协议命令：会删数据、改配置的先让用户确认，不让一个按钮悄悄做掉。
          if (DESTRUCTIVE.has(a.cmd)) {
            const ok = await api.confirm({ title: "确认操作", body: `界面贡献请求执行 ${a.cmd}。`, confirm: "执行", danger: true });
            if (!ok) return;
          }
          await client.call(a.cmd as never, { ...(a.args as object), ...(values ?? {}) } as never);
          return;
        }
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

  // 关掉的包：嵌入方的 disable 固定，设置里的开关（prefs.disabledPacks）即时生效。
  watchDisabled(state, o.disable ?? []);
  packs.register(corePack(reg));
  packs.register(shellPack(state));
  packs.register(settingsPack(state));
  packs.register(panelsPack(state));
  packs.register(messagesPack(reg));
  packs.register(workPanelsPack(state));
  packs.register(tasksPack(state));
  packs.register(changesPack(state));
  packs.register(morePagesPack(state));
  packs.register(modesPagePack(state));
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
      // 后端插件包的界面贡献（[[ui]] 段）：每个包一个贡献包，可在设置里单独关。
      try {
        const r = await client.call("packages.list", {});
        for (const pkg of r.packages) if (pkg.ui?.length) packs.register(backendPack(reg, pkg, plugins));
      } catch {
        /* 后端不提供插件包：没有界面贡献 */
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
    useEffect(() => applyAccent(p.accent), [p.accent]);
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
      // 回合尾巴的建议追问（session.followups）：后端用会话模型生成，按回合缓存；偏好里可关。
      ctx.add(sh.reg.followups, {
        id: "model",
        suggest: async (x) => {
          if (!x.session || !x.client.can("followups") || !sh.prefs.get().followups) return [];
          return (await x.client.call("session.followups", { session: x.session, turn: x.turn })).items;
        },
      });
      const cmd = (c: Parameters<typeof sh.reg.commands.add>[0]) => ctx.add(sh.reg.commands, c);
      cmd({
        id: "shell.toggleSidebar",
        title: "切换左栏",
        section: "general",
        keybind: "Mod+B",
        global: true,
        // 窄屏（sm）时左栏是抽屉：开关抽屉，不改折叠偏好。
        run: () =>
          currentViewport() === "sm"
            ? sh.nav.update((v) => ({ ...v, drawer: !v.drawer }))
            : sh.prefs.set("sidebarCollapsed", !sh.prefs.get().sidebarCollapsed),
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
      // 上 / 下一个会话：按左栏列表的顺序（最近更新在前）。
      const stepSession = (dir: -1 | 1) => {
        const items = listed(sh.client.sessions.get());
        if (!items.length) return;
        const i = items.findIndex((x) => x.id === sh.nav.get().session);
        const j = i < 0 ? 0 : Math.min(Math.max(i + dir, 0), items.length - 1);
        if (j !== i) sh.api.openSession(items[j].id);
      };
      cmd({ id: "session.prev", title: "上一个会话", section: "navigation", keybind: "Mod+[", global: true, run: () => stepSession(-1) });
      cmd({ id: "session.next", title: "下一个会话", section: "navigation", keybind: "Mod+]", global: true, run: () => stepSession(1) });
      cmd({
        id: "interaction.next",
        title: "下一个待确认",
        section: "interaction",
        keybind: "Mod+Shift+A",
        global: true,
        when: () => (sh.client.interactions.get().open.length ? true : "没有等你处理的事"),
        run: () => {
          const open = sh.client.interactions.get().open;
          const roots = [...new Set(open.map((x) => x.session.split("/")[0]))];
          if (!roots.length) return;
          const cur = sh.nav.get().session;
          // 当前会话之后的下一个；当前会话本身有待确认且只有它时聚焦输入框位置（接管卡在那里）。
          const i = cur ? roots.indexOf(cur) : -1;
          const next = roots[(i + 1) % roots.length];
          if (next !== cur) sh.api.openSession(next);
          sh.api.focusComposer();
        },
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
      cmd({
        id: "dev.slots",
        title: "开发者：切换插槽高亮",
        section: "general",
        keywords: ["slot", "插槽", "调试", "开发者"],
        run: () => slotDebug.set(!slotDebug.get()),
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
      // 命名布局：保存当前面板布局为一个名字；每个名字一条"切换布局"命令（命令面板里一键切换），随偏好增删。
      cmd({
        id: "layout.save",
        title: "保存当前布局…",
        section: "panels",
        keywords: ["命名布局", "layout"],
        run: async () => {
          const name = (await sh.api.prompt({ title: "保存当前布局", label: "布局名称（同名会覆盖）" }))?.trim();
          if (!name) return;
          sh.prefs.set("layouts", { ...sh.prefs.get().layouts, [name]: sh.layout.get() });
          sh.api.toast(`已保存布局：${name}`, "success");
        },
      });
      const layoutCmds = new Map<string, () => void>();
      const syncLayouts = () => {
        const names = new Set(Object.keys(sh.prefs.get().layouts));
        for (const [n, undo] of layoutCmds) {
          if (!names.has(n)) {
            undo();
            layoutCmds.delete(n);
          }
        }
        for (const n of names) {
          if (layoutCmds.has(n)) continue;
          layoutCmds.set(
            n,
            sh.reg.commands.add({
              id: `layout.apply:${n}`,
              title: `切换布局：${n}`,
              section: "panels",
              keywords: ["命名布局", "layout"],
              pack: "shell",
              run: () => {
                if (!applyLayout(sh, n)) sh.api.toast(`布局 ${n} 已不存在`, "warning");
              },
            }),
          );
        }
      };
      syncLayouts();
      ctx.onDispose(sh.prefs.subscribe(syncLayouts));
      ctx.onDispose(() => {
        for (const undo of layoutCmds.values()) undo();
        layoutCmds.clear();
      });
      cmd({
        id: "shell.escape",
        title: "关闭覆盖层",
        hidden: true,
        keybind: "Escape",
        global: true,
        run: () => {
          const v = sh.nav.get();
          if (v.palette || v.shortcuts || v.page || v.focus || v.drawer) {
            sh.nav.update((x) => ({ ...x, palette: null, shortcuts: false, page: null, focus: false, drawer: false }));
            return;
          }
          return false;
        },
      });
    },
  };
}

/** 切到某个命名布局：按当前已注册的面板清洗快照（插件没了的面板去掉），再整体替换。 */
export function applyLayout(sh: ShellState, name: string): boolean {
  const saved = sh.prefs.get().layouts[name];
  if (!saved) return false;
  const known = new Set(sh.reg.panels.get().map((p) => p.id));
  sh.layout.dispatch({ type: "replace", state: sanitizeLayout(saved, known) });
  return true;
}

export { TenonConversation, type TenonConversationProps } from "./embed";
