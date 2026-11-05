import { describe, expect, it } from "vitest";
import { createPackHost, defineRegistry, type Contribution } from "./registry";

interface Item extends Contribution {
  label: string;
}

describe("registry", () => {
  it("同 id 低 priority 替换内置，按 order 排序，撤销后恢复", () => {
    const r = defineRegistry<Item>("t");
    r.add({ id: "a", label: "内置 a", order: 2 });
    r.add({ id: "b", label: "b", order: 1 });
    const undo = r.add({ id: "a", label: "插件 a", order: 2, priority: 10 });
    expect(r.get().map((x) => x.label)).toEqual(["b", "插件 a"]);
    undo();
    expect(r.get().map((x) => x.label)).toEqual(["b", "内置 a"]);
  });
});

describe("pack host", () => {
  it("requires 不满足时停用并给原因，满足后激活，撤销时贡献一起消失", () => {
    const r = defineRegistry<Item>("t");
    const host = createPackHost();
    host.register({
      id: "perm",
      requires: { capability: "modes" },
      setup(ctx) {
        ctx.add(r, { id: "mode", label: "档位" });
      },
    });
    expect(host.get()[0].status.state).toBe("pending");
    host.setEnvironment({ ready: true, capabilities: [], plugins: null });
    const st = host.get()[0].status;
    expect(st.state).toBe("disabled");
    expect(st.state === "disabled" && st.reason).toContain("modes");
    expect(r.get()).toHaveLength(0);

    host.setEnvironment({ ready: true, capabilities: ["modes"], plugins: null });
    expect(host.get()[0].status.state).toBe("active");
    expect(r.get().map((x) => x.pack)).toEqual(["perm"]);
    expect(host.get()[0].contributes).toEqual(["t:mode"]);

    host.setDisabled(new Set(["perm"]));
    expect(r.get()).toHaveLength(0);
  });

  it("setup 抛错的包标 failed，已加的贡献撤销，其他包不受影响", () => {
    const r = defineRegistry<Item>("t");
    const host = createPackHost();
    host.register({
      id: "bad",
      setup(ctx) {
        ctx.add(r, { id: "x", label: "x" });
        throw new Error("坏了");
      },
    });
    host.register({ id: "good", setup: (ctx) => ctx.add(r, { id: "y", label: "y" }) });
    const [bad, good] = host.get();
    expect(bad.status).toEqual({ state: "failed", error: "坏了" });
    expect(good.status.state).toBe("active");
    expect(r.get().map((x) => x.id)).toEqual(["y"]);
  });

  it("后端插件未启用时停用", () => {
    const host = createPackHost();
    host.register({ id: "mem", requires: { plugin: "memory" }, setup() {} });
    host.setEnvironment({ ready: true, capabilities: [], plugins: new Set(["shell"]) });
    expect(host.get()[0].status.state).toBe("disabled");
    host.setEnvironment({ ready: true, capabilities: [], plugins: new Set(["memory"]) });
    expect(host.get()[0].status.state).toBe("active");
  });
});
