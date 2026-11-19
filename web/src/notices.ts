// 连接级 notice（Mortise 客户端改进 M10 协议T19，能力 notices）：服务端把 notice 发给每条连接，不需要订阅会话；
// session 为空表示全局。hello 之后补发最近 20 条（replay=true，at 为原时间），补发可能先于 hello 的应答到达。
// 本文件把传输层事件里的 notice 挑出来、整理成 NoticeEvent 交给监听方：
//   - 还没有监听方时收到的先存着（最多 NOTICE_BUFFER 条），第一个监听方挂上时一次交出，这样界面晚于 start() 挂监听也不丢；
//   - 只认 kind 为 notice 的事件；旧服务端也会在订阅了的会话上推 notice，照样交出（session 非空）。

import type { Connection } from "./transport";
import type { Envelope, Notice, NoticeLevel } from "./wire.gen";

/** 连接级 notice 事件名（wire.EvNotice）。 */
export const NOTICE_EVENT = "notice";
/** 支持连接级送达的能力名。 */
export const CAP_NOTICES = "notices";
/** 没有监听方时最多先存几条（与服务端补发的条数一致）。 */
export const NOTICE_BUFFER = 20;

export interface NoticeEvent {
  level: NoticeLevel;
  text: string;
  source?: string;
  /** 为空：全局（不属于任何会话）。 */
  session: string;
  /** hello 后补发的旧通知（不该再弹提示）。 */
  replay: boolean;
  /** 服务端时间（毫秒）；补发的是原时间。 */
  at: number;
}

export type NoticeListener = (n: NoticeEvent) => void;

export interface NoticeFeed {
  /** 监听 notice；返回取消函数。第一个监听方会先收到之前存着的。 */
  on(fn: NoticeListener): () => void;
  close(): void;
}

/** 信封 → NoticeEvent；不是 notice 或载荷不对时 null。level 不认识的按 info。 */
export function noticeOf(e: Envelope): NoticeEvent | null {
  if (e.kind !== NOTICE_EVENT) return null;
  const d = e.data as Partial<Notice> | null;
  if (!d || typeof d.text !== "string") return null;
  const level: NoticeLevel = d.level === "warn" || d.level === "error" ? d.level : "info";
  return { level, text: d.text, source: d.source || undefined, session: e.session ?? "", replay: !!d.replay, at: e.at };
}

export function createNoticeFeed(conn: Connection): NoticeFeed {
  const listeners = new Set<NoticeListener>();
  let pending: NoticeEvent[] = [];
  const off = conn.onEvent((e) => {
    const n = noticeOf(e);
    if (!n) return;
    if (!listeners.size) {
      pending = [...pending, n].slice(-NOTICE_BUFFER);
      return;
    }
    for (const f of [...listeners]) f(n);
  });
  return {
    on(fn) {
      listeners.add(fn);
      if (pending.length) {
        const q = pending;
        pending = [];
        for (const n of q) fn(n);
      }
      return () => listeners.delete(fn);
    },
    close() {
      off();
      listeners.clear();
      pending = [];
    },
  };
}
