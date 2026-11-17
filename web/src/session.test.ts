import { describe, expect, it } from "vitest";
import { createClient } from "./index";
import type { Transport } from "./transport";
import type { Envelope, Interaction, SessionSnapshot } from "./wire.gen";

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

const inter = (id: string, session = "s", opened_at = 1): Interaction => ({
  id,
  session,
  kind: "approval",
  title: "确认",
  approval: { tool: "shell" },
  responders: [],
  opened_at,
});

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((r) => (resolve = r));
  return { promise, resolve };
}

type Handler = (args: Record<string, unknown>) => unknown;

/** 假传输：记下命令顺序；handlers 决定返回值（可返回 Promise 控制时序）。 */
function fake(handlers: Record<string, Handler>) {
  const calls: string[] = [];
  let emit: (e: Envelope) => void = () => {};
  const t: Transport = {
    async call(cmd, args) {
      calls.push(cmd);
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

const tick = () => new Promise((r) => setTimeout(r, 0));

describe("SessionStore 发送时序", () => {
  it("open 还没订阅好时 send 先等订阅建立（首页先订阅再发送）", async () => {
    const gate = deferred();
    const f = fake({
      "session.get": () => snap(),
      "session.entries": () => ({ entries: [], base: 0, len: 0 }),
      "session.subscribe": () => gate.promise.then(() => ({})),
      "session.send": () => ({}),
    });
    const c = createClient({ transport: f.t });
    const st = c.session("s");
    const opening = st.open();
    const sending = st.send("你好");
    await tick();
    expect(f.calls).toContain("session.subscribe");
    expect(f.calls).not.toContain("session.send");
    expect(st.get().outbox.map((o) => o.state)).toEqual(["sending"]); // 先出现在待发区
    gate.resolve();
    await Promise.all([opening, sending]);
    expect(f.calls.indexOf("session.send")).toBeGreaterThan(f.calls.indexOf("session.subscribe"));
  });

  it("没有人打开会话时照常立即发送", async () => {
    const f = fake({ "session.send": () => ({}) });
    const c = createClient({ transport: f.t });
    await c.session("s").send("你好");
    expect(f.calls).toEqual(["session.send"]);
  });

  it("加载失败也照发，不卡住", async () => {
    const f = fake({
      "session.get": () => Promise.reject(new Error("断了")),
      "session.send": () => ({}),
    });
    const c = createClient({ transport: f.t });
    const st = c.session("s");
    const opening = st.open();
    await st.send("你好");
    await opening;
    expect(f.calls).toEqual(["session.get", "session.send"]);
    expect(st.get().status).toBe("error");
  });
});

describe("交互 store 读会话快照", () => {
  it("订阅前就打开的交互（只在快照里）也出现在 open 里", async () => {
    const f = fake({
      "session.get": () => snap({ interactions: [inter("i1")] }),
      "session.entries": () => ({ entries: [], base: 0, len: 0 }),
    });
    const c = createClient({ transport: f.t });
    await c.session("s").open();
    expect(c.interactions.get().open.map((x) => x.id)).toEqual(["i1"]);
    expect(c.interactions.forSession("s").map((x) => x.id)).toEqual(["i1"]);
  });

  it("快照是本会话树的全集：不在快照里的移出 open，别的会话不动；已 resolved 的不复活", () => {
    const f = fake({});
    const c = createClient({ transport: f.t });
    const opened = (it: Interaction): Envelope => ({ v: 2, session: it.session.split("/")[0], kind: "interaction.opened", at: 0, cursor: 1, data: it });
    f.emit(opened(inter("stale", "s", 1)));
    f.emit(opened(inter("other", "t", 2)));
    f.emit(opened(inter("gone", "s/sub", 3)));
    f.emit({ v: 2, session: "s", kind: "interaction.resolved", at: 0, cursor: 2, data: { id: "gone", session: "s/sub", outcome: "approved" } });
    c.interactions.absorb("s", [inter("child", "s/sub", 4), inter("gone", "s/sub", 3)]);
    expect(c.interactions.get().open.map((x) => x.id)).toEqual(["other", "child"]);
  });

  it("快照与现状相同时不触发更新", () => {
    const f = fake({});
    const c = createClient({ transport: f.t });
    c.interactions.absorb("s", [inter("i1")]);
    const before = c.interactions.get();
    c.interactions.absorb("s", [inter("i1")]);
    expect(c.interactions.get()).toBe(before);
  });
});
