// 一个会话的客户端状态（protocol 2，docs/06 - 应用协议契约.md 10.2）。负责 I/O，状态合成交给纯 reducer（session-reducer.ts）。
//
// 一致性：先 session.get 取快照，再取日志尾部，再从快照的 cursor 往后 session.subscribe——快照之后的变化一定在 cursor 之后，
// 订阅回放不漏不重。session.lagged（背压断开）与 local.reconnected（断线重连）时从最后收到的持久游标重新订阅；
// 游标过期（cursor_expired）时整个重新加载。
//
// 发送：每条输入带 client_id（幂等键）；失败的留在 outbox 里可重发，成功后等 turn.started.inputs / 条目的 client_ids 收掉。

import type { Connection } from "./transport";
import { clientID } from "./transport";
import { ApiError, LOCAL_RECONNECTED } from "./commands";
import { createStore, type ReadStore } from "./store";
import { applySnapshot, initialState, mergeEntries, reduce, type SessionState } from "./session-reducer";
import type { CursorRef, Envelope, Part, SessionSnapshot } from "./wire.gen";

/** 首屏与每次向上翻页取多少条。 */
export const PAGE = 50;

/** 一条待发送 / 发送失败的输入。 */
export interface Outgoing {
  client_id: string;
  text: string;
  content?: Part[];
  state: "sending" | "sent" | "failed";
  steer: boolean;
  error?: string;
}

export interface SessionView extends SessionState {
  outbox: Outgoing[];
  loadingOlder: boolean;
}

export interface SessionStore extends ReadStore<SessionView> {
  readonly id: string;
  /** 取快照、日志尾部并订阅。create 为真时会话不存在就新建（显式新建，服务端不隐式建）。 */
  open(opts?: { create?: boolean }): Promise<void>;
  /** 取消订阅；之后的事件不再合成。 */
  close(): void;
  /** 向上翻一页。 */
  loadOlder(): Promise<void>;
  /** 发送：空闲时开回合；回合中按插话（steer）。 */
  send(text: string, content?: Part[]): Promise<void>;
  /** 重发一条失败的输入。 */
  retry(clientID: string): Promise<void>;
  /** 丢掉一条失败的输入。 */
  discard(clientID: string): void;
  cancel(): Promise<void>;
}

export function createSessionStore(conn: Connection, id: string): SessionStore {
  const store = createStore<SessionView>({ ...initialState(id), outbox: [], loadingOlder: false });
  let gen = 0; // 加载代次：重新加载或关闭后，旧请求的结果丢弃
  let off: (() => void) | null = null;

  const patch = (f: (v: SessionView) => SessionView) => store.update(f);
  const setState = (f: (s: SessionState) => SessionState) =>
    patch((v) => ({ ...v, ...f(v) }));

  const subscribe = async (after: number, g: number) => {
    try {
      await conn.call("session.subscribe", { session: id, after });
    } catch (e) {
      if (g !== gen) return;
      if (e instanceof ApiError && e.code === "cursor_expired") {
        void load(false);
        return;
      }
      setState((s) => ({ ...s, status: "error", error: e instanceof Error ? e.message : String(e) }));
    }
  };

  const onEvent = (ev: Envelope) => {
    const g = gen;
    if (ev.kind === LOCAL_RECONNECTED) {
      void subscribe(store.get().cursor, g);
      return;
    }
    if (ev.session !== id) return;
    if (ev.kind === "session.lagged") {
      // 背压断开：只丢了易失事件，从最后送达的持久游标重新订阅
      setState((s) => ({ ...s, streams: [] }));
      void subscribe((ev.data as CursorRef).cursor, g);
      return;
    }
    patch((v) => {
      const next = reduce(v, ev);
      let outbox = v.outbox;
      if (ev.kind === "turn.started") {
        const ins = new Set((ev.data as { inputs?: string[] }).inputs ?? []);
        outbox = outbox.filter((o) => !ins.has(o.client_id));
      } else if (ev.kind === "entry.appended") {
        const ids = new Set(next.entries[next.entries.length - 1]?.client_ids ?? []);
        if (ids.size) outbox = outbox.filter((o) => !ids.has(o.client_id));
      }
      return { ...v, ...next, outbox };
    });
  };

  const load = async (create: boolean) => {
    const g = ++gen;
    setState((s) => ({ ...s, status: "loading", error: undefined }));
    try {
      let snap: SessionSnapshot;
      try {
        snap = await conn.call("session.get", { session: id });
      } catch (e) {
        if (!(create && e instanceof ApiError && e.code === "not_found")) throw e;
        snap = await conn.call("session.create", { session: id });
      }
      if (g !== gen) return;
      const page = await conn.call("session.entries", { session: id, tail: PAGE });
      if (g !== gen) return;
      setState((s) => ({ ...mergeEntries(applySnapshot({ ...s, entries: [] }, snap), page.entries ?? [], page.base, page.len), status: "ready" }));
      await subscribe(snap.cursor, g);
    } catch (e) {
      if (g !== gen) return;
      setState((s) => ({ ...s, status: "error", error: e instanceof Error ? e.message : String(e) }));
    }
  };

  const deliver = async (o: Outgoing) => {
    try {
      if (o.steer) {
        try {
          await conn.call("session.steer", { session: id, text: o.text, client_id: o.client_id });
        } catch (e) {
          // 回合恰好结束：改成普通发送
          if (!(e instanceof ApiError && (e.code === "invalid" || e.code === "not_idle"))) throw e;
          await conn.call("session.send", { session: id, text: o.text, client_id: o.client_id });
        }
      } else {
        await conn.call("session.send", {
          session: id,
          client_id: o.client_id,
          ...(o.content?.length ? { content: o.content } : { text: o.text }),
        });
      }
      patch((v) => ({ ...v, outbox: v.outbox.map((x) => (x.client_id === o.client_id ? { ...x, state: "sent" } : x)) }));
    } catch (e) {
      patch((v) => ({
        ...v,
        outbox: v.outbox.map((x) =>
          x.client_id === o.client_id ? { ...x, state: "failed", error: e instanceof Error ? e.message : String(e) } : x,
        ),
      }));
      throw e;
    }
  };

  return {
    id,
    get: store.get,
    subscribe: store.subscribe,
    async open(opts) {
      if (!off) off = conn.onEvent(onEvent);
      await load(!!opts?.create);
    },
    close() {
      gen++;
      off?.();
      off = null;
      void conn.call("session.unsubscribe", { session: id }).catch(() => {});
    },
    async loadOlder() {
      const v = store.get();
      const first = v.entries[0]?.idx ?? 0;
      if (v.loadingOlder || first <= 0) return;
      const g = gen;
      patch((x) => ({ ...x, loadingOlder: true }));
      try {
        const r = await conn.call("session.entries", { session: id, from: Math.max(first - PAGE, 0), to: first });
        if (g !== gen) return;
        setState((s) => mergeEntries(s, r.entries ?? []));
      } finally {
        patch((x) => ({ ...x, loadingOlder: false }));
      }
    },
    async send(text, content) {
      const t = text.trim();
      if (!t && !content?.length) return;
      const o: Outgoing = { client_id: clientID(), text: t, content, state: "sending", steer: store.get().runState === "running" && !content?.length };
      patch((v) => ({ ...v, outbox: [...v.outbox, o] }));
      await deliver(o);
    },
    async retry(cid) {
      const o = store.get().outbox.find((x) => x.client_id === cid);
      if (!o) return;
      const again = { ...o, state: "sending" as const, error: undefined };
      patch((v) => ({ ...v, outbox: v.outbox.map((x) => (x.client_id === cid ? again : x)) }));
      await deliver(again);
    },
    discard(cid) {
      patch((v) => ({ ...v, outbox: v.outbox.filter((x) => x.client_id !== cid) }));
    },
    async cancel() {
      await conn.call("session.cancel", { session: id });
    },
  };
}
