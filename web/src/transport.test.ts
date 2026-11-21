import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./commands";
import { connect, wsTransport, type Transport } from "./transport";
import type { Envelope } from "./wire.gen";

/** 假 WebSocket：记录发出的帧，测试手动触发 open / message / close。 */
class FakeWS {
  static OPEN = 1;
  static all: FakeWS[] = [];
  readyState = 0;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((m: { data: string }) => void) | null = null;
  constructor(public url: string) {
    FakeWS.all.push(this);
  }
  send(s: string) {
    this.sent.push(s);
  }
  close() {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.onclose?.();
  }
  open() {
    this.readyState = FakeWS.OPEN;
    this.onopen?.();
  }
  recv(f: unknown) {
    this.onmessage?.({ data: JSON.stringify(f) });
  }
  pings() {
    return this.sent.filter((s) => "ping" in JSON.parse(s)).length;
  }
}

beforeEach(() => {
  FakeWS.all = [];
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", FakeWS);
  vi.spyOn(Math, "random").mockReturnValue(0.5);
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// 穿插项 9：WebSocket 心跳。
describe("wsTransport 心跳", () => {
  const start = () => {
    const t = wsTransport("ws://x/ws", { heartbeatMs: 1000, heartbeatTimeoutMs: 300 });
    const states: string[] = [];
    t.start(
      () => {},
      (s) => states.push(s),
    );
    const s = FakeWS.all[0]!;
    s.open();
    return { t, s, states };
  };

  it("连上后按间隔发 ping，收到回应不断开", () => {
    const { s } = start();
    vi.advanceTimersByTime(1000);
    expect(s.pings()).toBe(1);
    s.recv({ pong: 1 });
    vi.advanceTimersByTime(1000);
    expect(s.pings()).toBe(2);
    s.recv({ pong: 2 });
    vi.advanceTimersByTime(500);
    expect(s.readyState).toBe(FakeWS.OPEN);
  });

  it("ping 之后超时没有任何帧，关掉连接并重连", () => {
    const { s, states } = start();
    vi.advanceTimersByTime(1000 + 300);
    expect(s.readyState).toBe(3);
    expect(states).toContain("reconnecting");
    vi.advanceTimersByTime(1000); // 重连退避 1 s
    expect(FakeWS.all.length).toBe(2);
  });

  it("任何帧（事件、应答）都算活着", () => {
    const { s } = start();
    vi.advanceTimersByTime(1000);
    s.recv({ event: { kind: "x" } });
    vi.advanceTimersByTime(900);
    expect(s.readyState).toBe(FakeWS.OPEN);
  });

  it("关闭后不再发心跳", () => {
    const { t, s } = start();
    t.close();
    vi.advanceTimersByTime(5000);
    expect(s.pings()).toBe(0);
    expect(FakeWS.all.length).toBe(1);
  });
});

describe("wsTransport异常与重连退避", () => {
  it("send同步抛 DOMException时包装 ApiError，后续调用可正常应答", async () => {
    const t = wsTransport("ws://x/ws");
    t.start(
      () => {},
      () => {},
    );
    const s = FakeWS.all[0]!;
    s.open();
    const send = s.send.bind(s);
    s.send = () => {
      throw new DOMException("socket is closing", "InvalidStateError");
    };
    let thrown: unknown;
    try {
      await t.call("session.get", { session: "s" });
    } catch (e) {
      thrown = e;
    }
    expect(thrown).toBeInstanceOf(ApiError);
    expect(thrown).toMatchObject({ code: "internal", message: "socket is closing", retry: false });

    s.send = send;
    const next = t.call("session.get", { session: "s2" });
    const frame = JSON.parse(s.sent[0]);
    s.recv({ id: frame.id, ok: true, result: { id: "s2" } });
    await expect(next).resolves.toEqual({ id: "s2" });
    t.close();
  });

  it("重连延迟带 jitter：random=0时使用退避的0.8倍", () => {
    vi.mocked(Math.random).mockReturnValue(0);
    const t = wsTransport("ws://x/ws");
    t.start(
      () => {},
      () => {},
    );
    const s = FakeWS.all[0]!;
    s.open();
    s.close();
    vi.advanceTimersByTime(799);
    expect(FakeWS.all).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeWS.all).toHaveLength(2);
    t.close();
  });
});

describe("connect关闭", () => {
  it("close立即清空事件监听，即使底层之后还 emit也不再投递", () => {
    let emit: (e: Envelope) => void = () => {};
    const transport: Transport = {
      async call() {
        return {};
      },
      start(fn) {
        emit = fn;
      },
      close() {},
    };
    const conn = connect(transport);
    const seen: string[] = [];
    conn.onEvent((e) => seen.push(e.kind));
    conn.close();
    emit({ v: 2, session: "s", kind: "turn.ended", at: 0, data: {} });
    expect(seen).toEqual([]);
  });
});
