import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { wsTransport } from "./transport";

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
});
afterEach(() => {
  vi.useRealTimers();
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
