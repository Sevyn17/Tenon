// @ybase/client：YBase 应用协议（protocol 2）的客户端。不依赖 React 与任何 UI 框架，
// 用它可以按契约另写一套界面（Vue、原生 JS、移动端……），见 client/README.md。

import { autoTransport, connect, type Connection, type Transport } from "./transport";
import { createSessionStore, type SessionStore } from "./session";
import { createSessionsStore, type SessionsStore } from "./sessions";
import { createInteractionsStore, type InteractionsStore } from "./interactions";
import { createStore, type ReadStore } from "./store";
import { PROTOCOL, type HelloResult } from "./wire.gen";

export * from "./commands";
export * from "./store";
export * from "./transport";
export * from "./session-reducer";
export * from "./session";
export * from "./sessions";
export * from "./interactions";
export type * from "./wire.gen";
export { PROTOCOL } from "./wire.gen";

export interface ClientOptions {
  /** 传输；缺省按页面地址自动选（?ws= / ?token= 用 WebSocket，否则 Wails）。 */
  transport?: Transport;
  /** hello 里上报的客户端名。 */
  client?: string;
}

export interface Client {
  conn: Connection;
  call: Connection["call"];
  /** 握手结果（模型、工作目录、身份、能力）；握手前为 null。 */
  hello: ReadStore<HelloResult | null>;
  /** 握手；连接建立后调用一次。 */
  start(): Promise<HelloResult>;
  /** 是否具备某能力（hello.capabilities）。 */
  can(cap: string): boolean;
  sessions: SessionsStore;
  interactions: InteractionsStore;
  /** 取得（或建）某会话的 store；同一 id 返回同一实例。 */
  session(id: string): SessionStore;
  close(): void;
}

export function createClient(opts: ClientOptions = {}): Client {
  const conn = connect(opts.transport ?? autoTransport());
  const hello = createStore<HelloResult | null>(null);
  const sessions = createSessionsStore(conn);
  const interactions = createInteractionsStore(conn);
  const opened = new Map<string, SessionStore>();
  return {
    conn,
    call: conn.call,
    hello,
    async start() {
      const r = await conn.call("hello", { client: opts.client ?? "ybase-web", protocol: { min: PROTOCOL, max: PROTOCOL } });
      hello.set(r);
      void sessions.refresh();
      void interactions.refresh();
      return r;
    },
    can: (cap) => hello.get()?.capabilities.includes(cap) ?? false,
    sessions,
    interactions,
    session(id) {
      let s = opened.get(id);
      if (!s) {
        s = createSessionStore(conn, id);
        opened.set(id, s);
      }
      return s;
    },
    close() {
      for (const s of opened.values()) s.close();
      sessions.close();
      interactions.close();
      conn.close();
    },
  };
}
