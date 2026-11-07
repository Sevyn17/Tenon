// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { InteractionCard } from "./interaction";
import type { UIInteraction, UIResponse } from "./types";

afterEach(cleanup);

const base = { id: "i1", session: "s1", opened_at: 0 };
const btn = (name: RegExp | string) => screen.getByRole("button", { name });

/** 渲染一张卡，返回收到的回答。 */
function card(it: Omit<UIInteraction, "id" | "session" | "opened_at">, extra: Partial<Parameters<typeof InteractionCard>[0]> = {}) {
  const got: UIResponse[] = [];
  const onRespond = vi.fn(async (r: UIResponse) => {
    got.push(r);
  });
  const view = render(<InteractionCard it={{ ...base, ...it }} onRespond={onRespond} keyboard {...extra} />);
  return { got, onRespond, view };
}

const flush = () => act(async () => {});

describe("InteractionCard", () => {
  it("approval：允许一次 / 本会话内允许 / 拒绝", async () => {
    const { got } = card({ kind: "approval", title: "t", approval: { tool: "shell", args: { cmd: "ls" }, session_rules: ["shell:ls"] } });
    expect(screen.getByText("需要确认：shell")).toBeTruthy();
    fireEvent.click(btn(/允许一次/));
    await flush();
    expect(got).toEqual([{ approve: true, scope: "once" }]);
  });

  it("approval：键盘 Enter 允许、a 本会话允许、Esc 拒绝", async () => {
    for (const [key, want] of [
      ["Enter", { approve: true, scope: "once" }],
      ["a", { approve: true, scope: "session" }],
      ["Escape", { approve: false }],
    ] as const) {
      const { got } = card({ kind: "approval", title: "t", approval: { tool: "shell", session_rules: ["shell:ls"] } });
      fireEvent.keyDown(window, { key });
      await flush();
      expect(got).toEqual([want]);
      cleanup();
    }
  });

  it("question：点选项、自由回答", async () => {
    const { got } = card({ kind: "question", title: "选哪个", question: { text: "选哪个", options: ["甲", "乙"] } });
    fireEvent.click(btn(/乙/));
    await flush();
    expect(got).toEqual([{ text: "乙" }]);
    cleanup();

    const b = card({ kind: "question", title: "q", question: { text: "q" } });
    expect((btn("提交") as HTMLButtonElement).disabled).toBe(true); // 空回答不能提交
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "  好的  " } });
    fireEvent.click(btn("提交"));
    await flush();
    expect(b.got).toEqual([{ text: "好的" }]);
  });

  it("choice 单选：点一下即提交选项 id；数字键选", async () => {
    const opts = [
      { id: "a", label: "苹果" },
      { id: "b", label: "香蕉" },
    ];
    const { got } = card({ kind: "choice", title: "水果", choice: { options: opts } });
    fireEvent.click(screen.getByRole("radio", { name: /香蕉/ }));
    await flush();
    expect(got).toEqual([{ value: "b" }]);
    cleanup();

    const k = card({ kind: "choice", title: "水果", choice: { options: opts } });
    fireEvent.keyDown(window, { key: "1" });
    await flush();
    expect(k.got).toEqual([{ value: "a" }]);
  });

  it("choice 多选：受 min / max 约束", async () => {
    const opts = ["a", "b", "c"].map((id) => ({ id, label: id.toUpperCase() }));
    const { got } = card({ kind: "choice", title: "多选", choice: { options: opts, multiple: true, min: 2, max: 2 } });
    const ok = () => btn(/确定/) as HTMLButtonElement;
    expect(ok().disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: /A/ }));
    expect(ok().disabled).toBe(true); // 少于 min
    fireEvent.click(screen.getByRole("checkbox", { name: /C/ }));
    expect(ok().disabled).toBe(false);
    fireEvent.click(screen.getByRole("checkbox", { name: /B/ }));
    expect(ok().disabled).toBe(true); // 多于 max
    fireEvent.click(screen.getByRole("checkbox", { name: /B/ }));
    fireEvent.click(ok());
    await flush();
    expect(got).toEqual([{ value: ["a", "c"] }]);
  });

  it("form：提交值含 schema 默认值；必填没填不能提交", async () => {
    const schema = {
      type: "object",
      required: ["name"],
      properties: { name: { type: "string", title: "名字" }, notify: { type: "boolean", default: true } },
    };
    const { got } = card({ kind: "form", title: "填表", schema, form: { submit: "保存" } });
    expect((btn("保存") as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/还没填：名字/)).toBeTruthy();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "张三" } });
    fireEvent.click(btn("保存"));
    await flush();
    expect(got).toEqual([{ value: { name: "张三", notify: true } }]);
  });

  it("confirm：自定义按钮文字，Esc 取消", async () => {
    const { got } = card({ kind: "confirm", title: "删除？", confirm: { danger: true, confirm: "删除", cancel: "算了" } });
    expect(btn(/删除/)).toBeTruthy();
    expect(btn(/算了/)).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });
    await flush();
    expect(got).toEqual([{ approve: false }]);
  });

  it("焦点在输入框里时数字键不被截走", async () => {
    const { got } = card({ kind: "question", title: "q", question: { text: "q", options: ["甲"] } });
    const box = screen.getByRole("textbox");
    fireEvent.keyDown(box, { key: "1" });
    await flush();
    expect(got).toEqual([]);
  });

  it("回答失败时显示错误并恢复可操作", async () => {
    const onRespond = vi.fn(async () => {
      throw new Error("网络断了");
    });
    render(<InteractionCard it={{ ...base, kind: "confirm", title: "继续？" }} onRespond={onRespond} />);
    fireEvent.click(btn(/继续/));
    await flush();
    expect(screen.getByText("网络断了")).toBeTruthy();
    expect((btn(/继续/) as HTMLButtonElement).disabled).toBe(false);
  });

  it("已结束：只读，显示结果，不再响应键盘", async () => {
    const { got } = card({ kind: "confirm", title: "继续？" }, { done: { outcome: "expired" } });
    expect(screen.getByText("已过期")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /继续/ })).toBeNull();
    fireEvent.keyDown(window, { key: "Enter" });
    await flush();
    expect(got).toEqual([]);
  });

  it("不认识的 kind：通用卡，可自由回答", async () => {
    const { got } = card({ kind: "x-custom", title: "插件问" });
    expect(screen.getByText(/不认识交互种类 "x-custom"/)).toBeTruthy();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "随便" } });
    fireEvent.click(btn("提交"));
    await flush();
    expect(got).toEqual([{ text: "随便" }]);
  });
});
