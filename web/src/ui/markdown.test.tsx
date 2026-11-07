// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Markdown, MarkdownExtensions, MarkdownLinkHandler } from "./markdown";

afterEach(cleanup);

describe("Markdown", () => {
  it("行内：代码、粗体、斜体", () => {
    const { container } = render(<Markdown text={"有 `x` 和 **粗** 和 *斜*"} />);
    expect(container.querySelector("code")?.textContent).toBe("x");
    expect(container.querySelector("strong")?.textContent).toBe("粗");
    expect(container.querySelector("em")?.textContent).toBe("斜");
  });

  it("标题、分隔线、表格", () => {
    const { container } = render(<Markdown text={"# 一\n\n---\n\n| a | b |\n|---|---|\n| 1 | 2 |"} />);
    expect(container.querySelector("h1")?.textContent).toBe("一");
    expect(container.querySelector("hr")).toBeTruthy();
    expect([...container.querySelectorAll("td")].map((td) => td.textContent)).toEqual(["1", "2"]);
  });

  // 模型输出不可信：HTML 原样当文字显示，不生成元素。
  it("HTML 不被解析", () => {
    const { container } = render(<Markdown text={'<img src=x onerror="alert(1)"><script>alert(2)</script>'} />);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
    expect(container.textContent).toContain("<script>");
  });

  it("没有链接处理器时链接不可跳转（无 href）", () => {
    const { container } = render(<Markdown text={"[点我](javascript:alert(1))"} />);
    const a = container.querySelector("a")!;
    expect(a.textContent).toBe("点我");
    expect(a.getAttribute("href")).toBeNull();
  });

  it("有链接处理器时点击交给处理器，不走浏览器跳转", () => {
    const onLink = vi.fn();
    render(
      <MarkdownLinkHandler value={onLink}>
        <Markdown text={"见 [文档](https://example.com/a)"} />
      </MarkdownLinkHandler>,
    );
    const a = screen.getByText("文档");
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    fireEvent(a, ev);
    expect(onLink).toHaveBeenCalledWith("https://example.com/a");
    expect(ev.defaultPrevented).toBe(true);
  });

  it("代码块：扩展按语言接管渲染，返回 null 时退回普通代码块", () => {
    const render1 = vi.fn((code: string) => <div data-testid="ext">{code.toUpperCase()}</div>);
    const { container } = render(
      <MarkdownExtensions value={[{ id: "demo", langs: ["demo"], render: render1 }, { id: "skip", langs: ["skip"], render: () => null }]}>
        <Markdown text={"```demo\nhi\n```\n\n```skip\nraw\n```"} />
      </MarkdownExtensions>,
    );
    expect(screen.getByTestId("ext").textContent).toBe("HI");
    expect(container.textContent).toContain("raw");
  });

  it("流式中未闭合的代码块照常显示", () => {
    const { container } = render(<Markdown text={"```go\nfunc main() {"} streaming />);
    expect(container.textContent).toContain("func main() {");
  });
});
