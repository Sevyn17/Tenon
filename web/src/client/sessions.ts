// 会话列表（protocol 2，docs/06 - 应用协议契约.md 10.4 sessions.list、10.5 sessions.changed）。
// sessions.list 是分页查询；连接级的 sessions.changed（易失）刷新列表里的一行；重连后重新查第一页。

import type { Connection } from "./transport";
import { LOCAL_RECONNECTED } from "./commands";
import { createStore, type ReadStore } from "./store";
import type { Envelope, SessionInfo, SessionsChanged } from "./wire.gen";

export interface SessionsView {
  items: SessionInfo[];
  next: number; // 0 = 没有下一页
  loading: boolean;
  error?: string;
  all: boolean; // 含子会话
}

export interface SessionsStore extends ReadStore<SessionsView> {
  refresh(): Promise<void>;
  more(): Promise<void>;
  setAll(all: boolean): Promise<void>;
  /** 本地插入（新建、分叉后立即出现在列表里，不等下一次查询）。 */
  upsert(info: SessionInfo): void;
  close(): void;
}

export function createSessionsStore(conn: Connection, limit = 50): SessionsStore {
  const store = createStore<SessionsView>({ items: [], next: 0, loading: false, all: false });
  let gen = 0;

  const page = async (cursor: number) => {
    const g = ++gen;
    store.update((v) => ({ ...v, loading: true, error: undefined }));
    try {
      const r = await conn.call("sessions.list", { cursor, limit, all: store.get().all });
      if (g !== gen) return;
      store.update((v) => ({
        ...v,
        loading: false,
        next: r.next,
        items: cursor === 0 ? (r.items ?? []) : dedupe([...v.items, ...(r.items ?? [])]),
      }));
    } catch (e) {
      if (g !== gen) return;
      store.update((v) => ({ ...v, loading: false, error: e instanceof Error ? e.message : String(e) }));
    }
  };

  const off = conn.onEvent((ev: Envelope) => {
    if (ev.kind === LOCAL_RECONNECTED) {
      void page(0);
      return;
    }
    if (ev.kind !== "sessions.changed") return;
    const d = ev.data as SessionsChanged;
    store.update((v) => {
      const i = v.items.findIndex((x) => x.id === d.id);
      if (i < 0) return v; // 不在已加载的页里：等下次查询
      const row = { ...v.items[i], run_state: d.run_state, title: d.title || v.items[i].title, updated_at: d.updated_at };
      const rest = v.items.filter((_, j) => j !== i);
      return { ...v, items: sortByUpdated([row, ...rest]) };
    });
  });

  return {
    get: store.get,
    subscribe: store.subscribe,
    refresh: () => page(0),
    async more() {
      const n = store.get().next;
      if (n > 0 && !store.get().loading) await page(n);
    },
    async setAll(all) {
      store.update((v) => ({ ...v, all }));
      await page(0);
    },
    upsert(info) {
      store.update((v) => ({ ...v, items: sortByUpdated(dedupe([info, ...v.items])) }));
    },
    close: off,
  };
}

function dedupe(xs: SessionInfo[]): SessionInfo[] {
  const seen = new Set<string>();
  return xs.filter((x) => (seen.has(x.id) ? false : (seen.add(x.id), true)));
}

function sortByUpdated(xs: SessionInfo[]): SessionInfo[] {
  return [...xs].sort((a, b) => b.updated_at - a.updated_at);
}

/** 按更新时间分组（今天 / 昨天 / 近 7 天 / 更早）。now 可注入，便于测试。 */
export type DateBucket = "today" | "yesterday" | "week" | "older";

export function groupByDate(items: SessionInfo[], now = Date.now()): { bucket: DateBucket; items: SessionInfo[] }[] {
  const d = new Date(now);
  const startToday = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const day = 86_400_000;
  const of = (t: number): DateBucket =>
    t >= startToday ? "today" : t >= startToday - day ? "yesterday" : t >= startToday - 6 * day ? "week" : "older";
  const order: DateBucket[] = ["today", "yesterday", "week", "older"];
  const m = new Map<DateBucket, SessionInfo[]>();
  for (const it of items) {
    const b = of(it.updated_at);
    m.set(b, [...(m.get(b) ?? []), it]);
  }
  return order.filter((b) => m.has(b)).map((b) => ({ bucket: b, items: m.get(b)! }));
}
