// M12：真排队（能力 send.queue）与 Plan 模式（能力 work_mode）在客户端 SDK 里的部分：
// send {queue} 的参数、回合中缺省仍是插话、unqueue、reducer 记 queued（快照覆盖、queue.changed 全量）与 workMode。
import { describe, expect, it } from "vitest";
import { createClient } from "./index";
import { applySnapshot, initialState, reduce } from "./session-reducer";
import type { Transport } from "./transport";
import type { Envelope, SessionSnapshot } from "./wire.gen";

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

const running = {
  "session.get": () => snap({ run_state: "running" }),
  "session.entries": () => ({ entries: [], base: 0, len: 0 }),
};

describe("SessionStore 排队发送", () => {
  it("回合中带 queue：session.send {queue:true}，进了队列后不挂在 outbox 里", async () => {
    const f = fake({ ...running, "session.send": (a) => ({ client_id: a.client_id, queued: true }) });
    const st = createClient({ transport: f.t }).session("s");
    await st.open();
    await st.send("下一件事", undefined, { queue: true });
    const sent = f.calls.find((c) => c.cmd === "session.send")!;
    expect(sent.args).toMatchObject({ session: "s", text: "下一件事", queue: true });
    expect(typeof sent.args.client_id).toBe("string");
    expect(f.calls.some((c) => c.cmd === "session.steer")).toBe(false);
    expect(st.get().outbox).toEqual([]);
  });

  it("回合中不带 queue：仍是插话（session.steer）", async () => {
    const f = fake({ ...running, "session.steer": () => ({}) });
    const st = createClient({ transport: f.t }).session("s");
    await st.open();
    await st.send("插一句");
    expect(f.calls.find((c) => c.cmd === "session.steer")?.args).toMatchObject({ text: "插一句" });
    expect(f.calls.some((c) => c.cmd === "session.send")).toBe(false);
  });

  it("空闲时带 queue：普通发送，不带 queue 字段", async () => {
    const f = fake({ "session.send": () => ({ queued: false }) });
    await createClient({ transport: f.t }).session("s").send("你好", undefined, { queue: true });
    expect(f.calls.map((c) => c.cmd)).toEqual(["session.send"]);
    expect(f.calls[0].args.queue).toBeUndefined();
  });

  it("unqueue：removed=true 时从 queued 里拿掉；false 原样返回", async () => {
    let removed = true;
    const f = fake({
      "session.get": () => snap({ run_state: "running", queued: [{ id: "q1", content: [{ kind: "text", text: "a" }], at: 1 }] }),
      "session.entries": () => ({ entries: [], base: 0, len: 0 }),
      "session.unqueue": () => ({ removed }),
    });
    const st = createClient({ transport: f.t }).session("s");
    await st.open();
    expect(st.get().queued.map((q) => q.id)).toEqual(["q1"]);
    removed = false;
    expect(await st.unqueue("q1")).toBe(false);
    expect(st.get().queued.map((q) => q.id)).toEqual(["q1"]);
    removed = true;
    expect(await st.unqueue("q1")).toBe(true);
    expect(f.calls.find((c) => c.cmd === "session.unqueue")?.args).toEqual({ session: "s", id: "q1" });
    expect(st.get().queued).toEqual([]);
  });
});

describe("reducer：queued 与 workMode", () => {
  const ev = (kind: string, data: unknown): Envelope => ({ v: 2, session: "s", kind, at: 0, data }) as Envelope;
  const q = (id: string) => ({ id, content: [{ kind: "text", text: id }], at: 1 });

  it("queue.changed 全量替换；空数组清空；快照覆盖", () => {
    let s = initialState("s");
    expect(s.queued).toEqual([]);
    s = reduce(s, ev("queue.changed", { queued: [q("a"), q("b")] }));
    expect(s.queued.map((x) => x.id)).toEqual(["a", "b"]);
    s = reduce(s, ev("queue.changed", { queued: [] }));
    expect(s.queued).toEqual([]);
    s = reduce(s, ev("queue.changed", { queued: [q("x")] }));
    s = applySnapshot(s, snap({ queued: [q("c")] }));
    expect(s.queued.map((x) => x.id)).toEqual(["c"]);
    s = applySnapshot(s, snap());
    expect(s.queued).toEqual([]);
  });

  it("workMode：快照与 settings_changed，缺省 execute", () => {
    let s = initialState("s");
    expect(s.workMode).toBe("execute");
    s = applySnapshot(s, snap({ settings: { max_steps: 0, work_mode: "plan" } }));
    expect(s.workMode).toBe("plan");
    s = reduce(s, ev("session.settings_changed", { settings: { max_steps: 0 } }));
    expect(s.workMode).toBe("execute");
    s = reduce(s, ev("session.settings_changed", { settings: { max_steps: 0, work_mode: "plan" } }));
    expect(s.workMode).toBe("plan");
  });
});
