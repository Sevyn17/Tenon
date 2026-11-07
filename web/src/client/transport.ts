// 传输层（protocol 2，docs/06 - 应用协议契约.md 10.9）：对上只暴露 call(cmd, args) 与 onEvent(fn)，外加连接状态。
// 两种实现：
//   - Wails（桌面端）：绑定方法 main.Service.Call(cmd, argsJSON) + Wails 事件 "tenon:event"；不走网络端口。
//   - WebSocket（网页服务）：请求帧 {"id","cmd","args"}，应答帧 {"id","ok","result"|"error"}，事件帧 {"event": Envelope}。
//
// 事件只推连接订阅了的会话。WebSocket 断线重连后服务端的订阅全部丢失：传输层发本地事件 local.reconnected，
// 订阅方从最后收到的游标重新订阅（不漏不重）。本文件不依赖 React 与任何界面代码。

import { ApiError, LOCAL_RECONNECTED, type CommandMap } from "./commands";
import { createStore, type ReadStore } from "./store";
import type { Envelope, ProtocolError } from "./wire.gen";

export type Listener = (e: Envelope) => void;

/** 连接状态：connecting 首次连接中；open 可用；reconnecting 断线后重连中；closed 已关闭（close() 之后）。 */
export type ConnectionState = "connecting" | "open" | "reconnecting" | "closed";

export interface Transport {
  call(cmd: string, args: unknown): Promise<unknown>;
  start(emit: Listener, state: (s: ConnectionState) => void): void;
  close(): void;
}

const WAILS_METHOD = "main.Service.Call";
const WAILS_EVENT = "tenon:event";

export function toApiError(err: unknown): ApiError {
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

/** Wails 传输。运行时由 Wails 资源服务器在 /wails/runtime.js 提供；不打包、不引 npm 依赖。 */
export function wailsTransport(runtimeURL = "/wails/runtime.js"): Transport {
  const rt = import(/* @vite-ignore */ runtimeURL) as Promise<WailsRuntime>;
  let off: (() => void) | null = null;
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
    start(emit, state) {
      state("connecting");
      rt.then(
        (r) => {
          off = r.Events.On(WAILS_EVENT, (ev) => {
            const d = ev?.data;
            if (d && typeof d === "object") emit(d as Envelope);
          });
          state("open");
        },
        () => state("reconnecting"),
      );
    },
    close() {
      off?.();
      off = null;
    },
  };
}

// ---- WebSocket ----

interface Pending {
  resolve(v: unknown): void;
  reject(e: unknown): void;
}

/** 心跳间隔：每隔这么久发一次 {"ping":n}（服务端 HeartbeatInterval 同值；服务端 60 s 收不到任何帧就断开）。 */
export const HEARTBEAT_MS = 25_000;
/** 发出心跳后这么久没收到任何帧，判定连接已死（电脑休眠、网络切换留下的半开连接），主动关掉走重连。 */
export const HEARTBEAT_TIMEOUT_MS = 10_000;

/** WebSocket 传输。断线后按退避自动重连（1s 起，最多 10s）；心跳见 HEARTBEAT_MS。 */
export function wsTransport(url: string, opts: { heartbeatMs?: number; heartbeatTimeoutMs?: number } = {}): Transport {
  const beatMs = opts.heartbeatMs ?? HEARTBEAT_MS;
  const beatTimeoutMs = opts.heartbeatTimeoutMs ?? HEARTBEAT_TIMEOUT_MS;
  let ws: WebSocket | null = null;
  let open: Promise<WebSocket> | null = null;
  let seq = 0;
  let everOpen = false;
  let closed = false;
  let delay = 1000;
  let emit: Listener = () => {};
  let setState: (s: ConnectionState) => void = () => {};
  const pending = new Map<string, Pending>();
  let beat: ReturnType<typeof setInterval> | undefined;
  let deadline: ReturnType<typeof setTimeout> | undefined;
  const stopBeat = () => {
    clearInterval(beat);
    clearTimeout(deadline);
    beat = deadline = undefined;
  };
  // 收到任何帧都算活着：取消等 pong 的计时。
  const alive = () => {
    clearTimeout(deadline);
    deadline = undefined;
  };
  const startBeat = (s: WebSocket) => {
    stopBeat();
    beat = setInterval(() => {
      if (s.readyState !== WebSocket.OPEN) return;
      s.send(JSON.stringify({ ping: ++seq }));
      if (deadline === undefined) deadline = setTimeout(() => s.close(), beatTimeoutMs);
    }, beatMs);
  };

  const connect = (): Promise<WebSocket> => {
    if (closed) return Promise.reject(new ApiError({ code: "internal", message: "连接已关闭", retry: false }));
    if (open) return open;
    open = new Promise((resolve, reject) => {
      const s = new WebSocket(url);
      s.onopen = () => {
        ws = s;
        delay = 1000;
        startBeat(s);
        resolve(s);
        setState("open");
        if (everOpen) emit(local(LOCAL_RECONNECTED)); // 新连接上没有订阅：通知订阅方重新订阅
        everOpen = true;
      };
      s.onerror = () => reject(new ApiError({ code: "internal", message: "WebSocket 连接失败", retry: true }));
      s.onclose = () => {
        stopBeat();
        ws = null;
        open = null;
        for (const p of pending.values()) p.reject(new ApiError({ code: "internal", message: "连接已断开", retry: true }));
        pending.clear();
        if (closed) {
          setState("closed");
          return;
        }
        setState(everOpen ? "reconnecting" : "connecting");
        setTimeout(() => void connect().catch(() => {}), delay);
        delay = Math.min(delay * 2, 10000);
      };
      s.onmessage = (m) => {
        alive();
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
    start(fn, state) {
      emit = fn;
      setState = state;
      state("connecting");
      void connect().catch(() => {});
    },
    close() {
      closed = true;
      ws?.close();
    },
  };
}

/** 按页面地址选择传输：URL 带 ?ws=<地址> 或 ?token=<令牌>（同源 /api）时用 WebSocket，否则用 Wails。 */
export function autoTransport(loc: Location = location): Transport {
  const q = new URLSearchParams(loc.search);
  const ws = q.get("ws");
  const token = q.get("token");
  if (ws) return wsTransport(ws);
  if (token !== null) {
    const proto = loc.protocol === "https:" ? "wss:" : "ws:";
    return wsTransport(`${proto}//${loc.host}/api?token=${encodeURIComponent(token)}`);
  }
  return wailsTransport();
}

/** 连接：类型化的 call、事件订阅与连接状态。由 createClient 持有；也可直接用。 */
export interface Connection {
  call<K extends keyof CommandMap>(cmd: K, args: CommandMap[K][0]): Promise<CommandMap[K][1]>;
  onEvent(fn: Listener): () => void;
  state: ReadStore<ConnectionState>;
  close(): void;
}

export function connect(t: Transport): Connection {
  const listeners = new Set<Listener>();
  const state = createStore<ConnectionState>("connecting");
  t.start(
    (e) => {
      for (const l of [...listeners]) l(e);
    },
    (s) => state.set(s),
  );
  return {
    call: (cmd, args) => t.call(cmd, args) as never,
    onEvent(fn) {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    state,
    close() {
      t.close();
      state.set("closed");
    },
  };
}

/** 客户端幂等键（send / steer / create / fork / interaction.respond 的 client_id）。 */
export function clientID(): string {
  const c = globalThis.crypto as Crypto | undefined;
  if (c && "randomUUID" in c) return c.randomUUID();
  return Date.now().toString(36) + "-" + Math.random().toString(36).slice(2, 10);
}
