// 传输层：对上只暴露 call(cmd, args) 与 onEvent(fn)（protocol 2，docs/06 - 应用协议契约.md 10.9）。
// 两种实现运行时选择：
//   - Wails（桌面端）：绑定方法 main.Service.Call(cmd, argsJSON) + Wails 事件 "ybase:event"；不走网络端口。
//   - WebSocket（网页服务）：ws://host/api?token=...，请求帧 {"id","cmd","args"}，
//     应答帧 {"id","ok","result"|"error"}，事件帧 {"event": Envelope}。
// 选择规则：页面 URL 带 ?ws=<地址> 或 ?token=<令牌>（同源 /api）时用 WebSocket，否则用 Wails。
//
// 事件只推连接订阅了的会话（session.subscribe）。WebSocket 断线重连后服务端的订阅全部丢失：
// 传输层发本地事件 local.reconnected，订阅方从最后收到的游标重新订阅（不漏不重）。

import { ApiError, LOCAL_RECONNECTED, type CommandMap } from "./api";
import type { Envelope, ProtocolError } from "./wire.gen";

type Listener = (e: Envelope) => void;

interface Impl {
  call(cmd: string, args: unknown): Promise<unknown>;
  start(emit: Listener): void;
}

const WAILS_METHOD = "main.Service.Call";
const WAILS_EVENT = "ybase:event";

function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const msg = err instanceof Error ? err.message : String(err);
  try {
    const b = JSON.parse(msg) as ProtocolError;
    if (b && typeof b.code === "string") return new ApiError(b);
  } catch {
    /* 非 JSON 错误文本 */
  }
  return new ApiError({ code: "internal", message: msg, retry: false });
}

function local(kind: string): Envelope {
  return { v: 2, session: "", kind, at: Date.now(), data: {} };
}

// ---- Wails ----

interface WailsRuntime {
  Call: { ByName(name: string, ...args: unknown[]): Promise<unknown> };
  Events: { On(name: string, cb: (ev: { data: unknown }) => void): () => void };
}

function wailsImpl(): Impl {
  // 运行时由 Wails 资源服务器在 /wails/runtime.js 提供；不打包、不引 npm 依赖。
  const url = "/wails/runtime.js";
  const rt = import(/* @vite-ignore */ url) as Promise<WailsRuntime>;
  return {
    async call(cmd, args) {
      const r = await rt;
      let out: unknown;
      try {
        out = await r.Call.ByName(WAILS_METHOD, cmd, args === undefined ? "" : JSON.stringify(args));
      } catch (e) {
        throw toApiError(e);
      }
      return typeof out === "string" && out !== "" ? JSON.parse(out) : out;
    },
    start(emit) {
      void rt.then((r) =>
        r.Events.On(WAILS_EVENT, (ev) => {
          const d = ev?.data;
          if (d && typeof d === "object") emit(d as Envelope);
        }),
      );
    },
  };
}

// ---- WebSocket ----

interface Pending {
  resolve(v: unknown): void;
  reject(e: unknown): void;
}

function wsImpl(url: string): Impl {
  let ws: WebSocket | null = null;
  let open: Promise<WebSocket> | null = null;
  let seq = 0;
  let everOpen = false;
  let emit: Listener = () => {};
  const pending = new Map<string, Pending>();

  const connect = (): Promise<WebSocket> => {
    if (open) return open;
    open = new Promise((resolve, reject) => {
      const s = new WebSocket(url);
      s.onopen = () => {
        ws = s;
        resolve(s);
        if (everOpen) emit(local(LOCAL_RECONNECTED)); // 新连接上没有订阅：通知订阅方重新订阅
        everOpen = true;
      };
      s.onerror = () => reject(new ApiError({ code: "internal", message: "WebSocket 连接失败", retry: true }));
      s.onclose = () => {
        ws = null;
        open = null;
        for (const p of pending.values())
          p.reject(new ApiError({ code: "internal", message: "连接已断开", retry: true }));
        pending.clear();
        setTimeout(() => void connect().catch(() => {}), 1000);
      };
      s.onmessage = (m) => {
        let f: { id?: string; ok?: boolean; result?: unknown; error?: ProtocolError; event?: Envelope };
        try {
          f = JSON.parse(String(m.data));
        } catch {
          return;
        }
        if (f.event) {
          emit(f.event);
          return;
        }
        if (f.id === undefined) return;
        const p = pending.get(f.id);
        if (!p) return;
        pending.delete(f.id);
        if (f.ok) p.resolve(f.result);
        else p.reject(new ApiError(f.error ?? { code: "internal", message: "未知错误", retry: false }));
      };
    });
    return open;
  };

  return {
    async call(cmd, args) {
      const s = ws ?? (await connect());
      const id = String(++seq);
      return new Promise((resolve, reject) => {
        pending.set(id, { resolve, reject });
        s.send(JSON.stringify({ id, cmd, args: args ?? {} }));
      });
    },
    start(fn) {
      emit = fn;
      void connect().catch(() => {});
    },
  };
}

function pick(): Impl {
  const q = new URLSearchParams(location.search);
  const ws = q.get("ws");
  const token = q.get("token");
  if (ws) return wsImpl(ws);
  if (token !== null) {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return wsImpl(`${proto}//${location.host}/api?token=${encodeURIComponent(token)}`);
  }
  return wailsImpl();
}

const impl = pick();
const listeners = new Set<Listener>();
impl.start((e) => {
  for (const l of listeners) l(e);
});

/** 执行一条 protocol 2 命令；失败抛 ApiError（含错误码与 details）。 */
export function call<K extends keyof CommandMap>(cmd: K, args: CommandMap[K][0]): Promise<CommandMap[K][1]> {
  return impl.call(cmd, args) as Promise<CommandMap[K][1]>;
}

/** 订阅连接上的全部事件（含本地事件 local.reconnected）；返回取消订阅函数。 */
export function onEvent(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** 客户端幂等键（send / steer / create / fork / interaction.respond 的 client_id）。 */
export function clientID(): string {
  const c = globalThis.crypto as Crypto | undefined;
  if (c && "randomUUID" in c) return c.randomUUID();
  return Date.now().toString(36) + "-" + Math.random().toString(36).slice(2, 10);
}
