// 连接级 notice（M10 协议T19）：不订阅会话也能收到；hello 应答之前到的补发不丢；level 规整；非 notice 事件不交出。
import { describe, expect, it, vi } from "vitest";
import { createClient } from "./index";
import { NOTICE_BUFFER, noticeOf } from "./notices";
import type { Transport } from "./transport";
import type { Envelope } from "./wire.gen";

function fake() {
  let emit: (e: Envelope) => void = () => {};
  const calls: string[] = [];
  const t: Transport = {
    async call(cmd) {
      calls.push(cmd);
      if (cmd === "hello") {
        // 服务端在 hello 应答之前先推补发（replay）
        emit(ev("", { level: "warn", source: "startup", text: "启动时的告警", replay: true }, 100));
        return { capabilities: ["notices"], protocol: 2 };
      }
      return { items: [], sessions: [] };
    },
    start(e) {
      emit = e;
    },
    close() {},
  };
  return { t, calls, emit: (e: Envelope) => emit(e) };
}

const ev = (session: string, data: unknown, at = 1, kind = "notice"): Envelope => ({ v: 2, session, kind, at, data });

describe("连接级 notice", () => {
  it("noticeOf：只认 notice，level 不认识的按 info，source 空串当没有", () => {
    expect(noticeOf(ev("s1", { level: "error", source: "settings", text: "保存失败" }, 5))).toEqual({
      level: "error",
      text: "保存失败",
      source: "settings",
      session: "s1",
      replay: false,
      at: 5,
    });
    expect(noticeOf(ev("", { level: "debug", source: "", text: "x" }))).toMatchObject({ level: "info", source: undefined, session: "" });
    expect(noticeOf(ev("", { text: "x" }, 1, "turn.started"))).toBeNull();
    expect(noticeOf(ev("", null))).toBeNull();
  });

  it("hello 应答之前到的补发、监听方挂上之前到的都先存着，第一个监听方一次收到；之后实时交出", async () => {
    const f = fake();
    const c = createClient({ transport: f.t });
    await c.start();
    f.emit(ev("", { level: "info", source: "workspace", text: "工作区已切换" }, 200));
    const got: string[] = [];
    const off = c.notices.on((n) => got.push(`${n.text}|${n.replay}|${n.level}|${n.session || "全局"}`));
    expect(got).toEqual(["启动时的告警|true|warn|全局", "工作区已切换|false|info|全局"]);
    // 没订阅过的会话上的 notice 也交出
    f.emit(ev("s9", { level: "error", source: "MCP", text: "连不上" }));
    expect(got.at(-1)).toBe("连不上|false|error|s9");
    off();
    expect(f.calls).not.toContain("session.subscribe");
  });

  it("没有监听方时最多存 NOTICE_BUFFER 条（留最新的）；close 之后不再交出", () => {
    const f = fake();
    const c = createClient({ transport: f.t });
    for (let i = 0; i < NOTICE_BUFFER + 5; i++) f.emit(ev("", { level: "info", text: `n${i}` }));
    const fn = vi.fn();
    c.notices.on(fn);
    expect(fn).toHaveBeenCalledTimes(NOTICE_BUFFER);
    expect(fn.mock.calls[0][0].text).toBe("n5");
    c.close();
    f.emit(ev("", { level: "info", text: "晚到" }));
    expect(fn).toHaveBeenCalledTimes(NOTICE_BUFFER);
  });
});
