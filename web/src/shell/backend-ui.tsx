// 后端插件包的界面贡献（[[ui]] 段，计划/05 F10）：packages.list 里带 ui 的包各变成一个贡献包（id "pkg:<名>"，
// 设置 › 插件里可单独关），每条 ui 是一个插槽项。视图是声明式的（DeclNode），宿主不执行包里的任何代码。
// 有 data 时先调那条只读命令，用结果替换视图里的 {{路径}} 占位；{{session}} 是插槽所在的会话。

import { useEffect, useState } from "react";
import type { Client, PackageInfo, UISlot } from "../client";
import { DeclarativeView, type DeclNode } from "../ui";
import type { ContributionPack } from "./registry";
import type { Registries } from "./contributions";
import { useShell } from "./context";

/** 替换字符串里的 {{路径}}：路径按点分取值（数组下标用数字），取不到留空。整串就是一个占位时保留原类型。 */
export function fill(v: unknown, data: Record<string, unknown>): unknown {
  if (typeof v === "string") {
    const whole = /^\{\{\s*([\w.]+)\s*\}\}$/.exec(v);
    if (whole) {
      const got = lookup(data, whole[1]);
      return got === undefined ? "" : got;
    }
    return v.replace(/\{\{\s*([\w.]+)\s*\}\}/g, (_, p: string) => {
      const got = lookup(data, p);
      return got === undefined || got === null ? "" : typeof got === "object" ? JSON.stringify(got) : String(got);
    });
  }
  if (Array.isArray(v)) return v.map((x) => fill(x, data));
  if (v && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, fill(x, data)]));
  return v;
}

function lookup(data: unknown, path: string): unknown {
  let cur: unknown = data;
  for (const k of path.split(".")) {
    if (cur === null || cur === undefined) return undefined;
    cur = (cur as Record<string, unknown>)[k];
  }
  return cur;
}

/** 有数据来源的视图：插槽所在会话变了、或每 30 秒，重取一次。 */
function DataView({ client, ui, session, onAction }: { client: Client; ui: UISlot; session?: string; onAction: Parameters<typeof DeclarativeView>[0]["onAction"] }) {
  const [state, setState] = useState<{ data?: Record<string, unknown>; error?: string }>({});
  useEffect(() => {
    if (!ui.data) return;
    let live = true;
    const load = () => {
      const args = fill(ui.data!.args ?? {}, { session: session ?? "" });
      client
        .call(ui.data!.cmd as never, args as never)
        .then((r) => live && setState({ data: r as Record<string, unknown> }))
        .catch((e) => live && setState({ error: e instanceof Error ? e.message : String(e) }));
    };
    load();
    const t = setInterval(load, 30_000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [client, ui, session]);
  if (state.error) return <DeclarativeView node={{ type: "text", text: `${ui.title || ui.id}：${state.error}`, tone: "danger", size: "sm" }} />;
  if (!state.data) return null;
  return <DeclarativeView node={fill(ui.view, { ...state.data, session: session ?? "" }) as DeclNode} onAction={onAction} />;
}

function UISlotView({ ui, props }: { ui: UISlot; props: Record<string, unknown> }) {
  const sh = useShell();
  const session = typeof props.session === "string" ? props.session : undefined;
  if (ui.data) return <DataView client={sh.client} ui={ui} session={session} onAction={sh.runDecl} />;
  return <DeclarativeView node={fill(ui.view, { session: session ?? "" }) as DeclNode} onAction={sh.runDecl} />;
}

/** 一个后端插件包 → 贡献包。requires：全部条目要求同一个插件时整包声明（插件没开整包停用、设置里看得到原因）；
 * 要求不同时按条过滤（plugins 为 null = 不知道哪些开着，不过滤）。 */
export function backendPack(reg: Registries, pkg: PackageInfo, plugins: Set<string> | null): ContributionPack {
  const all = pkg.ui ?? [];
  const reqs = [...new Set(all.map((u) => u.requires ?? ""))];
  const ui = reqs.length > 1 && plugins ? all.filter((u) => !u.requires || plugins.has(u.requires)) : all;
  return {
    id: `pkg:${pkg.name}`,
    title: `${pkg.name}（插件包 · ${pkg.scope === "project" ? "项目" : "用户"}）`,
    requires: reqs.length === 1 && reqs[0] ? { plugin: reqs[0] } : undefined,
    setup(ctx) {
      for (const u of ui) {
        ctx.add(reg.slots, {
          id: u.id,
          at: u.slot,
          order: u.order,
          render: (props) => <UISlotView ui={u} props={props} />,
        });
      }
    },
  };
}
