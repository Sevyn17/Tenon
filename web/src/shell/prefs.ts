// 工作台的界面偏好（前端方案第十二节"界面偏好"）：外观、输入、通知、快捷键改绑、布局。存 localStorage，
// 键带命名空间（嵌入方可改），读不到或解析失败时用缺省值。后端的东西（会话设置、插件配置）不在这里。

import { createStore, type ReadStore } from "../client";
import type { Accent, Density, ThemePref } from "../ui";

export interface Prefs {
  theme: ThemePref;
  /** 强调色。 */
  accent: Accent;
  density: Density;
  /** 对话最大宽度（px）。 */
  chatWidth: number;
  /** Enter 发送还是 Ctrl/⌘+Enter 发送。 */
  sendKey: "enter" | "mod-enter";
  /** 推理默认展开。 */
  thinkingOpen: boolean;
  /** 工具组结束后自动收起。 */
  toolsAutoCollapse: boolean;
  /** 显示上下文注入条目。 */
  showContext: boolean;
  /** 回合结束后给建议追问（后端每回合多一次模型调用）。 */
  followups: boolean;
  /** 回合完成 / 需要确认时提醒（窗口在后台时）。 */
  notify: "never" | "background" | "always";
  /** 快捷键改绑：命令 id → 快捷键（"" = 解绑）。 */
  keybinds: Record<string, string>;
  /** 关掉的内置贡献包。 */
  disabledPacks: string[];
  /** 左栏折叠。 */
  sidebarCollapsed: boolean;
  /** 左栏宽度。 */
  sidebarWidth: number;
  /** 语言。 */
  locale: "zh" | "en";
  /** 布局预设（LayoutPreset id；"" = 第一个）。 */
  preset: string;
  /** 命名布局：名称 → 布局快照（命令面板里一键切换）。 */
  layouts: Record<string, unknown>;
}

export const DEFAULT_PREFS: Prefs = {
  theme: "system",
  accent: "indigo",
  density: "comfortable",
  chatWidth: 720,
  sendKey: "enter",
  thinkingOpen: false,
  toolsAutoCollapse: true,
  showContext: false,
  followups: true,
  notify: "background",
  keybinds: {},
  disabledPacks: [],
  sidebarCollapsed: false,
  sidebarWidth: 280,
  locale: "zh",
  preset: "",
  layouts: {},
};

export interface PrefsStore extends ReadStore<Prefs> {
  set<K extends keyof Prefs>(k: K, v: Prefs[K]): void;
  reset(): void;
}

export function createPrefs(ns = "tenon", initial: Partial<Prefs> = {}, storage: Storage | null = safeStorage()): PrefsStore {
  const key = `${ns}.prefs.v1`;
  const base = { ...DEFAULT_PREFS, ...initial };
  let loaded: Partial<Prefs> = {};
  try {
    const raw = storage?.getItem(key);
    if (raw) loaded = JSON.parse(raw) as Partial<Prefs>;
  } catch {
    loaded = {};
  }
  const store = createStore<Prefs>(sanitize({ ...base, ...loaded }, base));
  const save = (p: Prefs) => {
    try {
      storage?.setItem(key, JSON.stringify(p));
    } catch {
      /* 存储满或被禁用：只在内存里生效 */
    }
  };
  return {
    get: store.get,
    subscribe: store.subscribe,
    set(k, v) {
      const next = { ...store.get(), [k]: v };
      store.set(next);
      save(next);
    },
    reset() {
      store.set(base);
      save(base);
    },
  };
}

/** 类型不对的字段换回缺省值（旧版本存的值、手改坏的值）。 */
function sanitize(p: Prefs, base: Prefs): Prefs {
  const out = { ...p };
  for (const k of Object.keys(base) as (keyof Prefs)[]) {
    if (typeof out[k] !== typeof base[k] || (Array.isArray(base[k]) && !Array.isArray(out[k]))) {
      (out as Record<string, unknown>)[k] = base[k];
    }
  }
  return out;
}

function safeStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}
