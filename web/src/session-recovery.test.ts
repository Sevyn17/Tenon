import { describe, expect, it } from "vitest";
import { createClient } from "./index";
import type { Transport } from "./transport";
import type { Entry, Envelope, SessionSnapshot } from "./wire.gen";

const snap = (over: Partial<SessionSnapshot> = {}): SessionSnapshot => ({
  id: "s",
  origin: "new",
  title: "",
  run_state: "idle",
  settings: { max_steps: 0 },
  log: { base: 0, len: 0 },
  interactions: [],
  cursor: 5,
  ...over,
});

const entry = (idx: number, client_ids?: string[]): Entry => ({
  id: `e${idx}`,
  idx,
  turn: 1,
  at: 0,
  item: { kind: "assistant" },
  ...(client_ids ? { client_ids } : {}),
});

type Handler = (args: Record<string, unknown>) => unknown;

function fake(handlers: Record<string, Handler>) {
  const calls: { cmd: string; args: Record<string, unknown> }[] = [];
  let emit: (e: Envelope) => void = () => {};
  const t: Transport = {
    async call(cmd, args) {
      calls.push({ cmd, args: args as Record<string, unknown> });
      const h = handlers[cmd];
      return h ? await h(args as Record<string, unknown>) : {};
    },
    start(e) {
      emit = e;
    },
    close() {},
  };
  return { t, calls, emit: (e: Envelope) => emit(e) };
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("SessionStore断线 /背压恢复", () => {
  it("背压后用快照覆盖 queued/run_state，清易失状态；订阅失败后可恢复 ready", async () => {
    let current = snap({ run_state: "running" });
    let failSubscribe = false;
    const f = fake({
      "session.get": () => current,
      "session.entries": () => ({ entries: [], base: 0, len: 0 }),
      "session.subscribe": () => {
        if (failSubscribe) throw new Error("订阅失败");
        return {};
      },
    });
    const client = createClient({ transport: f.t });
    const st = client.session("s");
    await st.open();
    const event = (kind: string, data: unknown) => f.emit({ v: 2, session: "s", kind, at: 0, data });

    event("turn.started", { turn: 1, inputs: [] });
    event("message.started", { stream_id: "stream", entry_id: "e1", part: "text", attempt: 1 });
    event("tool.started", { call_id: "tool", name: "shell", entry_id: "e2" });
    event("model.retrying", { attempt: 2, max: 3, wait_ms: 100, reason: "HTTP500" });
    event("queue.changed", { queued: [{ id: "stale", content: [{ kind: "text", text: "old" }], at: 1 }] });
    expect(st.get().streams).toHaveLength(1);
    expect(st.get().tools).toHaveLength(1);
    expect(st.get().retry).not.toBeNull();
    expect(st.get().queued.map((q) => q.id)).toEqual(["stale"]);

    current = snap({
      run_state: "idle",
      cursor: 7,
      queued: [{ id: "fresh", content: [{ kind: "text", text: "new" }], at: 2 }],
    });
    failSubscribe = true;
    event("session.lagged", { cursor: 5 });
    await flush();

    expect(st.get().status).toBe("error");
    expect(st.get().error).toBe("订阅失败");
    expect(st.get().streams).toEqual([]);
    expect(st.get().tools).toEqual([]);
    expect(st.get().retry).toBeNull();
    expect(st.get().runState).toBe("idle");
    expect(st.get().queued.map((q) => q.id)).toEqual(["fresh"]);

    failSubscribe = false;
    event("session.lagged", { cursor: 5 });
    await flush();
    expect(st.get().status).toBe("ready");
    expect(st.get().error).toBeUndefined();
    expect(st.get().queued.map((q) => q.id)).toEqual(["fresh"]);
    expect(f.calls.filter((c) => c.cmd === "session.get")).toHaveLength(3);
    //快照游标已到7，仍从断点5回放，避免漏掉快照前落库的持久条目。
    expect(f.calls.filter((c) => c.cmd === "session.subscribe").map((c) => c.args.after)).toEqual([5, 5, 5]);
    client.close();
  });
});

describe("SessionStore条目与实例清理", () => {
  it("entry.appended按本次事件的 client_ids收 outbox，即使它不是排序后的尾条目", async () => {
    const f = fake({
      "session.get": () => snap({ log: { base: 0, len: 6 } }),
      "session.entries": () => ({ entries: [entry(5, ["other"])], base: 0, len: 6 }),
      "session.send": () => ({}),
    });
    const client = createClient({ transport: f.t });
    const st = client.session("s");
    await st.open();
    await st.send("待确认");
    const cid = st.get().outbox[0].client_id;

    f.emit({ v: 2, session: "s", kind: "entry.appended", at: 0, cursor: 6, data: { entry: entry(4, [cid]) } });
    expect(st.get().entries.map((e) => e.idx)).toEqual([4, 5]);
    expect(st.get().outbox).toEqual([]);
    client.close();
  });

  it("同 id打开期间复用；最后 close淘汰，client.close也清空 opened", async () => {
    const f = fake({
      "session.get": () => snap(),
      "session.entries": () => ({ entries: [], base: 0, len: 0 }),
    });
    const client = createClient({ transport: f.t });
    const first = client.session("s");
    await first.open();
    await first.open();
    expect(client.session("s")).toBe(first);

    first.close();
    expect(client.session("s")).toBe(first);
    first.close();
    const second = client.session("s");
    expect(second).not.toBe(first);

    await second.open();
    await first.open();
    first.close();
    expect(client.session("s")).toBe(second);
    client.close();
    expect(client.session("s")).not.toBe(second);
  });
});
