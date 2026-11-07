// 内置贡献包 core：工作台自己的命令（带缺省快捷键）、区域、布局预设、消息操作。
// 与插件贡献走同一组注册表：嵌入方用同 id、更小的 priority 替换，或 createShell({ disable: ["core.xxx"] }) 关掉。

import { MessageSquareQuote, Copy, CornerUpLeft, Cpu, FileText, GitBranch, MessagesSquare, Paperclip, Pencil, Pin, PinOff, Quote, RotateCcw, Trash2 } from "lucide-react";
import { Button, Menu, copyText, type MenuGroupSpec } from "../../ui";
import { useEffect, useState } from "react";
import type { ModelsListResult } from "../../client";
import { useSession } from "../../client/react";
import { useShell } from "../context";
import { listed } from "../../client";
import type { ContributionPack } from "../registry";
import type { Registries } from "../contributions";
import { Sidebar } from "../regions/sidebar";
import { MainArea } from "../regions/conversation";

/** 会话管理（session.update / session.delete）：命令与左栏行菜单共用。 */
async function renameSession(x: { client: import("../../client").Client; shell: import("../contributions").ShellApi }, id: string) {
  const cur = x.client.sessions.find(id)?.title ?? "";
  const v = await x.shell.prompt({ title: "重命名会话", label: "留空则显示会话 id", initial: cur });
  if (v === null) return;
  const snap = await x.client.call("session.update", { session: id, title: v });
  x.client.sessions.upsert({ ...(x.client.sessions.find(id) ?? { id, preview: "", origin: snap.origin, run_state: snap.run_state, updated_at: Date.now() }), title: snap.title, pinned: snap.pinned || undefined });
}

async function pinSession(x: { client: import("../../client").Client }, id: string, pinned: boolean) {
  const snap = await x.client.call("session.update", { session: id, pinned });
  const row = x.client.sessions.find(id);
  if (row) x.client.sessions.upsert({ ...row, title: snap.title, pinned: snap.pinned || undefined });
}

async function deleteSession(x: { client: import("../../client").Client; shell: import("../contributions").ShellApi }, id: string) {
  const name = x.client.sessions.find(id)?.title || id;
  const ok = await x.shell.confirm({
    title: "删除会话",
    body: `删除「${name}」及它派生的子会话，记录全部清除、不能恢复。分叉出去的会话不受影响。`,
    confirm: "删除",
    danger: true,
  });
  if (!ok) return;
  await x.client.call("session.delete", { session: id });
  x.client.sessions.remove(id);
  if (x.shell.current() === id || x.shell.current()?.startsWith(id + "/")) {
    x.shell.openPage(null);
    const next = listed(x.client.sessions.get())[0];
    if (next) x.shell.openSession(next.id);
    else x.shell.closeSession();
  }
  x.shell.toast("已删除", "success");
}

/** 可选模型（models.list）：整个页面共用一份，第一次用到时取。 */
let modelsCache: Promise<ModelsListResult> | null = null;
function loadModels(client: import("../../client").Client) {
  modelsCache ??= client.call("models.list", {}).catch((e) => {
    modelsCache = null;
    throw e;
  });
  return modelsCache;
}

/** 输入框工具栏的模型选择（composer.toolbar.left）：别名一组、模型按提供方分组，凭证不可用的停用并给原因。 */
function ModelPicker({ session }: { session: string }) {
  const sh = useShell();
  const [st] = useSession(session);
  const [list, setList] = useState<ModelsListResult | null>(null);
  const can = sh.client.can("models");
  useEffect(() => {
    if (!can) return;
    let live = true;
    loadModels(sh.client).then((r) => live && setList(r), () => {});
    return () => {
      live = false;
    };
  }, [sh.client, can]);
  if (!can || !list || !list.models.length) return null;
  const cur = st.model || list.default;
  const pick = async (id: string) => {
    try {
      const r = await sh.client.call("session.settings", { session, model: id });
      if (!r.committed) sh.api.toast(`回合结束后换成 ${id}`);
    } catch (e) {
      sh.api.toast(e instanceof Error ? e.message : String(e), "danger");
    }
  };
  const groups = (): MenuGroupSpec[] => {
    const byGroup = new Map<string, MenuGroupSpec>();
    for (const m of list.models) {
      const g = m.alias ? "别名" : m.provider;
      if (!byGroup.has(g)) byGroup.set(g, { id: g, label: g, items: [] });
      byGroup.get(g)!.items.push({
        id: m.id,
        label: m.alias ? m.id : m.name || m.id,
        description: [m.alias && m.name ? m.name : "", m.context_window ? `${Math.round(m.context_window / 1000)}k` : "", m.images ? "图片" : "", m.reasoning ? "推理" : ""]
          .filter(Boolean)
          .join(" · ") || undefined,
        checked: m.id === cur,
        disabled: !m.available,
        reason: m.reason,
        run: () => void pick(m.id),
      });
    }
    return [...byGroup.values()];
  };
  const short = cur.includes("/") ? cur.slice(cur.indexOf("/") + 1) : cur;
  return (
    <Menu
      placement="top-start"
      groups={groups}
      trigger={({ ref, toggle }) => (
        <Button ref={ref} size="sm" variant="ghost" onClick={toggle} aria-label={`模型：${cur}，点击切换`} title={cur}>
          <Cpu size={12} /> {short}
        </Button>
      )}
    />
  );
}

const sh0 = (p: Record<string, unknown>) => typeof p.session === "string" && p.session !== "";

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
      ctx.add(reg.presets, { id: "chat", order: 1, title: "聊天", sidebar: "sidebar", main: "main", panels: "drawer" });
      ctx.add(reg.presets, { id: "embed", order: 2, title: "嵌入（单会话）", sidebar: null, main: "main", panels: "float" });

      // 输入框工具栏：切换本会话的模型（后端有模型目录时）。
      ctx.add(reg.slots, {
        id: "composer.model",
        at: "composer.toolbar.left",
        order: 10,
        render: (p) => (sh0(p) ? <ModelPicker session={p.session as string} /> : null),
      });

      // ---- 命令 ----
      const cmd = (c: Parameters<typeof reg.commands.add>[0]) => ctx.add(reg.commands, c);
      cmd({ id: "session.new", title: "新建会话", section: "session", keybind: "Mod+N", global: true, run: (x) => void x.shell.newSession() });
      cmd({ id: "shell.palette", title: "命令面板", section: "general", keybind: "Mod+K", global: true, hidden: true, run: (x) => x.shell.openPalette("commands") });
      cmd({ id: "shell.searchSessions", title: "搜索会话", section: "navigation", keybind: "Mod+P", global: true, run: (x) => x.shell.openPalette("sessions") });
      cmd({ id: "shell.settings", title: "打开设置", section: "general", keybind: "Mod+,", global: true, run: (x) => x.shell.openSettings() });
      cmd({ id: "shell.focusComposer", title: "聚焦输入框", section: "composer", keybind: "Mod+L", global: true, run: (x) => x.shell.focusComposer() });
      const top = (x: { session: string | null }) => (x.session ? x.session.split("/")[0] : null);
      cmd({
        id: "session.rename",
        title: "重命名会话",
        section: "session",
        slash: "rename",
        when: (x) => (x.session ? true : "没有打开的会话"),
        run: async (x) => {
          if (x.session) await renameSession(x, x.session);
        },
      });
      cmd({
        id: "session.togglePin",
        title: "置顶 / 取消置顶会话",
        section: "session",
        when: (x) => (!x.session ? "没有打开的会话" : x.session.includes("/") ? "子会话不能置顶" : true),
        run: async (x) => {
          const id = top(x);
          if (id) await pinSession(x, id, !x.client.sessions.find(id)?.pinned);
        },
      });
      cmd({
        id: "session.delete",
        title: "删除会话",
        section: "session",
        when: (x) => (x.session ? true : "没有打开的会话"),
        run: async (x) => {
          const id = top(x);
          if (id) await deleteSession(x, id);
        },
      });
      // 左栏会话行菜单（session.row.menu）：作用于那一行，不是当前会话。
      ctx.add(reg.menus, {
        id: "row.rename",
        menu: "session.row.menu",
        title: "重命名",
        icon: <Pencil size={14} />,
        group: "manage",
        run: (x) => void (x.row && renameSession(x, x.row.id)),
      });
      ctx.add(reg.menus, {
        id: "row.pin",
        menu: "session.row.menu",
        title: "置顶",
        icon: <Pin size={14} />,
        group: "manage",
        when: (x) => !!x.row && !x.row.pinned && !x.row.parent,
        run: (x) => void (x.row && pinSession(x, x.row.id, true)),
      });
      ctx.add(reg.menus, {
        id: "row.unpin",
        menu: "session.row.menu",
        title: "取消置顶",
        icon: <PinOff size={14} />,
        group: "manage",
        when: (x) => !!x.row?.pinned,
        run: (x) => void (x.row && pinSession(x, x.row.id, false)),
      });
      ctx.add(reg.menus, {
        id: "row.delete",
        menu: "session.row.menu",
        title: "删除",
        icon: <Trash2 size={14} />,
        group: "danger",
        danger: true,
        when: (x) => (!x.row ? false : x.row.parent && x.row.id.includes("/") ? "子会话随主会话一起删除" : true),
        run: (x) => void (x.row && deleteSession(x, x.row.id)),
      });
      ctx.add(reg.menus, { id: "header.rename", menu: "conversation.header.menu", title: "重命名", icon: <Pencil size={14} />, command: "session.rename", group: "manage" });
      ctx.add(reg.menus, { id: "header.pin", menu: "conversation.header.menu", title: "置顶 / 取消置顶", icon: <Pin size={14} />, command: "session.togglePin", group: "manage" });
      ctx.add(reg.menus, { id: "header.delete", menu: "conversation.header.menu", title: "删除会话", icon: <Trash2 size={14} />, command: "session.delete", group: "danger", danger: true });
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

      // ---- 输入框："+" 菜单的添加项、"@" 引用会话 ----
      ctx.add(reg.composerActions, {
        id: "add.file",
        group: "add",
        order: 0,
        title: "添加文件或图片",
        icon: <Paperclip size={14} />,
        keywords: ["附件", "图片", "file", "image"],
        run: (x) => {
          const el = document.createElement("input");
          el.type = "file";
          el.multiple = true;
          el.onchange = () => {
            const f = [...(el.files ?? [])];
            if (f.length) x.attach(f);
          };
          el.click();
        },
      });
      // 工作区文件（workspace.files）：插入 @path，模型用 read_file 读。路径含空格时加引号。
      ctx.add(reg.mentions, {
        id: "files",
        group: "文件",
        order: 0,
        when: (x) => (x.client.can("files") ? true : false),
        suggest: async (q, x) => {
          const r = await x.client.call("workspace.files", { query: q, limit: 20 });
          return r.files.map((f) => {
            const slash = f.path.lastIndexOf("/");
            return {
              id: "file:" + f.path,
              group: "文件",
              title: f.path.slice(slash + 1),
              description: slash > 0 ? f.path.slice(0, slash) : undefined,
              icon: <FileText size={14} />,
              insert: /\s/.test(f.path) ? `@"${f.path}"` : `@${f.path}`,
            };
          });
        },
      });
      ctx.add(reg.mentions, {
        id: "sessions",
        group: "会话",
        order: 10,
        suggest: (q, x) => {
          const needle = q.toLowerCase();
          return listed(x.client.sessions.get())
            .filter((r) => r.id !== x.session && (!needle || (r.title || r.id).toLowerCase().includes(needle) || r.id.includes(needle)))
            .slice(0, 8)
            .map((r) => ({
              id: "session:" + r.id,
              group: "会话",
              title: r.title || r.id,
              description: r.preview,
              icon: <MessagesSquare size={14} />,
              insert: `@session:${r.id}`,
            }));
        },
      });

      // ---- 消息操作（悬停条与 ⋯ 菜单）----
      const act = (a: Parameters<typeof reg.messageActions.add>[0]) => ctx.add(reg.messageActions, a);
      act({
        id: "msg.copy",
        appliesTo: ["user", "assistant", "selection"],
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
        appliesTo: ["user", "assistant", "selection"],
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
        id: "msg.askAbout",
        appliesTo: ["selection"],
        label: "就这段追问",
        icon: <MessageSquareQuote size={14} />,
        order: 12,
        run: (x) => {
          const q = (x.text ?? "").split("\n").map((l) => "> " + l).join("\n");
          x.shell.insert(q + "\n\n这段是什么意思？");
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
