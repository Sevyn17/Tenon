// 会话列表（protocol 2，docs/01 - 设计文档/06 - 应用协议契约.md 10.4 sessions.list、10.5 sessions.changed）。
// sessions.list 是分页查询；置顶的会话另查一次（sessions.list {pinned}，不分页），排在最前。
// 连接级的 sessions.changed（易失）刷新列表里的一行（含删除）；重连后重新查第一页。
//
// Mortise 客户端改进 M7（spike-protocol 第 3、4 节，协议T11、T14）：
// - 服务端报 sessions.changed.full 时每条 sessions.changed 都带全套字段（preview、last_end、parent、origin、
//   workspace、archived），列表里没有的会话按规则插入（mergeChanged）；旧服务端退回一次防抖 refresh()。
// - 已归档的会话不在缺省列表里，左栏「已归档」打开后单独查（sessions.list {archived:"only"}）。
// - 搜索、子会话走服务端（能力 sessions.query）；没有能力时由调用方本地过滤。
// 新字段在旧服务端上没有，读的时候一律经类型守卫（lastEndOf、archivedOf、workspaceOf），不做断言。

import type { Connection } from "./transport";
import { LOCAL_RECONNECTED } from "./commands";
import { createStore, type ReadStore } from "./store";
import type { Envelope, HelloResult, ListArgs, SessionInfo, SessionsChanged, TurnEnded, UpdateArgs } from "./wire.gen";

/** 能力：每条 sessions.changed 都带全套字段（M7）。 */
export const CAP_CHANGED_FULL = "sessions.changed.full";
/** 能力：sessions.list 支持 query / parent / archived，session.update 支持 archived（M7）。 */
export const CAP_QUERY = "sessions.query";

/** M7 加在会话行与 sessions.changed 上的字段（都可缺：旧服务端没有）。 */
export interface SessionExtra {
  preview?: string;
  last_end?: TurnEnded;
  parent?: string;
  origin?: string;
  workspace?: string;
  archived?: boolean;
}

/** 列表里的一行：协议的 SessionInfo 加上 M7 的字段。 */
export type SessionRow = SessionInfo & SessionExtra;

function isTurnEnded(v: unknown): v is TurnEnded {
  return typeof v === "object" && v !== null && "kind" in v && typeof v.kind === "string";
}

/** 会话行（或 sessions.changed）上记的最近一次回合结束；没有记录或旧服务端为 undefined。 */
export function lastEndOf(x: SessionInfo | SessionsChanged): TurnEnded | undefined {
  const v: unknown = x.last_end;
  return isTurnEnded(v) ? v : undefined;
}

/** 是否已归档。 */
export function archivedOf(x: SessionInfo | SessionsChanged): boolean {
  return x.archived === true;
}

/**
 * 会话行是否算子会话（列表里折叠进「含子会话」、缩进显示）：带 parent 且不是用户主动分叉的。
 * origin=fork 的分叉会话虽然记了来源 parent，但在列表里算顶层（与服务端 store.TopLevel 一致）。
 */
export function isChildRow(x: { parent?: string; origin?: string }): boolean {
  return !!x.parent && x.origin !== "fork";
}

/** 线上来的字符串字段：不是字符串就当没有。 */
function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

/** 握手里当前工作区的键（与 SessionInfo.workspace 同值）；空串或 undefined = 宿主不按工作区区分。 */
export function workspaceOf(h: HelloResult | null | undefined): string | undefined {
  return h ? str(h.workspace) : undefined;
}

/** 已归档的会话（左栏「已归档」打开后才查；undefined = 没打开）。 */
export interface ArchivedView {
  items: SessionRow[];
  next: number;
  loading: boolean;
  error?: string;
}

export interface SessionsView {
  /** 按更新时间分页的会话（不含置顶的）。 */
  items: SessionRow[];
  /** 置顶的会话（按更新时间）。 */
  pinned: SessionRow[];
  next: number; // 0 = 没有下一页
  loading: boolean;
  error?: string;
  all: boolean; // 含子会话
  /** 已归档的会话（M7）；不在 listed() 里。 */
  archived?: ArchivedView;
}

export interface SessionsStoreOptions {
  /** sessions.changed 是否带全套字段（client.can(CAP_CHANGED_FULL)）。 */
  full?: () => boolean;
  /** 当前工作区的键（hello.workspace）；空串或 undefined = 不按工作区区分。 */
  workspace?: () => string | undefined;
  /** 是否支持服务端查询（client.can(CAP_QUERY)）。 */
  query?: () => boolean;
  /** 旧服务端收到未知会话后补一次 refresh() 的防抖（毫秒，缺省 1000）。 */
  refreshDelay?: number;
}

export interface SessionsStore extends ReadStore<SessionsView> {
  refresh(): Promise<void>;
  more(): Promise<void>;
  setAll(all: boolean): Promise<void>;
  /** 本地插入（新建、分叉后立即出现在列表里，不等下一次查询）。 */
  upsert(info: SessionRow): void;
  /** 本地移除（删除成功后立即消失，不等 sessions.changed）。 */
  remove(id: string): void;
  /** 一行的信息（置顶、分页、已归档任一处）。 */
  find(id: string): SessionRow | undefined;
  /** 打开「已归档」：查第一页（已打开时重查）。失败记在 archived.error，不抛。 */
  openArchived(): Promise<void>;
  /** 已归档的下一页。 */
  moreArchived(): Promise<void>;
  /** 收起「已归档」（丢掉已查的行）。 */
  closeArchived(): void;
  /** 归档 / 恢复（session.update {archived}）；失败抛错（调用方提示）。归档顺带取消置顶。 */
  setArchived(id: string, archived: boolean): Promise<void>;
  /** 服务端搜索标题与用户消息（需要 sessions.query；没有能力时调用方本地过滤）。 */
  search(query: string, limit?: number): Promise<SessionRow[]>;
  /** 某会话的直接子会话：有 sessions.query 时按 parent 查，否则取一批再本地过滤。失败抛错。 */
  children(parent: string): Promise<SessionRow[]>;
  close(): void;
}

export function createSessionsStore(conn: Connection, limit = 50, opts: SessionsStoreOptions = {}): SessionsStore {
  const store = createStore<SessionsView>({ items: [], pinned: [], next: 0, loading: false, all: false });
  let gen = 0;
  let archGen = 0; // 已归档查询的代次

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
      store.update((v) => ({ ...v, loading: false, error: errText(e) }));
    }
  };

  // 已归档：cursor = 0 查第一页（替换），否则追加。
  const archPage = async (cursor: number) => {
    const g = ++archGen;
    store.update((v) => ({ ...v, archived: { items: v.archived?.items ?? [], next: v.archived?.next ?? 0, loading: true } }));
    try {
      const args: ListArgs = { archived: "only", cursor, limit, all: store.get().all };
      const r = await conn.call("sessions.list", args);
      if (g !== archGen) return;
      const got: SessionRow[] = (r.items ?? []).map((x) => ({ ...x, archived: true }));
      store.update((v) => {
        if (!v.archived) return v; // 查询途中收起了
        const items = cursor === 0 ? got : dedupe([...v.archived.items, ...got]);
        return { ...v, archived: { items, next: r.next, loading: false } };
      });
    } catch (e) {
      if (g !== archGen) return;
      store.update((v) => (v.archived ? { ...v, archived: { ...v.archived, loading: false, error: errText(e) } } : v));
    }
  };

  // 旧服务端（没有 sessions.changed.full）收到列表里没有的会话：防抖后重查第一页。
  // 定时器只在没有挂起的重查时启动，持续的事件流也会在 refreshDelay 内触发一次。
  let timer: ReturnType<typeof setTimeout> | undefined;
  const later = () => {
    if (timer !== undefined) return;
    timer = setTimeout(() => {
      timer = undefined;
      void page(0); // page 自己记错误（error 字段），不会抛
    }, opts.refreshDelay ?? 1000);
  };

  const off = conn.onEvent((ev: Envelope) => {
    if (ev.kind === LOCAL_RECONNECTED) {
      void page(0);
      if (store.get().archived) void archPage(0);
      return;
    }
    if (ev.kind !== "sessions.changed") return;
    const ctx: ChangedContext = { full: !!opts.full?.(), workspace: opts.workspace?.() };
    let refresh = false;
    store.update((v) => {
      const r = mergeChanged(v, ev.data as SessionsChanged, ctx);
      refresh = r.refresh;
      return r.view;
    });
    if (refresh) later();
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
      store.update((v) => {
        // 已归档的行留在原处；其余照旧进置顶区或按时间的列表。
        if (v.archived?.items.some((x) => x.id === info.id)) return { ...v, archived: { ...v.archived, items: replaceIn(v.archived.items, info) } };
        return place({ ...v, items: v.items.filter((x) => x.id !== info.id), pinned: v.pinned.filter((x) => x.id !== info.id) }, info);
      });
    },
    remove(id) {
      store.update((v) => dropTree(v, id));
    },
    find(id) {
      return findIn(store.get(), id);
    },
    openArchived: () => archPage(0),
    async moreArchived() {
      const a = store.get().archived;
      if (a && a.next > 0 && !a.loading) await archPage(a.next);
    },
    closeArchived() {
      archGen++;
      store.update((v) => ({ ...v, archived: undefined }));
    },
    async setArchived(id, archived) {
      const args: UpdateArgs = { session: id, archived };
      const snap = await conn.call("session.update", args);
      const known = findIn(store.get(), id);
      store.update((v) => {
        const rest = without(v, id);
        // 恢复：从已归档里拿掉，回到哪一组（工作区 / 置顶）由下面的重查决定。
        if (!archived) return rest;
        const row: SessionRow = {
          ...(known ?? { id, preview: "", origin: snap.origin, run_state: snap.run_state, updated_at: 0 }),
          title: snap.title,
          pinned: undefined,
          archived: true,
        };
        return rest.archived && row.updated_at > 0 ? { ...rest, archived: { ...rest.archived, items: sortByUpdated([row, ...rest.archived.items]) } } : rest;
      });
      if (!archived) await page(0);
    },
    async search(query, n = 50) {
      const args: ListArgs = { query: query.trim(), limit: n, all: store.get().all };
      const r = await conn.call("sessions.list", args);
      return r.items ?? [];
    },
    async children(parent) {
      if (opts.query?.()) {
        const args: ListArgs = { parent, all: true, limit: 200 };
        const r = await conn.call("sessions.list", args);
        return r.items ?? [];
      }
      // 旧服务端不认 parent：取一批再过滤（只看得到最近 200 条里的）。
      const r = await conn.call("sessions.list", { all: true, limit: 200 });
      return (r.items ?? []).filter((x) => x.parent === parent || x.id.startsWith(parent + "/"));
    },
    close() {
      off();
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
    },
  };
}

/** mergeChanged 需要的连接信息。 */
export interface ChangedContext {
  /** 服务端报了 sessions.changed.full：每条事件都带全套字段。 */
  full: boolean;
  /** 当前工作区的键（hello.workspace）；空串或 undefined = 不按工作区区分（任何 workspace 都算当前）。 */
  workspace?: string;
}

/**
 * sessions.changed 合进列表（纯函数，便于测试）。refresh = 事件不够判断这一行放哪（旧服务端的未知会话、
 * 没有更新时间的新行），调用方应防抖重查第一页。
 *
 * 列表里已有的行：原地更新（全套字段时连 preview、last_end、archived 一起）；归档了就挪进「已归档」（打开时）。
 * 列表里没有的行（只在全套字段时插入）：不是子会话（或开了「含子会话」）、属于当前工作区、没归档、更新时间不早于已加载的最后一行（或已到末页）。置顶的不看更新时间。
 */
export function mergeChanged(v: SessionsView, d: SessionsChanged, ctx: ChangedContext = { full: false }): { view: SessionsView; refresh: boolean } {
  const keep = (view: SessionsView) => ({ view, refresh: false });
  if (d.deleted) return keep(dropTree(v, d.id));
  const known = findIn(v, d.id);
  const archived = ctx.full && archivedOf(d);

  if (known) {
    const row = patch(known, d, ctx.full);
    // 已归档：从缺省列表挪进「已归档」。
    if (archived) {
      const rest = without(v, d.id);
      return keep(rest.archived ? { ...rest, archived: { ...rest.archived, items: sortByUpdated([row, ...rest.archived.items]) } } : rest);
    }
    // 在「已归档」里且恢复了：拿掉，放回缺省列表（按插入规则）。
    if (ctx.full && v.archived?.items.some((x) => x.id === d.id)) {
      const rest = without(v, d.id);
      return insertNew(rest, row, ctx);
    }
    if (v.archived?.items.some((x) => x.id === d.id)) return keep({ ...v, archived: { ...v.archived, items: replaceIn(v.archived.items, row) } });
    return keep(place({ ...v, items: v.items.filter((x) => x.id !== d.id), pinned: v.pinned.filter((x) => x.id !== d.id) }, row));
  }

  if (!ctx.full) {
    // 旧服务端：只知道标题、状态与置顶。置顶的补进置顶区（置顶区不分页）；其余重查第一页。
    // 子会话（id 带 "/"）在缺省列表里本来就不显示，不为它重查。
    if (d.pinned) return keep(place(v, newRow(d, false)));
    return { view: v, refresh: v.all || !d.id.includes("/") };
  }

  const row = newRow(d, true);
  if (archived) {
    if (!v.archived || !sameWorkspace(row, ctx)) return keep(v);
    return keep(fits(v.archived.items, v.archived.next, row) ? { ...v, archived: { ...v.archived, items: sortByUpdated([row, ...v.archived.items]) } } : v);
  }
  return insertNew(v, row, ctx);
}

/** sessions.changed 合进列表（只要结果视图；需要重查的情况由 store 处理）。 */
export function applyChanged(v: SessionsView, d: SessionsChanged, ctx?: ChangedContext): SessionsView {
  return mergeChanged(v, d, ctx).view;
}

/** 按插入规则放一行列表里原来没有的会话。 */
function insertNew(v: SessionsView, row: SessionRow, ctx: ChangedContext): { view: SessionsView; refresh: boolean } {
  if (isChildRow(row) && !v.all) return { view: v, refresh: false };
  const ws = row.workspace ?? "";
  if (ctx.workspace && ws !== ctx.workspace) return { view: v, refresh: false }; // 别的工作区的不显示
  if (row.pinned) return { view: place(v, row), refresh: false };
  // 改名、置顶这类事件 updated_at = 0：不知道排在哪，重查。
  if (!row.updated_at) return { view: v, refresh: true };
  // 比已加载的最后一行还旧：等翻页时自然出现，现在插进来会排错位置。
  if (!fits(v.items, v.next, row)) return { view: v, refresh: false };
  return { view: place(v, row), refresh: false };
}

/** 行属于当前工作区（ctx 没有工作区时都算）。 */
function sameWorkspace(row: SessionRow, ctx: ChangedContext): boolean {
  return !ctx.workspace || (row.workspace ?? "") === ctx.workspace;
}

/** 新行能否插进已加载的列表：已到末页，或不早于已加载的最后一行。 */
function fits(xs: SessionRow[], next: number, row: SessionRow): boolean {
  if (next <= 0 || xs.length === 0) return true;
  return row.updated_at >= xs[xs.length - 1]!.updated_at;
}

/** 从一条 sessions.changed 造一行（列表里原来没有）。 */
function newRow(d: SessionsChanged, full: boolean): SessionRow {
  const row: SessionRow = {
    id: d.id,
    title: d.title,
    preview: (full && str(d.preview)) || "",
    run_state: d.run_state,
    origin: (full && str(d.origin)) || "new",
    updated_at: d.updated_at || 0,
    pinned: d.pinned || undefined,
  };
  if (!full) return row;
  // 全套字段：没出现 = 空串 / false / 没有（m7-framework 约定）。
  const parent = str(d.parent);
  const end = lastEndOf(d);
  if (parent) row.parent = parent;
  row.workspace = str(d.workspace) ?? "";
  if (end) row.last_end = end;
  if (archivedOf(d)) row.archived = true;
  return row;
}

/** 已有的一行按 sessions.changed 更新。全套字段时 preview、last_end、archived 以事件为准（缺 = 清掉）。 */
function patch(known: SessionRow, d: SessionsChanged, full: boolean): SessionRow {
  const row: SessionRow = {
    ...known,
    title: d.title,
    run_state: d.run_state,
    pinned: d.pinned || undefined,
    updated_at: d.updated_at || known.updated_at, // 改标题、置顶、归档时 updated_at = 0：不动原值
  };
  if (!full) return row;
  // 全套字段：以事件为准，没出现 = 空串 / false / 没有。origin 对新服务端恒非空，缺了保留原值。
  row.preview = str(d.preview) ?? "";
  const end = lastEndOf(d);
  if (end) row.last_end = end;
  else delete row.last_end;
  if (archivedOf(d)) row.archived = true;
  else delete row.archived;
  const parent = str(d.parent);
  if (parent) row.parent = parent;
  else delete row.parent;
  row.origin = str(d.origin) || known.origin;
  row.workspace = str(d.workspace) ?? "";
  return row;
}

function place(v: SessionsView, row: SessionRow): SessionsView {
  return row.pinned ? { ...v, pinned: sortByUpdated([row, ...v.pinned]) } : { ...v, items: sortByUpdated([row, ...v.items]) };
}

function findIn(v: SessionsView, id: string): SessionRow | undefined {
  return (
    v.pinned.find((x) => x.id === id) ??
    v.items.find((x) => x.id === id) ??
    v.archived?.items.find((x) => x.id === id)
  );
}

/** 从各组里拿掉一行（不连带子会话）。 */
function without(v: SessionsView, id: string): SessionsView {
  const keep = (x: SessionRow) => x.id !== id;
  const out: SessionsView = { ...v, items: v.items.filter(keep), pinned: v.pinned.filter(keep) };
  if (v.archived) out.archived = { ...v.archived, items: v.archived.items.filter(keep) };
  return out;
}

/** 删掉一行及它的子会话（id 以 "<id>/" 开头）。 */
function dropTree(v: SessionsView, id: string): SessionsView {
  const keep = (x: SessionRow) => x.id !== id && !x.id.startsWith(id + "/");
  const out: SessionsView = { ...v, items: v.items.filter(keep), pinned: v.pinned.filter(keep) };
  if (v.archived) out.archived = { ...v.archived, items: v.archived.items.filter(keep) };
  return out;
}

/** 换掉列表里同 id 的一行并按更新时间重排。 */
function replaceIn(xs: SessionRow[], row: SessionRow): SessionRow[] {
  return sortByUpdated(xs.map((x) => (x.id === row.id ? row : x)));
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** 当前工作区的左栏顺序：置顶在前，其余按更新时间。 */
export function listed(v: SessionsView): SessionRow[] {
  return [...v.pinned, ...v.items];
}

function dedupe<T extends { id: string }>(xs: T[]): T[] {
  const seen = new Set<string>();
  return xs.filter((x) => (seen.has(x.id) ? false : (seen.add(x.id), true)));
}

function sortByUpdated<T extends { updated_at: number }>(xs: T[]): T[] {
  return [...xs].sort((a, b) => b.updated_at - a.updated_at);
}
