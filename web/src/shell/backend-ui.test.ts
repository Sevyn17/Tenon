import { describe, expect, it } from "vitest";
import { fill } from "./backend-ui";

describe("插件包视图的占位替换", () => {
  it("按路径取值；整串占位保留类型；取不到留空", () => {
    const data = { attempts: 3, usage: { input: 120 }, items: [{ title: "a" }], session: "s1" };
    expect(fill({ type: "text", text: "请求 {{attempts}} 次，输入 {{usage.input}}" }, data)).toEqual({ type: "text", text: "请求 3 次，输入 120" });
    expect(fill({ type: "progress", value: "{{attempts}}" }, data)).toEqual({ type: "progress", value: 3 });
    expect(fill(["{{items.0.title}}", "{{nope.x}}", "x{{nope}}y"], data)).toEqual(["a", "", "xy"]);
    expect(fill({ args: { session: "{{session}}" } }, data)).toEqual({ args: { session: "s1" } });
  });
});
