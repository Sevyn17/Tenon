// 工作台的全部注册表（前端方案第十三节）。内置贡献与插件贡献走同一组注册表；嵌入方用同 id、更小的 priority 替换内置项，
// 或用 createShell 的 disable 关掉整个内置包。
//
// 一个 Shell 实例一组注册表（createRegistries），不是模块级单例：同一页面可以挂多个工作台互不干扰。

import type { ComponentType, ReactNode } from "react";
import type { Client, Entry, Interaction, SessionInfo } from "../client";
import type { DeclNode, MarkdownExtension, MentionItem, ToolView, UIAction } from "../ui";
import { defineRegistry, type Contribution, type Registry } from "./registry";

// ---- 命令（快捷键是命令的字段，见 keys.ts）----

export type CommandSection = "general" | "session" | "navigation" | "panels" | "composer" | "interaction";

export interface Command extends Contribution {
  title: string;
  description?: string;
  section?: CommandSection;
  keywords?: string[];
  /** 缺省快捷键（"Mod+Shift+K"；连按两次 "Escape Escape"）。用户可在设置里改绑。 */
  keybind?: string;
  /** 也作为输入框的 / 命令。 */
  slash?: string;
  icon?: ReactNode;
  /** 快捷键在输入框聚焦时是否仍生效（缺省否：让路给输入框）。 */
  global?: boolean;
  /** 不出现在命令面板里（只有快捷键）。 */
  hidden?: boolean;
  /** 当前是否可用；不可用时给原因（命令面板显示为停用，不出现点了没反应的条目）。 */
  when?: (cx: CommandContext) => boolean | string;
  /** 返回 false：把按键让给焦点元素。 */
  run: (cx: CommandContext) => void | false | Promise<void>;
}

/** 命令执行时能拿到的东西。 */
export interface CommandContext {
  client: Client;
  session: string | null;
  shell: ShellApi;
}

// ---- 菜单项 ----

/** 菜单位置：左栏菜单区、头像菜单、会话行菜单、对话头部菜单、面板竖栏的"+"、输入框"+"由 ComposerAction 管。 */
export type MenuPlace = "nav.primary" | "avatar.menu" | "session.row.menu" | "conversation.header.menu" | "rail.add";

export interface MenuItem extends Contribution {
  menu: MenuPlace;
  title: string;
  icon?: ReactNode;
  /** 关联的命令：显示它的快捷键，点了执行它。 */
  command?: string;
  /** 打开主区页面（MainPage id）。 */
  page?: string;
  /** 打开面板（Panel id）。 */
  panel?: string;
  run?: (cx: CommandContext & { row?: SessionInfo }) => void;
  when?: (cx: CommandContext & { row?: SessionInfo }) => boolean | string;
  danger?: boolean;
  /** 菜单内分组（同 group 放一起，组间加分隔）。 */
  group?: string;
  /** 角标（菜单区项的数字）。 */
  badge?: (cx: CommandContext) => number | undefined;
}

// ---- 插槽 ----

/** 插槽种类：list 全部渲染；single 胜出的一个；chain 依次问，第一个返回非空者接管。 */
export type SlotKind = "list" | "single" | "chain";

export interface SlotItem extends Contribution {
  at: string;
  /** 渲染；props 由宿主按插槽位置给（见 slots.ts 的 SlotProps）。返回 null = 这次不贡献（chain 里交给下一个）。 */
  render?: (props: Record<string, unknown>) => ReactNode;
  /** 声明式内容（后端插件、无代码贡献）。 */
  view?: DeclNode;
}

// ---- 区域与布局预设 ----

export interface Region extends Contribution {
  render: () => ReactNode;
}

export interface LayoutPreset extends Contribution {
  title: string;
  /** 左栏、主区、面板区用哪些区域（Region id）；null = 不显示。 */
  sidebar: string | null;
  main: string;
  panels: "dock" | "drawer" | "float" | null;
}

// ---- 面板 ----

export interface PanelProps {
  session: string | null;
  params?: unknown;
  /** 面板所在组是否可见（隐藏时可暂停昂贵的更新）。 */
  visible: boolean;
}

export interface PanelCardProps {
  session: string | null;
  open: () => void;
}

export interface Panel extends Contribution {
  title: string;
  icon: ReactNode;
  keybind?: string;
  /** 竖栏角标。 */
  badge?: (cx: CommandContext) => number | undefined;
  /** 需要会话（没有会话时显示空态）。 */
  needsSession?: boolean;
  /** 缺省放在右侧还是底部。 */
  dock?: "right" | "bottom";
  /** 对话内小卡片（④）：对话区够宽且本面板没开时显示的概览组件。组件自己取数据，没有可说的时返回 null；
   *  外框用 PanelCard（点了 open() 打开本面板）。 */
  card?: ComponentType<PanelCardProps>;
  render: (p: PanelProps) => ReactNode;
}

// ---- 主区页面（插件市场等，菜单区项打开，Esc / 返回回到对话）----

export interface MainPage extends Contribution {
  title: string;
  icon?: ReactNode;
  render: () => ReactNode;
}

// ---- 消息与工具 ----

export type ActionTarget = "user" | "assistant" | "tool" | "turn" | "selection";

export interface MessageActionCx extends CommandContext {
  target: ActionTarget;
  /** 条目（user / assistant / tool）或回合（turn）的数据；selection 时是选中的文本。 */
  entryId?: string;
  turn?: number;
  text?: string;
  callId?: string;
}

export interface MessageAction extends Contribution {
  appliesTo: ActionTarget[];
  label: string;
  icon?: ReactNode;
  placement?: "hover" | "menu";
  keybind?: string;
  danger?: boolean;
  when?: (cx: MessageActionCx) => boolean | string;
  run: (cx: MessageActionCx) => void | Promise<void>;
}

export interface ToolRenderer extends Contribution {
  /** 匹配工具名（精确名或以 * 结尾的前缀）。 */
  tool: string;
  view: ToolView;
}

export interface MarkdownExt extends Contribution {
  ext: MarkdownExtension;
}

export interface InteractionRenderer extends Contribution {
  kind: string;
  /** 替换交互卡正文。 */
  render: (it: Interaction) => ReactNode;
}

/** 按条目种类（item.kind）渲染消息流里内置组件不认识的条目（后端插件的自定义条目）。没有渲染器时显示一行说明。 */
export interface EntryRenderer extends Contribution {
  kind: string;
  render: (entry: Entry, cx: CommandContext & { turn: number }) => ReactNode;
}

/** 通知中心里某类通知的样式（按 ShellNotice.kind 匹配；没有 kind 或没有渲染器时用缺省行）。 */
export interface NotificationRenderer extends Contribution {
  kind: string;
  render: (n: ShellNotice, cx: CommandContext) => ReactNode;
}

export interface FollowupProvider extends Contribution {
  /** 某回合结束后的建议追问。 */
  suggest: (cx: CommandContext & { turn: number }) => string[] | Promise<string[]>;
}

// ---- 输入框 ----

/** "@" 提及的候选来源（会话、文件、插件给的东西）。按查询同步返回；没有候选返回空数组。 */
export interface MentionProvider extends Contribution {
  /** 分组标题。 */
  group: string;
  when?: (cx: CommandContext) => boolean | string;
  suggest: (query: string, cx: CommandContext) => MentionItem[];
}

export interface ComposerAction extends Contribution {
  group: "add" | "command";
  title: string;
  description?: string;
  icon?: ReactNode;
  slash?: string;
  keywords?: string[];
  when?: (cx: CommandContext) => boolean | string;
  /** 返回字符串：填进编辑区。 */
  run: (cx: CommandContext & { insert: (t: string) => void; attach: (f: File[]) => void }) => void | string | Promise<void | string>;
}

// ---- 设置（三级：整页 / 宿主页里一节 / 某节几行）----

export interface SettingsPage extends Contribution {
  title: string;
  icon?: ReactNode;
  /** 页面分组：app（工作台偏好）/ backend（后端与插件）/ plugin（插件整页）/ about。 */
  group: "app" | "backend" | "plugin" | "about";
  /** 搜索用的条目（不挂载页面也能被搜到）。 */
  entries?: { id: string; title: string; keywords?: string[] }[];
  render?: () => ReactNode;
}

export interface SettingsSection extends Contribution {
  page: string;
  title: string;
  description?: string;
  entries?: { id: string; title: string; keywords?: string[] }[];
  render: () => ReactNode;
}

// ---- 通知 ----

export interface ShellNotice {
  id: string;
  title: string;
  body?: string;
  tone: "info" | "success" | "warning" | "danger";
  /** 通知类别（NotificationRenderer 按它匹配）：内置有 turn.done、interaction；插件自定。 */
  kind?: string;
  /** 附带数据（给渲染器用）。 */
  data?: unknown;
  session?: string;
  at: number;
  read?: boolean;
}

// ---- 宿主暴露给贡献的接口 ----

export interface ShellApi {
  /** 当前会话 id；没有时为 null。 */
  current(): string | null;
  openSession(id: string): void;
  newSession(): Promise<void>;
  openPanel(id: string, params?: unknown): void;
  togglePanel(id: string): void;
  closePanel(id: string): void;
  openPage(id: string | null): void;
  openSettings(page?: string, entry?: string): void;
  closeSettings(): void;
  openPalette(mode?: "commands" | "sessions"): void;
  /** 往当前输入框插入文本（不发送）。 */
  insert(text: string): void;
  focusComposer(): void;
  run(command: string): void;
  notify(n: Omit<ShellNotice, "id" | "at">): void;
  toast(text: string, tone?: ShellNotice["tone"]): void;
  /** 二次确认（破坏性操作）。 */
  confirm(o: { title: string; body?: ReactNode; confirm?: string; danger?: boolean }): Promise<boolean>;
  /** 让用户输入一行文字。 */
  prompt(o: { title: string; label?: string; initial?: string }): Promise<string | null>;
}

// ---- 全部注册表 ----

export interface Registries {
  commands: Registry<Command>;
  menus: Registry<MenuItem>;
  slots: Registry<SlotItem>;
  regions: Registry<Region>;
  presets: Registry<LayoutPreset>;
  panels: Registry<Panel>;
  pages: Registry<MainPage>;
  messageActions: Registry<MessageAction>;
  toolRenderers: Registry<ToolRenderer>;
  markdown: Registry<MarkdownExt>;
  interactionRenderers: Registry<InteractionRenderer>;
  entryRenderers: Registry<EntryRenderer>;
  notificationRenderers: Registry<NotificationRenderer>;
  followups: Registry<FollowupProvider>;
  composerActions: Registry<ComposerAction>;
  mentions: Registry<MentionProvider>;
  settingsPages: Registry<SettingsPage>;
  settingsSections: Registry<SettingsSection>;
}

export function createRegistries(): Registries {
  return {
    commands: defineRegistry("command"),
    menus: defineRegistry("menu"),
    slots: defineRegistry("slot"),
    regions: defineRegistry("region"),
    presets: defineRegistry("preset"),
    panels: defineRegistry("panel"),
    pages: defineRegistry("page"),
    messageActions: defineRegistry("message-action"),
    toolRenderers: defineRegistry("tool-renderer"),
    markdown: defineRegistry("markdown"),
    interactionRenderers: defineRegistry("interaction-renderer"),
    entryRenderers: defineRegistry("entry-renderer"),
    notificationRenderers: defineRegistry("notification-renderer"),
    followups: defineRegistry("followup"),
    composerActions: defineRegistry("composer-action"),
    mentions: defineRegistry("mention"),
    settingsPages: defineRegistry("settings-page"),
    settingsSections: defineRegistry("settings-section"),
  };
}

/** 消息操作 → 组件用的 UIAction（按 when 过滤，停用的带原因）。 */
export function toUIActions(list: MessageAction[], cx: MessageActionCx): UIAction[] {
  const out: UIAction[] = [];
  for (const a of list) {
    if (!a.appliesTo.includes(cx.target)) continue;
    const w = a.when ? a.when(cx) : true;
    if (w === false) continue;
    out.push({
      id: a.id,
      label: a.label,
      icon: a.icon,
      placement: a.placement ?? "menu",
      keybind: a.keybind,
      danger: a.danger,
      disabled: typeof w === "string",
      reason: typeof w === "string" ? w : undefined,
      run: () => void a.run(cx),
    });
  }
  return out;
}
