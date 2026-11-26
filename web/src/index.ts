// @tenon/client：Tenon 应用协议（protocol 2）的客户端。不依赖 React 与任何 UI 框架，
// 用它可以按契约另写一套界面（Vue、原生 JS、移动端……），见 client/README.md。

import { autoTransport, connect, type Connection, type Transport } from "./transport";
import { createSessionStore, type SessionStore } from "./session";
import { CAP_CHANGED_FULL, CAP_QUERY, createSessionsStore, workspaceOf, type SessionsStore } from "./sessions";
import { createInteractionsStore, type InteractionsStore } from "./interactions";
import { createNoticeFeed, type NoticeFeed } from "./notices";
import { createStore, type ReadStore } from "./store";
import { ApiError } from "./commands";
import { PROTOCOL, type HelloResult } from "./wire.gen";

export * from "./commands";
export * from "./store";
export * from "./transport";
export * from "./session-reducer";
export * from "./session";
export * from "./sessions";
export * from "./interactions";
export * from "./notices";
export type * from "./wire.gen";
export { PROTOCOL } from "./wire.gen";

export interface ClientOptions {
  /** 上传地址（缺省同源 /api/artifacts；嵌入到别的页面、后端在别处时改）。 */
  uploadURL?: string;
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
  /** 大附件上传（POST /api/artifacts，能力 upload；06 10.9）。onProgress 报 0–1。 */
  upload(session: string, file: File, onProgress?: (p: number) => void): Promise<string>;
  sessions: SessionsStore;
  interactions: InteractionsStore;
  /**
   * 连接级 notice（能力 notices）：不需要订阅会话，全局的 session 为空；hello 后的补发带 replay。
   * 创建客户端时就开始收（补发可能先于 hello 应答），没有监听方时先存最近 20 条。
   */
  notices: NoticeFeed;
  /** 取得（或建）某会话的 store；同一 id 返回同一实例。 */
  session(id: string): SessionStore;
  close(): void;
}

/** HTTP 上传的路径（appapi.UploadPath）。 */
export const UPLOAD_PATH = "/api/artifacts";

/**
 * 客户端自己产生的上传失败（服务端没给错误体时）放在 ApiError.details.reason，供界面按码翻译；
 * message 只是英文短句。network：请求没发出去或没有应答；http：应答不是 200 且没有协议错误体（details.status 为 HTTP 状态码）。
 */
export const UPLOAD_ERR_NETWORK = "upload_network";
export const UPLOAD_ERR_HTTP = "upload_http";

/** POST multipart 上传：凭同源 cookie（网页服务）或资源服务器（桌面端）鉴权；返回 artifact:// 引用。 */
function uploadFile(url: string, session: string, file: File, onProgress?: (p: number) => void): Promise<string> {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    fd.append("session", session);
    fd.append("mime", file.type || "application/octet-stream");
    fd.append("file", file, file.name || "upload");
    const x = new XMLHttpRequest();
    x.open("POST", url);
    x.withCredentials = true;
    x.responseType = "json";
    if (onProgress) x.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total);
    x.onerror = () => reject(new ApiError({ code: "internal", message: "upload failed: network error", retry: true, details: { reason: UPLOAD_ERR_NETWORK } }));
    x.onload = () => {
      const b = x.response as { ref?: string; code?: string; message?: string; retry?: boolean } | null;
      if (x.status === 200 && b?.ref) resolve(b.ref);
      else if (b?.code) reject(new ApiError({ code: b.code, message: b.message ?? `upload failed: HTTP ${x.status}`, retry: !!b.retry }));
      else reject(new ApiError({ code: "internal", message: `upload failed: HTTP ${x.status}`, retry: !!b?.retry, details: { reason: UPLOAD_ERR_HTTP, status: x.status } }));
    };
    x.send(fd);
  });
}

export function createClient(opts: ClientOptions = {}): Client {
  const conn = connect(opts.transport ?? autoTransport());
  const hello = createStore<HelloResult | null>(null);
  const has = (cap: string) => hello.get()?.capabilities.includes(cap) ?? false;
  // M7：sessions.changed 带全套字段时按工作区插入未知会话；搜索、归档、子会话走 sessions.query。
  const sessions = createSessionsStore(conn, 50, {
    full: () => has(CAP_CHANGED_FULL),
    workspace: () => workspaceOf(hello.get()),
    query: () => has(CAP_QUERY),
  });
  const interactions = createInteractionsStore(conn);
  const notices = createNoticeFeed(conn);
  const opened = new Map<string, SessionStore>();
  return {
    conn,
    call: conn.call,
    hello,
    async start() {
      const r = await conn.call("hello", { client: opts.client ?? "tenon-web", protocol: { min: PROTOCOL, max: PROTOCOL } });
      hello.set(r);
      void sessions.refresh();
      void interactions.refresh();
      return r;
    },
    can: has,
    upload: (session, file, onProgress) => uploadFile(opts.uploadURL ?? UPLOAD_PATH, session, file, onProgress),
    sessions,
    interactions,
    notices,
    session(id) {
      let s = opened.get(id);
      if (!s) {
        s = createSessionStore(conn, id, {
          onSnapshot: (snap) => interactions.absorb(id, snap.interactions ?? []),
          onClose: () => {
            if (opened.get(id) === s) opened.delete(id);
          },
        });
        opened.set(id, s);
      }
      return s;
    },
    close() {
      for (const s of opened.values()) s.close();
      opened.clear();
      sessions.close();
      interactions.close();
      notices.close();
      conn.close();
    },
  };
}
