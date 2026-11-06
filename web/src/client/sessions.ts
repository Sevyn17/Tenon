// 会话列表（protocol 2，docs/06 - 应用协议契约.md 10.4 sessions.list、10.5 sessions.changed）。
// sessions.list 是分页查询；置顶的会话另查一次（sessions.list {pinned}，不分页），排在最前。
// 连接级的 sessions.changed（易失）刷新列表里的一行（含删除）；重连后重新查第一页。

import type { Connection } from "./transport";
import { LOCAL_RECONNECTED } from "./commands";
import { createStore, type ReadStore } from "./store";
import type { Envelope, SessionInfo, SessionsChanged } from "./wire.gen";

export interface SessionsView {
  /** 按更新时间分页的会话（不含置顶的）。 */
  items: SessionInfo[];
  /** 置顶的会话（按更新时间）。 */
  pinned: SessionInfo[];
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
  /** 本地移除（删除成功后立即消失，不等 sessions.changed）。 */
  remove(id: string): void;
  /** 一行的信息（置顶或分页里任一处）。 */
  find(id: string): SessionInfo | undefined;
  close(): void;
}

export function createSessionsStore(conn: Connection, limit = 50): SessionsStore {
  const store = createStore<SessionsView>({ items: [], pinned: [], next: 0, loading: false, all: false });
  let gen = 0;

  const page = async (cursor: number) => {
    const g = ++gen;
    store.update((v) => ({ ...v, loading: true, error: undefined }));
    try {
      const all = store.get().all;
      const [r, p] = await Promise.all([
        conn.call("sessions.list", { cursor, limit, all }),
        cursor === 0 ? conn.call("sessions.list", { pinned: true, limit: 200, all }) : Promise.resolve(null),
      ]);
      if (g !== gen) return;
      store.update((v) => {
        const pinned = p ? (p.items ?? []) : v.pinned;
        const items = cursor === 0 ? (r.items ?? []) : dedupe([...v.items, ...(r.items ?? [])]);
        return { ...v, loading: false, next: r.next, pinned, items: items.filter((x) => !x.pinned) };
      });
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
    store.update((v) => applyChanged(v, ev.data as SessionsChanged));
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
      store.update((v) => place({ ...v, items: v.items.filter((x) => x.id !== info.id), pinned: v.pinned.filter((x) => x.id !== info.id) }, info));
    },
    remove(id) {
      store.update((v) => dropTree(v, id));
    },
    find(id) {
      const v = store.get();
      return v.pinned.find((x) => x.id === id) ?? v.items.find((x) => x.id === id);
    },
    close: off,
  };
}

/** sessions.changed 合进列表（纯函数，便于测试）。不在已加载页里的行：置顶了就补进置顶区，否则等下次查询。 */
export function applyChanged(v: SessionsView, d: SessionsChanged): SessionsView {
  if (d.deleted) return dropTree(v, d.id);
  const old = v.pinned.find((x) => x.id === d.id) ?? v.items.find((x) => x.id === d.id);
  if (!old && !d.pinned) return v;
  const row: SessionInfo = {
    ...(old ?? { id: d.id, preview: "", origin: "new" }),
    title: d.title,
    run_state: d.run_state,
    pinned: d.pinned || undefined,
    updated_at: d.updated_at || old?.updated_at || 0, // 改标题、置顶时 updated_at = 0：不动原值
  };
  const rest = { ...v, items: v.items.filter((x) => x.id !== d.id), pinned: v.pinned.filter((x) => x.id !== d.id) };
  return place(rest, row);
}

function place(v: SessionsView, row: SessionInfo): SessionsView {
  return row.pinned ? { ...v, pinned: sortByUpdated([row, ...v.pinned]) } : { ...v, items: sortByUpdated([row, ...v.items]) };
}

/** 删掉一行及它的子会话（id 以 "<id>/" 开头）。 */
function dropTree(v: SessionsView, id: string): SessionsView {
  const keep = (x: SessionInfo) => x.id !== id && !x.id.startsWith(id + "/");
  return { ...v, items: v.items.filter(keep), pinned: v.pinned.filter(keep) };
}

/** 左栏顺序：置顶在前，其余按更新时间。 */
export function listed(v: SessionsView): SessionInfo[] {
  return [...v.pinned, ...v.items];
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
