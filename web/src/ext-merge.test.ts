// CommandMap 可声明合并（docs/61 3.3）：产品用 declare module 加自己的命令，call 直接认（tsc 校验类型，vitest 校验运行）。
import { describe, expect, it } from "vitest";
import { connect, type Transport } from "./transport";

declare module "./commands" {
  interface CommandMap {
    "x.demo.echo": [{ text: string }, { text: string }];
  }
}

describe("CommandMap 声明合并", () => {
  it("合并进来的扩展命令可以类型化调用", async () => {
    const seen: string[] = [];
    const t: Transport = {
      call: async (cmd, args) => {
        seen.push(cmd);
        return { text: (args as { text: string }).text + "!" };
      },
      start: (_emit, state) => state("open"),
      close: () => {},
    };
    const c = connect(t);
    const r = await c.call("x.demo.echo", { text: "hi" });
    const s: string = r.text; // 结果类型来自合并的声明
    expect(s).toBe("hi!");
    expect(seen).toEqual(["x.demo.echo"]);
    c.close();
  });
});
