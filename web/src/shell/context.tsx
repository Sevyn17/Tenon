// 工作台的上下文：一个 Shell 实例的客户端、注册表、贡献包、偏好、布局、文案与宿主接口。
// 区域与内置贡献都从这里取，不用模块级单例（同一页面可以挂多个工作台）。
//
// <Slot>：按插槽种类渲染贡献（list 全部、single 胜出的一个、chain 第一个非空者），每份贡献包一层 ErrorBoundary，
// 抛错只在该位置显示占位、所属包标记为 failed（设置 › 插件里能看到）。

import { createContext, Fragment, useContext, useMemo, type ReactNode } from "react";
import { createStore, type Client, type Store } from "../client";
import sd from "./slotdebug.module.css";
import type { NavState } from "./nav";
import type { ShellBus } from "./bus";
import type { NoticesStore } from "./notices";
import { useStore } from "../client/react";
import { DeclarativeView, ErrorBoundary, type DeclAction } from "../ui";
import type { CommandContext, Registries, ShellApi, SlotItem, SlotKind } from "./contributions";
import type { T } from "./i18n";
import type { LayoutStore } from "./layout/store";
import type { PrefsStore } from "./prefs";
import type { PackHost, Registry, Contribution } from "./registry";

export interface Brand {
  name: string;
  /** 侧栏左上角的标记（图标或图片）。 */
  mark?: ReactNode;
  /** 空会话首屏的问候。 */
  greeting?: string;
  /** 空会话首屏的示例提示。 */
  examples?: string[];
}

export interface ShellState {
  client: Client;
  reg: Registries;
  packs: PackHost;
  prefs: PrefsStore;
  layout: LayoutStore;
  t: T;
  api: ShellApi;
  /** 导航状态（当前会话、主区页面、设置、命令面板）：区域订阅它重渲染。 */
  nav: Store<NavState>;
  /** 区域之间的请求（插入输入框、聚焦输入框）。 */
  bus: ShellBus;
  /** 通知中心（回合完成、需要确认、插件的 notify）。 */
  notices: NoticesStore;
  brand: Brand;
  /** 嵌入方强制的布局预设（createShell 的 preset）；不写进偏好，给了就不看偏好里的 preset。 */
  preset?: string;
  /** 声明式 UI 的动作执行（按钮只能调协议命令 / 开面板 / 填输入框 / 回答交互）。 */
  runDecl: (a: DeclAction, values?: Record<string, unknown>) => Promise<void>;
}

const Ctx = createContext<ShellState | null>(null);

export const ShellProvider = Ctx.Provider;

export function useShell(): ShellState {
  const s = useContext(Ctx);
  if (!s) throw new Error("@ybase/shell：组件必须在 Shell 里使用");
  return s;
}

/** 订阅一个注册表（贡献增删时重渲染）。 */
export function useRegistry<T extends Contribution>(r: Registry<T>): T[] {
  return useStore(r);
}

/** 命令 / 菜单执行时的上下文。 */
export function useCommandContext(): CommandContext {
  const s = useShell();
  return { client: s.client, session: s.api.current(), shell: s.api };
}

/** 插槽高亮（开发者工具）：地址带 ?slots 时打开，也可用命令 dev.slots 切换。打开后每个插槽外面画一圈虚线并标出位置与贡献数。 */
export const slotDebug: Store<boolean> = createStore<boolean>(
  typeof location !== "undefined" && new URLSearchParams(location.search).has("slots"),
);

function SlotFrame({ at, kind, n, children }: { at: string; kind: SlotKind; n: number; children: ReactNode }) {
  const on = useStore(slotDebug);
  if (!on) return <>{children}</>;
  return (
    <span className={sd.slot} data-slot={at} data-empty={n === 0 || undefined}>
      <span className={sd.label}>
        {at} · {kind} · {n}
      </span>
      {children}
    </span>
  );
}

/** 一处插槽。props 原样交给每份贡献的 render。 */
export function Slot(p: Parameters<typeof SlotInner>[0]) {
  const s = useShell();
  const all = useRegistry(s.reg.slots);
  const n = all.filter((x) => x.at === p.at).length;
  return (
    <SlotFrame at={p.at} kind={p.kind ?? "list"} n={n}>
      <SlotInner {...p} />
    </SlotFrame>
  );
}

function SlotInner({
  at,
  kind = "list",
  props = {},
  fallback = null,
  wrap,
}: {
  at: string;
  kind?: SlotKind;
  props?: Record<string, unknown>;
  /** single / chain 没有贡献时显示的内容（通常是内置实现）。 */
  fallback?: ReactNode;
  /** list 里每一项外面再包一层（如工具栏按钮的间距）。 */
  wrap?: (node: ReactNode, item: SlotItem) => ReactNode;
}) {
  const s = useShell();
  const all = useRegistry(s.reg.slots);
  const items = useMemo(() => all.filter((x) => x.at === at), [all, at]);
  // 非 chain 插槽传空位置，不白跑贡献的 render。
  const chain = useChainSlot(kind === "chain" ? at : "", props);
  const one = (it: SlotItem): ReactNode => (
    <ErrorBoundary key={`${it.pack}:${it.id}`} label={`${at} · ${it.id}`} onError={(e) => it.pack && s.packs.fail(it.pack, e.message)}>
      {it.view ? <DeclarativeView node={it.view} onAction={s.runDecl} /> : it.render?.(props)}
    </ErrorBoundary>
  );
  if (kind === "list") {
    if (!items.length) return <>{fallback}</>;
    return <>{items.map((it) => <Fragment key={`${it.pack}:${it.id}`}>{wrap ? wrap(one(it), it) : one(it)}</Fragment>)}</>;
  }
  if (kind === "single") {
    // 同一插槽的 single 贡献按 priority 取最小者（注册表的同 id 替换之外，不同 id 也只显示一个）。
    const win = [...items].sort((a, b) => (a.priority ?? 100) - (b.priority ?? 100))[0];
    return <>{win ? one(win) : fallback}</>;
  }
  return <>{chain ?? fallback}</>;
}

/** chain 插槽求值：依次问，第一个返回非空内容者接管；都不接时返回 null（调用方据此决定要不要让出位置）。 */
export function useChainSlot(at: string, props: Record<string, unknown> = {}): ReactNode | null {
  const s = useShell();
  const all = useRegistry(s.reg.slots);
  const ordered = all.filter((x) => x.at === at).sort((a, b) => (a.priority ?? 100) - (b.priority ?? 100));
  for (const it of ordered) {
    let node: ReactNode = null;
    try {
      node = it.view ? <DeclarativeView node={it.view} onAction={s.runDecl} /> : (it.render?.(props) ?? null);
    } catch (e) {
      if (it.pack) s.packs.fail(it.pack, e instanceof Error ? e.message : String(e));
      continue;
    }
    if (node !== null && node !== undefined && node !== false) {
      return (
        <ErrorBoundary label={`${at} · ${it.id}`} onError={(e) => it.pack && s.packs.fail(it.pack, e.message)}>
          {node}
        </ErrorBoundary>
      );
    }
  }
  return null;
}

/** 某插槽当前是否有贡献（决定要不要留出位置）。 */
export function useSlotFilled(at: string): boolean {
  const s = useShell();
  const all = useRegistry(s.reg.slots);
  return all.some((x) => x.at === at);
}

/** when() 的统一解释：true 可用；false 不显示；字符串 = 停用原因。 */
export function whenState(w: boolean | string | undefined): { show: boolean; disabled: boolean; reason?: string } {
  if (w === undefined || w === true) return { show: true, disabled: false };
  if (w === false) return { show: false, disabled: true };
  return { show: true, disabled: true, reason: w };
}
