// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { DeclarativeView, type DeclNode } from "./declarative";

afterEach(cleanup);

describe("DeclarativeView", () => {
  it("按节点渲染，未知节点显示占位而不是整块挂掉", () => {
    const node: DeclNode[] = [
      { type: "text", text: "你好", tone: "muted" },
      { type: "badge", text: "新" },
      { type: "table", columns: ["名", "值"], rows: [["a", 1]] },
      { type: "bogus" } as unknown as DeclNode,
      null as unknown as DeclNode,
    ];
    const { container } = render(<DeclarativeView node={node} />);
    expect(screen.getByText("你好")).toBeTruthy();
    expect(screen.getByText("新")).toBeTruthy();
    expect([...container.querySelectorAll("td")].map((x) => x.textContent)).toEqual(["a", "1"]);
    expect(screen.getByText(/不支持的内容：bogus/)).toBeTruthy();
    expect(screen.getByText(/不支持的内容：\?/)).toBeTruthy();
  });

  it("嵌套 stack / row / list", () => {
    render(
      <DeclarativeView
        node={{ type: "stack", children: [{ type: "row", children: [{ type: "list", items: [{ type: "text", text: "深处" }] }] }] }}
      />,
    );
    expect(screen.getByText("深处").closest("li")).toBeTruthy();
  });

  it("没给 onAction 时按钮不可用", () => {
    render(<DeclarativeView node={{ type: "button", label: "执行", action: { kind: "insert", text: "x" } }} />);
    expect((screen.getByRole("button", { name: "执行" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("按钮与链接把声明的动作交给宿主", () => {
    const onAction = vi.fn();
    render(
      <DeclarativeView
        onAction={onAction}
        node={[
          { type: "button", label: "调命令", action: { kind: "call", cmd: "todo.list", args: { all: true } } },
          { type: "link", label: "开面板", action: { kind: "openPanel", panel: "p1" } },
        ]}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "调命令" }));
    fireEvent.click(screen.getByRole("button", { name: "开面板" }));
    expect(onAction.mock.calls).toEqual([[{ kind: "call", cmd: "todo.list", args: { all: true } }], [{ kind: "openPanel", panel: "p1" }]]);
  });

  it("表单：带初值与填写的值提交，提交中不可重复点", async () => {
    let release!: () => void;
    const onAction = vi.fn(() => new Promise<void>((r) => (release = r)));
    render(
      <DeclarativeView
        onAction={onAction}
        node={{
          type: "form",
          schema: { type: "object", properties: { title: { type: "string" }, n: { type: "integer" } } },
          value: { n: 3 },
          submit: "保存",
          action: { kind: "call", cmd: "x.save" },
        }}
      />,
    );
    fireEvent.change(screen.getAllByRole("textbox")[0]!, { target: { value: "标题" } });
    const save = screen.getByRole("button", { name: "保存" }) as HTMLButtonElement;
    fireEvent.click(save);
    expect(onAction).toHaveBeenCalledWith({ kind: "call", cmd: "x.save" }, { title: "标题", n: 3 });
    await act(async () => {});
    expect(save.disabled).toBe(true);
    await act(async () => release());
    expect(save.disabled).toBe(false);
  });
});
