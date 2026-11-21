import { describe, expect, it } from "vitest";
import { LOCAL_RECONNECTED } from "./commands";
import { createClient } from "./index";
import { connect, wailsTransport, WAILS_RECONNECTED_EVENT, type WailsRuntime } from "./transport";
import type { Envelope, SessionSnapshot } from "./wire.gen";

// 客户端改进 M5：桌面端 bind.SwapAPI 之后发 Wails 事件 tenon:reconnected，传输层按 WebSocket 重连的同一条路径
// 通知上层（状态 reconnecting → open，再发 local.reconnected），上层重新拉列表、重新订阅。

/** 假 Wails 运行时：记录命令，测试手动触发事件。 */
function fakeRuntime(caps: string[] = ["reconnect_event"]) {
  const handlers = new Map<string, Set<(ev: { data: unknown }) => void>>();
  const calls: string[] = [];
  const rt: WailsRuntime = {
    Call: {
      async ByName(_name: string, ...args: unknown[]) {
        const cmd = String(args[0]);
        calls.push(cmd);
        switch (cmd) {
          case "hello":
            return JSON.stringify({ protocol: 2, server: "t", principal: { id: "local", role: "operator" }, capabilities: caps });
          case "sessions.list":
            return JSON.stringify({ items: [], next: 0 });
          case "interactions.list":
            return JSON.stringify({ items: [] });
          default:
            return "{}";
        }
      },
    },
    Events: {
      On(name, cb) {
        let set = handlers.get(name);
        if (!set) handlers.set(name, (set = new Set()));
        set.add(cb);
        return () => set.delete(cb);
      },
    },
  };
  const fire = (name: string, data: unknown) => {
    for (const cb of [...(handlers.get(name) ?? [])]) cb({ data });
  };
  const listening = () => [...handlers.values()].reduce((n, s) => n + s.size, 0);
  return { rt, calls, fire, listening };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("wailsTransport 换运行时通知", () => {
  it("收到 tenon:reconnected：状态 reconnecting → open，再发 local.reconnected", async () => {
    const f = fakeRuntime();
    const conn = connect(wailsTransport(Promise.resolve(f.rt)));
    const states: string[] = [];
    conn.state.subscribe((s) => states.push(s));
    const kinds: string[] = [];
    conn.onEvent((e) => kinds.push(e.kind));
    await flush();
    expect(conn.state.get()).toBe("open");

    // 普通协议事件照常转发，不算重连
    const ev: Envelope = { v: 2, session: "s1", kind: "turn.started", at: 1, data: {} };
    f.fire("tenon:event", ev);
    expect(kinds).toEqual(["turn.started"]);

    states.length = 0;
    f.fire(WAILS_RECONNECTED_EVENT, {});
    expect(states).toEqual(["reconnecting", "open"]);
    expect(kinds).toEqual(["turn.started", LOCAL_RECONNECTED]);
    expect(conn.state.get()).toBe("open");
    conn.close();
  });

  it("close 之后两个事件都退订", async () => {
    const f = fakeRuntime();
    const conn = connect(wailsTransport(Promise.resolve(f.rt)));
    await flush();
    expect(f.listening()).toBe(2);
    conn.close();
    expect(f.listening()).toBe(0);
    const kinds: string[] = [];
    conn.onEvent((e) => kinds.push(e.kind));
    f.fire(WAILS_RECONNECTED_EVENT, {});
    expect(kinds).toEqual([]);
    expect(conn.state.get()).toBe("closed");
  });

  it("上层看到重连：会话列表、待办交互重新拉取；hello 报 reconnect_event", async () => {
    const f = fakeRuntime();
    const client = createClient({ transport: wailsTransport(Promise.resolve(f.rt)) });
    await client.start();
    await flush();
    expect(client.can("reconnect_event")).toBe(true);
    const before = (cmd: string) => f.calls.filter((c) => c === cmd).length;
    const lists = before("sessions.list");
    const inter = before("interactions.list");

    f.fire(WAILS_RECONNECTED_EVENT, {});
    await flush();
    expect(before("sessions.list")).toBeGreaterThan(lists);
    expect(before("interactions.list")).toBe(inter + 1);
    client.close();
  });

  it("旧后端不报能力：can 为 false（前端退回整页刷新）", async () => {
    const f = fakeRuntime([]);
    const client = createClient({ transport: wailsTransport(Promise.resolve(f.rt)) });
    await client.start();
    expect(client.can("reconnect_event")).toBe(false);
    client.close();
  });
});

describe("wailsTransport生命周期与恢复快照", () => {
  it("close后 runtime才 resolve：不注册事件，也不重新打开", async () => {
    const f = fakeRuntime();
    let resolve!: (rt: WailsRuntime) => void;
    const runtime = new Promise<WailsRuntime>((r) => (resolve = r));
    const conn = connect(wailsTransport(runtime));
    conn.close();
    resolve(f.rt);
    await flush();
    expect(f.listening()).toBe(0);
    expect(conn.state.get()).toBe("closed");
  });

  it("runtime加载失败进入 closed终态", async () => {
    const conn = connect(wailsTransport(Promise.reject(new Error("运行时缺失"))));
    await flush();
    expect(conn.state.get()).toBe("closed");
    conn.close();
  });

  it("断线时回合结束且队列变化：重连后无幽灵流/工具，queued与快照一致", async () => {
    const handlers = new Map<string, Set<(ev: { data: unknown }) => void>>();
    const subscribeAfter: number[] = [];
    let gets = 0;
    let snapshot: SessionSnapshot = {
      id: "s",
      origin: "new",
      title: "",
      run_state: "running",
      settings: { max_steps: 0 },
      log: { base: 0, len: 0 },
      interactions: [],
      cursor: 5,
    };
    const rt: WailsRuntime = {
      Call: {
        async ByName(_name, ...args) {
          const cmd = String(args[0]);
          const payload = args[1] ? JSON.parse(String(args[1])) : {};
          if (cmd === "session.get") {
            gets++;
            return JSON.stringify(snapshot);
          }
          if (cmd === "session.entries") return JSON.stringify({ entries: [], base: 0, len: 0 });
          if (cmd === "session.subscribe") {
            subscribeAfter.push(payload.after);
            return "{}";
          }
          return "{}";
        },
      },
      Events: {
        On(name, cb) {
          let set = handlers.get(name);
          if (!set) handlers.set(name, (set = new Set()));
          set.add(cb);
          return () => set.delete(cb);
        },
      },
    };
    const fire = (name: string, data: unknown) => {
      for (const cb of [...(handlers.get(name) ?? [])]) cb({ data });
    };
    const event = (kind: string, data: unknown) => fire("tenon:event", { v: 2, session: "s", kind, at: 0, data });
    const client = createClient({ transport: wailsTransport(Promise.resolve(rt)) });
    const st = client.session("s");
    await st.open();
    await flush();

    event("turn.started", { turn: 1, inputs: [] });
    event("message.started", { stream_id: "ghost-stream", entry_id: "e1", part: "text", attempt: 1 });
    event("tool.started", { call_id: "ghost-tool", name: "shell", entry_id: "e2" });
    event("model.retrying", { attempt: 2, max: 2, wait_ms: 100, reason: "offline" });
    event("queue.changed", { queued: [{ id: "stale", content: [{ kind: "text", text: "old" }], at: 1 }] });
    expect(st.get().streams).toHaveLength(1);
    expect(st.get().tools).toHaveLength(1);
    expect(st.get().queued.map((q) => q.id)).toEqual(["stale"]);

    snapshot = {
      ...snapshot,
      run_state: "idle",
      cursor: 8,
      queued: [{ id: "fresh", content: [{ kind: "text", text: "new" }], at: 2 }],
    };
    fire(WAILS_RECONNECTED_EVENT, {});
    await flush();

    expect(st.get().status).toBe("ready");
    expect(st.get().error).toBeUndefined();
    expect(st.get().runState).toBe("idle");
    expect(st.get().streams).toEqual([]);
    expect(st.get().tools).toEqual([]);
    expect(st.get().retry).toBeNull();
    expect(st.get().queued.map((q) => q.id)).toEqual(["fresh"]);
    expect(gets).toBe(2);
    expect(subscribeAfter).toEqual([5, 5]);
    client.close();
  });
});
