// 内置设置页之二（前端方案第十二节）：快捷键、通知、布局（工作台偏好），权限档位、插件、插件包（后端），开发者。
// 后端插件的配置一律在"插件"页它自己名下：L1 由 plugins.list 的配置 schema 生成表单（plugins.set 未实现时只读并说明），
// 第一方插件可再往 "plugin:<名字>" 页贡献 SettingsSection。前端贡献包也在"插件"页列出，可单独关掉（core 等不可关）。

import { useEffect, useMemo, useState } from "react";
import { Bell, Code, Keyboard, LayoutGrid, Package, Puzzle, ShieldCheck } from "lucide-react";
import { ApiError, type Envelope, type ModeInfo, type PackageInfo, type PluginInfo } from "../../client";
import { useStore } from "../../client/react";
import {
  Badge,
  Button,
  CodeBlock,
  EmptyState,
  Input,
  Kbd,
  SchemaForm,
  Segmented,
  Select,
  SettingsNote,
  SettingsRow,
  SettingsSection,
  Skeleton,
  Switch,
  isMac,
  type JSONSchema,
} from "../../ui";
import type { ContributionPack, PackInfo } from "../registry";
import type { ShellState } from "../context";
import type { SettingsPage } from "../contributions";
import { effectiveBindings } from "../commands";
import { conflicts, normalize } from "../keys";

/** 不能在设置里关掉的内置包（关了设置页本身或基本命令就没了）。 */
export const LOCKED_PACKS = new Set(["core", "shell", "settings", "settings.more"]);

function errText(e: unknown) {
  return e instanceof Error ? e.message : String(e);
}

/** 调一次协议命令：加载中 / 结果 / 错误（unsupported 等原样显示）。 */
function useCall<T>(f: () => Promise<T>, deps: unknown[]): { data?: T; error?: string; loading: boolean; reload: () => void } {
  const [st, setSt] = useState<{ data?: T; error?: string; loading: boolean }>({ loading: true });
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    setSt((x) => ({ ...x, loading: true }));
    f().then(
      (data) => live && setSt({ data, loading: false }),
      (e) => live && setSt({ error: errText(e), loading: false }),
    );
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, n]);
  return { ...st, reload: () => setN((x) => x + 1) };
}

// ---- 快捷键 ----

const SECTION: Record<string, string> = { general: "通用", session: "会话", navigation: "导航", panels: "面板", composer: "输入框", interaction: "交互" };

/** 一次按键 → 快捷键写法（Mod = Mac 上 ⌘、其他平台 Ctrl）。只按修饰键时返回 null。 */
function chordOf(e: KeyboardEvent): string | null {
  if (["Control", "Meta", "Shift", "Alt"].includes(e.key)) return null;
  const parts: string[] = [];
  if (isMac ? e.metaKey : e.ctrlKey) parts.push("Mod");
  if (isMac && e.ctrlKey) parts.push("Ctrl");
  if (e.altKey) parts.push("Alt");
  if (e.shiftKey && e.key.length > 1) parts.push("Shift");
  const k = e.key === " " ? "Space" : e.key.length === 1 ? e.key.toUpperCase() : e.key;
  if (e.shiftKey && e.key.length === 1 && /[A-Z0-9]/i.test(e.key)) parts.push("Shift");
  parts.push(k);
  return parts.join("+");
}

function Keybindings({ sh }: { sh: ShellState }) {
  const prefs = useStore(sh.prefs);
  const cmds = useStore(sh.reg.commands);
  const [q, setQ] = useState("");
  const [rec, setRec] = useState<string | null>(null);
  const binds = effectiveBindings(sh);
  const clash = conflicts(binds);
  const clashOf = (id: string) => {
    const b = binds.get(id);
    if (!b) return [];
    return (clash.get(normalize(b)) ?? []).filter((x) => x !== id);
  };

  useEffect(() => {
    if (!rec) return;
    const on = (e: KeyboardEvent) => {
      e.preventDefault();
      e.stopPropagation();
      if (e.key === "Escape" && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) return setRec(null);
      const c = chordOf(e);
      if (!c) return;
      sh.prefs.set("keybinds", { ...sh.prefs.get().keybinds, [rec]: c });
      setRec(null);
    };
    window.addEventListener("keydown", on, true);
    return () => window.removeEventListener("keydown", on, true);
  }, [rec, sh.prefs]);

  const needle = q.trim().toLowerCase();
  const groups = new Map<string, typeof cmds>();
  for (const c of cmds) {
    if (needle && !c.title.toLowerCase().includes(needle) && !c.id.toLowerCase().includes(needle) && !(binds.get(c.id) ?? "").toLowerCase().includes(needle)) continue;
    const g = SECTION[c.section ?? "general"] ?? "通用";
    groups.set(g, [...(groups.get(g) ?? []), c]);
  }
  const set = (id: string, v: string | undefined) => {
    const next = { ...prefs.keybinds };
    if (v === undefined) delete next[id];
    else next[id] = v;
    sh.prefs.set("keybinds", next);
  };
  return (
    <>
      <SettingsSection description="点“改绑”后按下新的组合键，Esc 取消。改过的可以恢复默认；有冲突的会标出来（同一组合键只会触发其中一个）。">
        <SettingsRow
          id="keybinds.search"
          title="查找命令"
          control={<Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="命令名或按键" aria-label="查找命令" />}
        />
        <SettingsRow
          id="keybinds.reset"
          title="全部恢复默认"
          description={Object.keys(prefs.keybinds).length ? `已改 ${Object.keys(prefs.keybinds).length} 个` : "没有改过"}
          control={
            <Button size="sm" disabled={!Object.keys(prefs.keybinds).length} onClick={() => sh.prefs.set("keybinds", {})}>
              恢复默认
            </Button>
          }
        />
      </SettingsSection>
      {[...groups].map(([g, list]) => (
        <SettingsSection key={g} title={g}>
          {list.map((c) => {
            const b = binds.get(c.id);
            const changed = c.id in prefs.keybinds;
            const other = clashOf(c.id);
            return (
              <SettingsRow
                key={c.id}
                id={`kb.${c.id}`}
                title={c.title}
                description={
                  other.length ? (
                    <span style={{ color: "var(--warning)" }}>与"{other.map((x) => sh.reg.commands.byId(x)?.title ?? x).join("、")}"冲突</span>
                  ) : (
                    c.id
                  )
                }
                control={
                  <>
                    {rec === c.id ? <Badge tone="accent">按下组合键…</Badge> : b ? <Kbd bind={b} /> : <span style={{ color: "var(--fg-3)" }}>未绑定</span>}
                    <Button size="sm" variant="ghost" onClick={() => setRec(rec === c.id ? null : c.id)}>
                      {rec === c.id ? "取消" : "改绑"}
                    </Button>
                    {b && (
                      <Button size="sm" variant="ghost" onClick={() => set(c.id, "")}>
                        解绑
                      </Button>
                    )}
                    {changed && (
                      <Button size="sm" variant="ghost" onClick={() => set(c.id, undefined)}>
                        恢复
                      </Button>
                    )}
                  </>
                }
              />
            );
          })}
        </SettingsSection>
      ))}
    </>
  );
}

// ---- 通知 ----

function Notifications({ sh }: { sh: ShellState }) {
  const p = useStore(sh.prefs);
  const [perm, setPerm] = useState(typeof Notification === "undefined" ? "unsupported" : Notification.permission);
  return (
    <SettingsSection title="提醒">
      <SettingsRow
        id="notify"
        title="系统通知"
        description="回合完成、需要你确认或回答时。通知中心总会记一条。"
        control={
          <Segmented
            label="系统通知"
            value={p.notify}
            onChange={(v) => sh.prefs.set("notify", v)}
            options={[
              { value: "never", label: "从不" },
              { value: "background", label: "窗口在后台时" },
              { value: "always", label: "总是" },
            ]}
          />
        }
      />
      <SettingsRow
        id="notify.permission"
        title="浏览器权限"
        description={perm === "granted" ? "已允许" : perm === "denied" ? "已拒绝：要在浏览器的站点设置里改" : perm === "unsupported" ? "这个环境不支持系统通知" : "还没有询问"}
        control={
          perm === "default" ? (
            <Button size="sm" onClick={() => void Notification.requestPermission().then(setPerm)}>
              请求权限
            </Button>
          ) : null
        }
      />
    </SettingsSection>
  );
}

// ---- 布局 ----

function Layout({ sh }: { sh: ShellState }) {
  const p = useStore(sh.prefs);
  const presets = useStore(sh.reg.presets);
  const cur = presets.find((x) => x.id === p.preset) ?? presets[0];
  return (
    <>
    <SettingsSection title="布局">
      <SettingsRow
        id="preset"
        title="布局预设"
        description="工作台：左栏 + 停靠面板；聊天：面板变抽屉；嵌入：单会话，面板浮动"
        control={
          <Select value={cur?.id ?? ""} onChange={(e) => sh.prefs.set("preset", e.target.value)} aria-label="布局预设">
            {presets.map((x) => (
              <option key={x.id} value={x.id}>
                {x.title}
              </option>
            ))}
          </Select>
        }
      />
      <SettingsRow
        id="layout.reset"
        title="重置面板布局"
        description="关掉全部面板、浮动窗，停靠区恢复默认尺寸"
        control={
          <Button size="sm" onClick={() => sh.layout.reset()}>
            重置
          </Button>
        }
      />
      <SettingsRow
        id="sidebar.reset"
        title="左栏宽度"
        description={`${p.sidebarWidth}px（拖左栏右缘可改，双击恢复）`}
        control={
          <Button size="sm" variant="ghost" disabled={p.sidebarWidth === 280} onClick={() => sh.prefs.set("sidebarWidth", 280)}>
            恢复默认
          </Button>
        }
      />
    </SettingsSection>
    <SettingsSection title="命名布局" description="把当前面板布局存成一个名字，之后在命令面板里输入“切换布局”一键恢复。">
      <SettingsRow
        id="layouts"
        title="保存当前布局"
        control={
          <Button size="sm" onClick={() => sh.api.run("layout.save")}>
            保存…
          </Button>
        }
      />
      {Object.keys(p.layouts).map((n) => (
        <SettingsRow
          key={n}
          id={`layouts.${n}`}
          title={n}
          control={
            <>
              <Button size="sm" onClick={() => sh.api.run(`layout.apply:${n}`)}>
                切换
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  const rest = { ...p.layouts };
                  delete rest[n];
                  sh.prefs.set("layouts", rest);
                }}
              >
                删除
              </Button>
            </>
          }
        />
      ))}
    </SettingsSection>
    </>
  );
}

// ---- 权限档位（需要后端 modes 能力）----

function Modes({ sh }: { sh: ShellState }) {
  const session = useStore(sh.nav).session;
  const list = useCall(() => sh.client.call("mode.list", {}), []);
  const cur = useCall(() => (session ? sh.client.call("mode.get", { session }) : Promise.resolve(null)), [session]);
  const show = useCall(() => (session ? sh.client.call("mode.show", { session }) : Promise.resolve(null)), [session, cur.data?.state.mode]);
  const [busy, setBusy] = useState(false);

  const choose = async (m: ModeInfo) => {
    if (!session) return;
    setBusy(true);
    try {
      try {
        await sh.client.call("mode.set", { session, mode: m.id });
      } catch (e) {
        // 项目档位放宽了内置档位：确认后再切
        if (!(e instanceof ApiError && e.code === "need_confirm")) throw e;
        const ok = await sh.api.confirm({
          title: `切换到 ${m.label}`,
          body: `这是项目档位，会放宽：${(m.widened ?? []).join("、") || e.message}`,
          confirm: "切换",
          danger: true,
        });
        if (!ok) return;
        await sh.client.call("mode.set", { session, mode: m.id, confirm: true });
      }
      sh.api.toast(`档位：${m.label}`, "success");
      cur.reload();
    } catch (e) {
      sh.api.toast(errText(e), "danger");
    } finally {
      setBusy(false);
    }
  };

  if (list.error) return <EmptyState title="读不到档位" >{list.error}</EmptyState>;
  return (
    <>
      <SettingsSection title="当前会话" description={session ? session : "先打开一个会话，才能切换它的档位"}>
        {list.loading ? (
          <SettingsRow title={<Skeleton w={160} />} />
        ) : (
          (list.data?.modes ?? []).map((m) => (
            <SettingsRow
              key={m.id}
              id={`mode.${m.id}`}
              title={
                <>
                  {m.label} {m.project && <Badge tone="warning">项目</Badge>} {cur.data?.state.mode === m.id && <Badge tone="accent">当前</Badge>}
                </>
              }
              description={m.widened?.length ? `放宽：${m.widened.join("、")}` : m.id}
              control={
                <Button size="sm" disabled={!session || busy || cur.data?.state.mode === m.id} onClick={() => void choose(m)}>
                  切换
                </Button>
              }
            />
          ))
        )}
      </SettingsSection>
      {show.data && (
        <SettingsSection title={`取值表：${show.data.mode}`}>
          {show.data.items.map((it) => (
            <SettingsRow key={it.item} title={it.title} description={`${it.item} · 来自 ${it.source}${it.bounded ? " · 有上限" : ""}`} control={<code>{it.value}</code>} />
          ))}
        </SettingsSection>
      )}
    </>
  );
}

// ---- 插件（后端插件 + 前端贡献包）----

/** plugins.list 的配置字段 → JSON Schema（交给 SchemaForm 生成表单）。 */
export function schemaOf(fields: PluginInfo["schema"]): JSONSchema {
  const props: Record<string, JSONSchema> = {};
  for (const f of fields) {
    const t = f.type === "bool" ? "boolean" : /^(u?int|float)/.test(f.type) ? (f.type.startsWith("float") ? "number" : "integer") : f.type === "slice" ? "array" : "string";
    let def: unknown = f.default;
    if (t === "boolean") def = f.default === "true";
    else if ((t === "integer" || t === "number") && f.default !== undefined && f.default !== "") def = Number(f.default);
    props[f.key] = {
      type: t,
      title: f.title || f.key,
      description: f.description,
      default: def,
      ...(t === "array" ? { items: { type: "string" } } : {}),
      ...(f.widget ? { "x-widget": f.widget } : {}),
      ...(f.group ? { "x-group": f.group } : {}),
    };
  }
  return { type: "object", properties: props };
}

const STATE_TEXT: Record<PackInfo["status"]["state"], string> = { pending: "加载中", active: "已启用", disabled: "已停用", failed: "出错" };
const STATE_TONE = { pending: "neutral", active: "success", disabled: "neutral", failed: "danger" } as const;

function Plugins({ sh }: { sh: ShellState }) {
  const r = useCall(() => sh.client.call("plugins.list", {}), []);
  const packs = useStore(sh.packs);
  const prefs = useStore(sh.prefs);
  const sections = useStore(sh.reg.settingsSections);
  const [open, setOpen] = useState<string | null>(null);
  const plugins = r.data?.plugins ?? [];
  const sel = plugins.find((p) => p.name === open);

  if (sel) {
    const mine = sections.filter((x) => x.page === `plugin:${sel.name}`);
    const fronts = packs.filter((p) => p.requires?.plugin === sel.name);
    return (
      <>
        <div style={{ marginBottom: 12 }}>
          <Button size="sm" variant="ghost" onClick={() => setOpen(null)}>
            ‹ 全部插件
          </Button>
        </div>
        <SettingsSection title={sel.name} description={`来源：${sel.source}${sel.default ? " · 缺省启用" : ""}`}>
          <SettingsRow title="状态" control={<Badge tone={sel.on ? "success" : "neutral"}>{sel.on ? "已启用" : "未启用"}</Badge>} />
          <SettingsRow title="作用" description={sel.effects.join("、") || "—"} />
          {fronts.length > 0 && <SettingsRow title="界面贡献" description={fronts.map((p) => `${p.title}（${p.contributes.length} 项）`).join("、")} />}
        </SettingsSection>
        {sel.schema.length > 0 && (
          <SettingsSection title="配置">
            <SchemaForm schema={schemaOf(sel.schema)} value={{}} onChange={() => {}} readOnly />
            <SettingsNote>运行中不能改：编辑 profile.toml 里 [{sel.name}] 一节后重启（plugins.set 暂未实现）。</SettingsNote>
          </SettingsSection>
        )}
        {mine.map((sec) => (
          <SettingsSection key={sec.id} title={sec.title} description={sec.description}>
            {sec.render()}
          </SettingsSection>
        ))}
      </>
    );
  }

  const toggle = (id: string, on: boolean) => {
    const cur = new Set(prefs.disabledPacks);
    if (on) cur.delete(id);
    else cur.add(id);
    sh.prefs.set("disabledPacks", [...cur]);
  };

  return (
    <>
      <SettingsSection title="后端插件" description="由服务端的装配决定（profile.toml）。点开看说明与配置。">
        {r.loading && <SettingsRow title={<Skeleton w={200} />} />}
        {r.error && <SettingsNote>{r.error}</SettingsNote>}
        {plugins.map((p) => (
          <SettingsRow
            key={p.name}
            id={`plugin.${p.name}`}
            title={
              <>
                {p.name} {p.on ? <Badge tone="success">启用</Badge> : <Badge>未启用</Badge>}
              </>
            }
            description={p.effects.join("、") || p.source}
            control={
              <Button size="sm" variant="ghost" onClick={() => setOpen(p.name)}>
                详情
              </Button>
            }
          />
        ))}
      </SettingsSection>
      <SettingsSection title="界面贡献包" description="前端的功能包。依赖某个后端插件的包，插件没装配时自动停用。关掉的包刷新后仍保持关闭。">
        {packs.map((p) => {
          const st = p.status;
          const locked = LOCKED_PACKS.has(p.id);
          const userOff = prefs.disabledPacks.includes(p.id);
          return (
            <SettingsRow
              key={p.id}
              id={`pack.${p.id}`}
              title={
                <>
                  {p.title} <Badge tone={STATE_TONE[st.state]}>{STATE_TEXT[st.state]}</Badge> {p.builtin && <Badge>内置</Badge>}
                </>
              }
              description={
                st.state === "failed"
                  ? `出错：${st.error}`
                  : st.state === "disabled" && !userOff
                    ? st.reason
                    : `${p.id}${p.requires?.plugin ? ` · 需要插件 ${p.requires.plugin}` : ""}${p.requires?.capability ? ` · 需要能力 ${p.requires.capability}` : ""} · ${p.contributes.length} 项贡献`
              }
              control={<Switch checked={!userOff} disabled={locked} onChange={(v) => toggle(p.id, v)} label={locked ? `${p.title}（不可关闭）` : p.title} />}
            />
          );
        })}
      </SettingsSection>
    </>
  );
}

// ---- 插件包 ----

function Packages({ sh }: { sh: ShellState }) {
  const r = useCall(() => sh.client.call("packages.list", {}), []);
  if (r.loading) return <Skeleton w="100%" h={60} />;
  if (r.error) return <EmptyState title="读不到插件包">{r.error}</EmptyState>;
  const list: PackageInfo[] = r.data?.packages ?? [];
  if (!list.length) return <EmptyState icon={<Package size={20} />} title="没有安装插件包" />;
  return (
    <SettingsSection title="已安装">
      {list.map((p) => (
        <SettingsRow
          key={p.name}
          id={`pkg.${p.name}`}
          title={
            <>
              {p.name} <Badge>{p.version}</Badge> <Badge>{p.scope}</Badge>
            </>
          }
          description={`${p.description || "—"} · ${p.contents} · ${p.source}`}
        />
      ))}
    </SettingsSection>
  );
}

// ---- 开发者：事件流、贡献清单 ----

function Developer({ sh }: { sh: ShellState }) {
  const packs = useStore(sh.packs);
  const [events, setEvents] = useState<Envelope[]>([]);
  const [paused, setPaused] = useState(false);
  const [filter, setFilter] = useState("");
  useEffect(() => {
    if (paused) return;
    return sh.client.conn.onEvent((e) => setEvents((xs) => [e, ...xs].slice(0, 200)));
  }, [sh.client, paused]);
  const shown = useMemo(() => {
    const f = filter.trim();
    return f ? events.filter((e) => e.kind.includes(f) || e.session.includes(f)) : events;
  }, [events, filter]);
  return (
    <>
      <SettingsSection title="事件流" description="这个连接收到的原始事件（最近 200 条，新的在上）。只有订阅了的会话会推事件。">
        <SettingsRow
          title="控制"
          control={
            <>
              <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="按 kind 或会话过滤" aria-label="过滤事件" />
              <Button size="sm" variant="ghost" onClick={() => setPaused(!paused)}>
                {paused ? "继续" : "暂停"}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setEvents([])}>
                清空
              </Button>
            </>
          }
        />
        <div style={{ padding: "0 16px 12px", maxHeight: 320, overflow: "auto" }}>
          {shown.length === 0 ? (
            <div style={{ color: "var(--fg-3)", padding: "8px 0" }}>还没有事件</div>
          ) : (
            <CodeBlock
              code={shown.map((e) => `${new Date(e.at).toLocaleTimeString()}  ${e.cursor ?? "·"}  ${e.kind}  ${e.session}  ${JSON.stringify(e.data).slice(0, 160)}`).join("\n")}
            />
          )}
        </div>
      </SettingsSection>
      <SettingsSection title="贡献清单" description="每个贡献包注册了什么（注册表:id）。">
        {packs.map((p) => (
          <SettingsRow key={p.id} title={`${p.title}（${p.id}）`} description={p.contributes.join("  ") || "—"} stacked />
        ))}
      </SettingsSection>
    </>
  );
}

export function morePagesPack(sh: ShellState): ContributionPack {
  return {
    id: "settings.more",
    title: "设置页（快捷键、插件、开发者等）",
    builtin: true,
    setup(ctx) {
      const page = (p: SettingsPage) => ctx.add(sh.reg.settingsPages, p);
      page({
        id: "keybindings",
        order: 2,
        title: "快捷键",
        icon: <Keyboard size={14} />,
        group: "app",
        entries: [
          { id: "keybinds.search", title: "改绑快捷键", keywords: ["快捷键", "shortcut", "keybinding"] },
          { id: "keybinds.reset", title: "快捷键恢复默认" },
        ],
        render: () => <Keybindings sh={sh} />,
      });
      page({
        id: "notifications",
        order: 3,
        title: "通知",
        icon: <Bell size={14} />,
        group: "app",
        entries: [
          { id: "notify", title: "系统通知", keywords: ["提醒", "notification"] },
          { id: "notify.permission", title: "浏览器通知权限" },
        ],
        render: () => <Notifications sh={sh} />,
      });
      page({
        id: "layout",
        order: 4,
        title: "布局",
        icon: <LayoutGrid size={14} />,
        group: "app",
        entries: [
          { id: "preset", title: "布局预设", keywords: ["聊天", "嵌入", "工作台"] },
          { id: "layout.reset", title: "重置面板布局", keywords: ["面板"] },
          { id: "sidebar.reset", title: "左栏宽度" },
        ],
        render: () => <Layout sh={sh} />,
      });
      page({
        id: "plugins",
        order: 20,
        title: "插件",
        icon: <Puzzle size={14} />,
        group: "backend",
        entries: [
          { id: "plugins", title: "后端插件", keywords: ["plugin", "配置"] },
          { id: "packs", title: "界面贡献包", keywords: ["前端", "停用"] },
        ],
        render: () => <Plugins sh={sh} />,
      });
      page({ id: "packages", order: 21, title: "插件包", icon: <Package size={14} />, group: "backend", render: () => <Packages sh={sh} /> });
      page({
        id: "developer",
        order: 90,
        title: "开发者",
        icon: <Code size={14} />,
        group: "about",
        entries: [
          { id: "dev.events", title: "事件流", keywords: ["调试", "event"] },
          { id: "dev.contrib", title: "贡献清单", keywords: ["调试"] },
        ],
        render: () => <Developer sh={sh} />,
      });
    },
  };
}

/** 权限档位页：后端有 modes 能力时才出现。 */
export function modesPagePack(sh: ShellState): ContributionPack {
  return {
    id: "settings.modes",
    title: "权限档位设置页",
    builtin: true,
    requires: { capability: "modes" },
    setup(ctx) {
      ctx.add(sh.reg.settingsPages, {
        id: "modes",
        order: 10,
        title: "权限档位",
        icon: <ShieldCheck size={14} />,
        group: "backend",
        entries: [{ id: "modes", title: "切换档位", keywords: ["权限", "permission", "ask", "workspace"] }],
        render: () => <Modes sh={sh} />,
      });
    },
  };
}

/** 偏好里的 disabledPacks 变了就重新判断各包（设置里的开关即时生效）。 */
export function watchDisabled(sh: ShellState, base: string[]): () => void {
  let last = "";
  const apply = () => {
    const ids = [...new Set([...base, ...sh.prefs.get().disabledPacks.filter((x) => !LOCKED_PACKS.has(x))])].sort();
    const key = ids.join(",");
    if (key === last) return;
    last = key;
    sh.packs.setDisabled(new Set(ids));
  };
  apply();
  return sh.prefs.subscribe(apply);
}
