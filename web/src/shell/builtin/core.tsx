// 内置贡献包 core：工作台自己的命令（带缺省快捷键）、区域、布局预设、消息操作。
// 与插件贡献走同一组注册表：嵌入方用同 id、更小的 priority 替换，或 createShell({ disable: ["core.xxx"] }) 关掉。

import { Copy, CornerUpLeft, GitBranch, Pencil, Quote, RotateCcw } from "lucide-react";
import { copyText } from "../../ui";
import type { ContributionPack } from "../registry";
import type { Registries } from "../contributions";
import { Sidebar } from "../regions/sidebar";
import { MainArea } from "../regions/conversation";

/** 会话里某回合的起点：rewind {to_turn} 用回合号（绝对目标，重发无害）。 */
async function rewindTo(cx: { client: import("../../client").Client; session: string | null }, turn: number) {
  if (!cx.session) return;
  await cx.client.call("session.rewind", { session: cx.session, to_turn: turn });
}

export function corePack(reg: Registries): ContributionPack {
  return {
    id: "core",
    title: "工作台核心",
    builtin: true,
    setup(ctx) {
      // ---- 区域与预设 ----
      ctx.add(reg.regions, { id: "sidebar", render: () => <Sidebar /> });
      ctx.add(reg.regions, { id: "main", render: () => <MainArea /> });
      ctx.add(reg.presets, { id: "workbench", order: 0, title: "工作台", sidebar: "sidebar", main: "main", panels: "dock" });

      // ---- 命令 ----
      const cmd = (c: Parameters<typeof reg.commands.add>[0]) => ctx.add(reg.commands, c);
      cmd({ id: "session.new", title: "新建会话", section: "session", keybind: "Mod+N", global: true, run: (x) => void x.shell.newSession() });
      cmd({ id: "shell.palette", title: "命令面板", section: "general", keybind: "Mod+K", global: true, hidden: true, run: (x) => x.shell.openPalette("commands") });
      cmd({ id: "shell.searchSessions", title: "搜索会话", section: "navigation", keybind: "Mod+P", global: true, run: (x) => x.shell.openPalette("sessions") });
      cmd({ id: "shell.settings", title: "打开设置", section: "general", keybind: "Mod+,", global: true, run: (x) => x.shell.openSettings() });
      cmd({ id: "shell.focusComposer", title: "聚焦输入框", section: "composer", keybind: "Mod+L", global: true, run: (x) => x.shell.focusComposer() });
      cmd({
        id: "session.cancel",
        title: "停止当前回合",
        section: "session",
        keybind: "Escape Escape",
        global: true,
        when: (x) => (x.session ? true : "没有打开的会话"),
        run: async (x) => {
          if (x.session) await x.client.call("session.cancel", { session: x.session });
        },
      });
      cmd({
        id: "session.fork",
        title: "分叉当前会话",
        section: "session",
        when: (x) => (x.session ? true : "没有打开的会话"),
        run: async (x) => {
          if (!x.session) return;
          const snap = await x.client.call("session.fork", { session: x.session });
          x.shell.openSession(snap.id);
          x.shell.toast(`已分叉到 ${snap.id}`, "success");
        },
      });
      cmd({
        id: "session.compact",
        title: "压缩上下文",
        section: "session",
        slash: "compact",
        when: (x) => (!x.session ? "没有打开的会话" : x.client.can("compact") ? true : "后端未装压缩插件"),
        run: async (x) => {
          if (!x.session) return;
          const r = await x.client.call("session.compact", { session: x.session });
          x.shell.toast(`已压缩到第 ${r.upto} 条`, "success");
        },
      });
      cmd({
        id: "session.maxSteps",
        title: "设置每回合最大步数",
        section: "session",
        slash: "steps",
        when: (x) => (x.session ? true : "没有打开的会话"),
        run: async (x) => {
          if (!x.session) return;
          const v = await x.shell.prompt({ title: "每回合最大步数", label: "0 = 不限" });
          if (v === null || v.trim() === "") return;
          const n = Math.max(0, Math.floor(Number(v)) || 0);
          await x.client.call("session.settings", { session: x.session, max_steps: n });
          x.shell.toast(`最大步数：${n || "不限"}`);
        },
      });

      // ---- 消息操作（悬停条与 ⋯ 菜单）----
      const act = (a: Parameters<typeof reg.messageActions.add>[0]) => ctx.add(reg.messageActions, a);
      act({
        id: "msg.copy",
        appliesTo: ["user", "assistant"],
        label: "复制",
        icon: <Copy size={14} />,
        placement: "hover",
        order: 0,
        run: async (x) => {
          if (await copyText(x.text ?? "")) x.shell.toast("已复制");
        },
      });
      act({
        id: "msg.edit",
        appliesTo: ["user"],
        label: "编辑后重发",
        icon: <Pencil size={14} />,
        placement: "hover",
        order: 1,
        run: async (x) => {
          if (x.turn === undefined) return;
          const ok = await x.shell.confirm({
            title: "编辑后重发",
            body: `会回退到第 ${x.turn} 回合之前（这一回合及之后的消息都会截掉），原文放回输入框。`,
            confirm: "回退并编辑",
            danger: true,
          });
          if (!ok) return;
          await rewindTo(x, x.turn);
          x.shell.insert(x.text ?? "");
          x.shell.focusComposer();
        },
      });
      act({
        id: "msg.retry",
        appliesTo: ["assistant"],
        label: "重试本回合",
        icon: <RotateCcw size={14} />,
        placement: "hover",
        order: 1,
        run: async (x) => {
          if (x.turn === undefined || !x.session) return;
          // 取本回合的用户输入：回退到本回合之前再原样发送。
          const r = await x.client.call("session.entries", { session: x.session, tail: 200 });
          const first = r.entries.find((e) => e.turn === x.turn && e.item.kind === "user");
          const text = (first?.item.content ?? []).map((p) => p.text ?? "").join("");
          if (!text) return x.shell.toast("找不到本回合的输入", "warning");
          await rewindTo(x, x.turn);
          await x.client.session(x.session).send(text);
        },
      });
      act({
        id: "msg.quote",
        appliesTo: ["user", "assistant"],
        label: "引用到输入框",
        icon: <Quote size={14} />,
        order: 10,
        run: (x) => {
          const q = (x.text ?? "").split("\n").map((l) => "> " + l).join("\n");
          x.shell.insert(q + "\n\n");
          x.shell.focusComposer();
        },
      });
      act({
        id: "msg.fork",
        appliesTo: ["user", "assistant"],
        label: "从这里分叉",
        icon: <GitBranch size={14} />,
        order: 11,
        run: async (x) => {
          if (!x.session || x.turn === undefined) return;
          // 分叉到"这一回合之后"：用户消息从本回合起点分（不含本回合），助手消息带上本回合。
          const r = await x.client.call("session.entries", { session: x.session, tail: 500 });
          const turnStart = (t: number) => r.entries.find((e) => e.turn === t && e.item.kind === "user")?.idx;
          const upto = x.target === "user" ? turnStart(x.turn) : turnStart(x.turn + 1);
          const snap = await x.client.call("session.fork", { session: x.session, upto });
          x.shell.openSession(snap.id);
          x.shell.toast(`已分叉到 ${snap.id}`, "success");
        },
      });
      act({
        id: "msg.rewind",
        appliesTo: ["user"],
        label: "回退到这里之前",
        icon: <CornerUpLeft size={14} />,
        danger: true,
        order: 20,
        run: async (x) => {
          if (x.turn === undefined) return;
          const ok = await x.shell.confirm({
            title: "回退",
            body: `截掉第 ${x.turn} 回合及之后的全部消息，不能撤销。`,
            confirm: "回退",
            danger: true,
          });
          if (ok) await rewindTo(x, x.turn);
        },
      });
    },
  };
}
