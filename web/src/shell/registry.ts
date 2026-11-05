// 贡献模型（前端方案第十三节）：一切可插入的东西都经注册表贡献，内置贡献也走同一条路。
//
// 规则：
//   - 每份贡献属于一个贡献包（ContributionPack）；包可声明 requires（后端插件名 / 能力），不满足时整包不激活；
//   - 同一注册表里 id 相同的贡献，priority 小的胜（内置缺省 100），嵌入方据此替换内置贡献；
//   - 插槽三类：list（全部渲染，按 order 排）、single（priority 最小者胜）、chain（依次问，第一个非空者接管）；
//   - 包的可用性四态：pending / active / disabled（带原因）/ failed（带错误），界面按态显示，不出现点了没反应的按钮；
//   - 包的 setup 同步执行；它注册的一切随包停用一起撤销。

import { createStore, type ReadStore } from "../client";

/** 注册表项的公共字段。 */
export interface Contribution {
  id: string;
  /** 排序（小的在前），缺省 0。 */
  order?: number;
  /** 同 id 替换的优先级：小的胜，内置缺省 100。 */
  priority?: number;
  /** 所属贡献包（注册时由宿主填）。 */
  pack?: string;
}

export const BUILTIN_PRIORITY = 100;

/** 一个类型化的注册表。值不可变：增删后换新数组，配合 useSyncExternalStore。 */
export interface Registry<T extends Contribution> extends ReadStore<T[]> {
  readonly name: string;
  /** 加一项，返回撤销函数。 */
  add(item: T): () => void;
  /** 按 id 取胜出的那一项。 */
  byId(id: string): T | undefined;
}

export function defineRegistry<T extends Contribution>(name: string): Registry<T> {
  let all: T[] = [];
  const view = createStore<T[]>([]);
  const recompute = () => {
    const win = new Map<string, T>();
    for (const it of all) {
      const cur = win.get(it.id);
      if (!cur || (it.priority ?? BUILTIN_PRIORITY) < (cur.priority ?? BUILTIN_PRIORITY)) win.set(it.id, it);
    }
    view.set([...win.values()].sort((a, b) => (a.order ?? 0) - (b.order ?? 0)));
  };
  return {
    name,
    get: view.get,
    subscribe: view.subscribe,
    add(item) {
      all = [...all, item];
      recompute();
      return () => {
        all = all.filter((x) => x !== item);
        recompute();
      };
    },
    byId: (id) => view.get().find((x) => x.id === id),
  };
}

// ---- 贡献包 ----

export interface Requires {
  /** 后端插件名（plugins.list 里 on 为真才激活）。 */
  plugin?: string;
  /** hello.capabilities 里的能力。 */
  capability?: string;
}

export type PackState =
  | { state: "pending" }
  | { state: "active" }
  | { state: "disabled"; reason: string }
  | { state: "failed"; error: string };

export interface PackInfo {
  id: string;
  title: string;
  builtin: boolean;
  requires?: Requires;
  status: PackState;
  /** 本包注册了哪些东西（"注册表名:id"），开发者工具与设置 › 插件详情页用。 */
  contributes: string[];
}

export interface PackContext {
  /** 往注册表加一项（自动标记所属包，随包撤销）。 */
  add<T extends Contribution>(r: Registry<T>, item: T): void;
  /** 包停用时调用（释放定时器、订阅等）。 */
  onDispose(f: () => void): void;
  /** 包停用时中止。 */
  signal: AbortSignal;
}

export interface ContributionPack {
  id: string;
  title?: string;
  requires?: Requires;
  /** 内置包（设置里标"内置"，可被 disable 关掉）。 */
  builtin?: boolean;
  setup(ctx: PackContext): void;
}

/** 后端环境：用来判断包的 requires。 */
export interface Environment {
  ready: boolean;
  capabilities: string[];
  /** 已启用的后端插件名；null = 还不知道（plugins.list 不可用时视为"不检查插件"）。 */
  plugins: Set<string> | null;
}

export interface PackHost extends ReadStore<PackInfo[]> {
  register(p: ContributionPack): void;
  /** 后端环境变化（握手完成、插件列表到达）时重新判断各包。 */
  setEnvironment(env: Environment): void;
  /** 关掉某些包（嵌入方 disable 与设置里的开关）。 */
  setDisabled(ids: Set<string>): void;
  /** 某包运行期出错（渲染抛错）：记下，状态变成 failed。 */
  fail(id: string, error: string): void;
}

export function createPackHost(): PackHost {
  const packs: ContributionPack[] = [];
  const live = new Map<string, { dispose: () => void }>();
  const info = createStore<PackInfo[]>([]);
  const contributes = new Map<string, string[]>();
  const failed = new Map<string, string>();
  let env: Environment = { ready: false, capabilities: [], plugins: null };
  let disabled = new Set<string>();

  const statusOf = (p: ContributionPack): PackState => {
    if (failed.has(p.id)) return { state: "failed", error: failed.get(p.id)! };
    if (disabled.has(p.id)) return { state: "disabled", reason: "已在设置中关闭" };
    const r = p.requires;
    if (!r) return { state: "active" };
    if (!env.ready) return { state: "pending" };
    if (r.capability && !env.capabilities.includes(r.capability)) return { state: "disabled", reason: `后端未提供能力 ${r.capability}` };
    if (r.plugin && env.plugins && !env.plugins.has(r.plugin)) return { state: "disabled", reason: `后端插件 ${r.plugin} 未启用` };
    return { state: "active" };
  };

  const activate = (p: ContributionPack) => {
    const undo: (() => void)[] = [];
    const ac = new AbortController();
    const tags: string[] = [];
    const ctx: PackContext = {
      add(r, item) {
        undo.push(r.add({ ...item, pack: p.id }));
        tags.push(`${r.name}:${item.id}`);
      },
      onDispose: (f) => undo.push(f),
      signal: ac.signal,
    };
    try {
      p.setup(ctx);
    } catch (e) {
      for (const u of undo.reverse()) u();
      failed.set(p.id, e instanceof Error ? e.message : String(e));
      return;
    }
    contributes.set(p.id, tags);
    live.set(p.id, {
      dispose() {
        ac.abort();
        for (const u of undo.reverse()) u();
      },
    });
  };

  const sync = () => {
    for (const p of packs) {
      const st = statusOf(p);
      const on = live.has(p.id);
      if (st.state === "active" && !on) activate(p);
      else if (st.state !== "active" && on) {
        live.get(p.id)!.dispose();
        live.delete(p.id);
      }
    }
    info.set(
      packs.map((p) => ({
        id: p.id,
        title: p.title ?? p.id,
        builtin: !!p.builtin,
        requires: p.requires,
        status: statusOf(p),
        contributes: contributes.get(p.id) ?? [],
      })),
    );
  };

  return {
    get: info.get,
    subscribe: info.subscribe,
    register(p) {
      if (packs.some((x) => x.id === p.id)) throw new Error(`贡献包 ${p.id} 重复注册`);
      packs.push(p);
      sync();
    },
    setEnvironment(e) {
      env = e;
      sync();
    },
    setDisabled(ids) {
      disabled = ids;
      sync();
    },
    fail(id, error) {
      if (failed.get(id) === error) return;
      failed.set(id, error);
      sync();
    },
  };
}
