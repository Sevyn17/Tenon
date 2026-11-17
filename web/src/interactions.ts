// 未结束的交互（protocol 2，docs/01 - 设计文档/06 - 应用协议契约.md 10.6）：工具确认、模块提问等"智能体在等人回答"。
// 来源：interactions.list（跨会话，当前身份能回答的）+ 会话快照的 interactions（absorb）+ 订阅会话上的
// interaction.opened / resolved 事件。子会话的交互也写在根会话的事件序列里，所以只订阅根会话就能看到整棵树等人的地方。
//
// 为什么要读快照：订阅从快照的 cursor 往后回放，cursor 之前打开的交互只在快照里。首页第一条消息就触发审批时，
// interaction.opened 早于订阅，只靠事件会漏掉（docs/50 M2）。

import type { Connection } from "./transport";
import { clientID } from "./transport";
import { ApiError, LOCAL_RECONNECTED } from "./commands";
import { createStore, type ReadStore } from "./store";
import type { Envelope, Interaction, InteractionResolved, Response } from "./wire.gen";

/** 刚结束、本端还要短暂显示结果的交互（"已由 X 处理"、"已过期"）。 */
export interface Resolved {
  id: string;
  outcome: string;
  by?: string;
  at: number;
}

export interface InteractionsView {
  open: Interaction[]; // 按打开时间
  resolved: Resolved[]; // 最近结束的（只保留少量）
  error?: string;
}

export interface InteractionsStore extends ReadStore<InteractionsView> {
  refresh(): Promise<void>;
  /** 回答；成功返回 outcome。已被别处回答时不抛错，返回已有结果。 */
  respond(id: string, response: Response): Promise<string>;
  /** 某会话（含它的子会话）的未结束交互。 */
  forSession(session: string): Interaction[];
  /**
   * 收进某会话快照里的未结束交互（快照含以它为根的子会话）。快照是那一刻已提交的全集：
   * 本会话树里不在快照中的视为已结束，移出 open；之后的变化由订阅从快照 cursor 回放补上。
   */
  absorb(session: string, items: Interaction[]): void;
  close(): void;
}

const KEEP_RESOLVED = 20;

export function createInteractionsStore(conn: Connection): InteractionsStore {
  const store = createStore<InteractionsView>({ open: [], resolved: [] });

  const resolve = (r: Resolved) =>
    store.update((v) => ({
      ...v,
      open: v.open.filter((x) => x.id !== r.id),
      resolved: [r, ...v.resolved.filter((x) => x.id !== r.id)].slice(0, KEEP_RESOLVED),
    }));

  const refresh = async () => {
    try {
      const r = await conn.call("interactions.list", {});
      store.update((v) => ({ ...v, error: undefined, open: sortOpened(r.items ?? []) }));
    } catch (e) {
      store.update((v) => ({ ...v, error: e instanceof Error ? e.message : String(e) }));
    }
  };

  const off = conn.onEvent((ev: Envelope) => {
    switch (ev.kind) {
      case LOCAL_RECONNECTED:
        void refresh();
        return;
      case "interaction.opened": {
        const it = ev.data as Interaction;
        store.update((v) => (v.open.some((x) => x.id === it.id) ? v : { ...v, open: sortOpened([...v.open, it]) }));
        return;
      }
      case "interaction.resolved": {
        const d = ev.data as InteractionResolved;
        resolve({ id: d.id, outcome: d.outcome, by: d.by, at: ev.at });
        return;
      }
    }
  });

  return {
    get: store.get,
    subscribe: store.subscribe,
    refresh,
    async respond(id, response) {
      try {
        const r = await conn.call("interaction.respond", { interaction: id, response, client_id: clientID() });
        resolve({ id, outcome: r.outcome, at: Date.now() });
        return r.outcome;
      } catch (e) {
        // 只收第一条回答：别处先回答了，本端按已有结果收尾，不当成错误
        if (e instanceof ApiError && e.code === "conflict" && e.reason === "answered") {
          const outcome = String(e.details?.["outcome"] ?? "answered");
          resolve({ id, outcome, by: "other", at: Date.now() });
          return outcome;
        }
        throw e;
      }
    },
    forSession(session) {
      return store.get().open.filter((x) => inTree(x.session, session));
    },
    absorb(session, items) {
      store.update((v) => {
        const done = new Set(v.resolved.map((r) => r.id));
        const keep = v.open.filter((x) => !inTree(x.session, session));
        const fresh = items.filter((x) => !done.has(x.id));
        const next = sortOpened([...keep, ...fresh]);
        if (next.length === v.open.length && next.every((x, i) => x === v.open[i] || sameInteraction(x, v.open[i]))) return v;
        return { ...v, open: next };
      });
    },
    close: off,
  };
}

function inTree(s: string, root: string): boolean {
  return s === root || s.startsWith(root + "/");
}

function sameInteraction(a: Interaction, b: Interaction): boolean {
  return a.id === b.id && a.session === b.session && a.opened_at === b.opened_at;
}

function sortOpened(xs: Interaction[]): Interaction[] {
  return [...xs].sort((a, b) => a.opened_at - b.opened_at);
}
