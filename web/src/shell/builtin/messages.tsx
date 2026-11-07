// 内置贡献包 messages（前端方案第七节）：消息上的附加操作：朗读、反馈 👍 👎、导出（回合 / 整个会话，Markdown / JSON）。
// 都走 MessageAction / Command 注册表；嵌入方 disable: ["messages"] 整包关掉，或同 id 低 priority 替换。
// 反馈存后端（feedback.set / feedback.list，存在会话行上、随会话删除）；首版存在 localStorage 的旧反馈第一次用到时搬过去。

import { Download, FileJson, ThumbsDown, ThumbsUp, Volume2 } from "lucide-react";
import type { Client, Entry } from "../../client";
import type { ContributionPack } from "../registry";
import type { Registries } from "../contributions";

// ---- 反馈 ----

type Vote = "up" | "down";
const fbKey = (session: string) => `tenon.feedback.${session}`;

/** 首版存在本地的反馈：搬到后端后删掉本地这份（搬一次）。 */
async function migrateLocal(client: Client, session: string) {
  let old: Record<string, Vote> = {};
  try {
    old = JSON.parse(localStorage.getItem(fbKey(session)) ?? "{}") as Record<string, Vote>;
  } catch {
    return;
  }
  const ids = Object.keys(old);
  if (!ids.length) return;
  for (const entry of ids) await client.call("feedback.set", { session, entry, vote: old[entry] });
  try {
    localStorage.removeItem(fbKey(session));
  } catch {
    /* 存储不可用 */
  }
}

/** 一个会话的反馈：条目 id → 票。 */
export async function readFeedback(client: Client, session: string): Promise<Record<string, Vote>> {
  await migrateLocal(client, session);
  const r = await client.call("feedback.list", { session });
  return Object.fromEntries(r.items.map((f) => [f.entry, f.vote as Vote]));
}

/** 同一票再点一次是撤销。 */
async function vote(client: Client, session: string, entry: string, v: Vote): Promise<Vote | null> {
  const cur = (await readFeedback(client, session))[entry];
  const next = cur === v ? null : v;
  await client.call("feedback.set", { session, entry, vote: next ?? "" });
  return next;
}

// ---- 导出 ----

function text(parts: { kind: string; text?: string; media?: { mime: string } }[] | undefined): string {
  return (parts ?? []).map((p) => (p.kind === "text" ? (p.text ?? "") : p.media ? `[${p.media.mime}]` : "")).join("");
}

/** 条目 → Markdown：用户 / 助手 / 工具调用（参数折叠成代码块）/ 压缩点。 */
export function entriesToMarkdown(title: string, entries: Entry[]): string {
  const out: string[] = [`# ${title}`, ""];
  let turn = -1;
  for (const e of entries) {
    if (e.turn !== turn) {
      turn = e.turn;
      out.push(`## 第 ${turn} 回合`, "");
    }
    const it = e.item;
    switch (it.kind) {
      case "user":
        out.push("**用户**", "", text(it.content), "");
        break;
      case "assistant": {
        const t = text(it.content);
        if (t) out.push("**助手**", "", t, "");
        for (const c of it.tool_calls ?? []) out.push(`> 调用 \`${c.name}\``, "", "```json", JSON.stringify(c.args, null, 2), "```", "");
        break;
      }
      case "tool_result": {
        const t = text(it.output);
        out.push(`> 结果（${it.status ?? "ok"}）`, "", "```", t.length > 4000 ? t.slice(0, 4000) + "\n…（已截断）" : t, "```", "");
        break;
      }
      case "compaction":
        out.push(`> 已压缩到第 ${it.upto ?? 0} 条：${text(it.summary)}`, "");
        break;
    }
  }
  return out.join("\n");
}

/** 取整个会话的条目（分页拉到底）。 */
async function allEntries(client: Client, session: string): Promise<Entry[]> {
  const out: Entry[] = [];
  let from = 0;
  for (;;) {
    const r = await client.call("session.entries", { session, from, to: from + 500 });
    const es = r.entries ?? [];
    out.push(...es);
    if (es.length === 0 || from + 500 >= r.len) break;
    from += 500;
  }
  return out;
}

function download(name: string, mime: string, body: string) {
  const url = URL.createObjectURL(new Blob([body], { type: mime }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

const safe = (s: string) => s.replace(/[\\/:*?"<>|\s]+/g, "_").slice(0, 60) || "session";

// ---- 朗读 ----

function speak(t: string): boolean {
  if (typeof speechSynthesis === "undefined") return false;
  if (speechSynthesis.speaking) {
    speechSynthesis.cancel();
    return true;
  }
  const u = new SpeechSynthesisUtterance(t.replace(/```[\s\S]*?```/g, "（代码略）").replace(/[#>*_`]/g, ""));
  u.lang = /[一-鿿]/.test(t) ? "zh-CN" : "en-US";
  speechSynthesis.speak(u);
  return true;
}

export function messagesPack(reg: Registries): ContributionPack {
  return {
    id: "messages",
    title: "消息附加操作（朗读、反馈、导出）",
    builtin: true,
    setup(ctx) {
      const act = (a: Parameters<typeof reg.messageActions.add>[0]) => ctx.add(reg.messageActions, a);
      act({
        id: "msg.feedbackUp",
        appliesTo: ["assistant"],
        label: "有帮助",
        icon: <ThumbsUp size={14} />,
        placement: "hover",
        order: 5,
        when: (x) => (x.entryId ? true : false),
        run: async (x) => {
          if (!x.session || !x.entryId) return;
          const v = await vote(x.client, x.session, x.entryId, "up");
          x.shell.toast(v ? "已记下：有帮助" : "已取消反馈");
        },
      });
      act({
        id: "msg.feedbackDown",
        appliesTo: ["assistant"],
        label: "没帮助",
        icon: <ThumbsDown size={14} />,
        placement: "hover",
        order: 6,
        when: (x) => (x.entryId ? true : false),
        run: async (x) => {
          if (!x.session || !x.entryId) return;
          const v = await vote(x.client, x.session, x.entryId, "down");
          x.shell.toast(v ? "已记下：没帮助" : "已取消反馈");
        },
      });
      act({
        id: "msg.readAloud",
        appliesTo: ["assistant"],
        label: "朗读 / 停止朗读",
        icon: <Volume2 size={14} />,
        order: 13,
        when: () => (typeof speechSynthesis === "undefined" ? "浏览器不支持语音合成" : true),
        run: (x) => {
          if (!speak(x.text ?? "")) x.shell.toast("浏览器不支持语音合成", "warning");
        },
      });
      act({
        id: "turn.exportMd",
        appliesTo: ["turn"],
        label: "导出本回合（Markdown）",
        icon: <Download size={14} />,
        order: 30,
        run: async (x) => {
          if (!x.session || x.turn === undefined) return;
          const es = (await allEntries(x.client, x.session)).filter((e) => e.turn === x.turn);
          download(`${safe(x.session)}-turn${x.turn}.md`, "text/markdown", entriesToMarkdown(`${x.session} · 第 ${x.turn} 回合`, es));
        },
      });

      const cmd = (c: Parameters<typeof reg.commands.add>[0]) => ctx.add(reg.commands, c);
      const need = (x: { session: string | null }) => (x.session ? true : "没有打开的会话");
      cmd({
        id: "session.exportMd",
        title: "导出会话（Markdown）",
        section: "session",
        icon: <Download size={14} />,
        keywords: ["export", "下载"],
        when: need,
        run: async (x) => {
          if (!x.session) return;
          const snap = await x.client.call("session.get", { session: x.session });
          download(`${safe(snap.title || x.session)}.md`, "text/markdown", entriesToMarkdown(snap.title || x.session, await allEntries(x.client, x.session)));
        },
      });
      cmd({
        id: "session.exportJson",
        title: "导出会话（JSON）",
        section: "session",
        icon: <FileJson size={14} />,
        keywords: ["export", "下载", "raw"],
        when: need,
        run: async (x) => {
          if (!x.session) return;
          const snap = await x.client.call("session.get", { session: x.session });
          const body = { session: snap, entries: await allEntries(x.client, x.session), feedback: await readFeedback(x.client, x.session) };
          download(`${safe(snap.title || x.session)}.json`, "application/json", JSON.stringify(body, null, 2));
        },
      });
      // 对话头部 ⋯ 菜单里也放导出
      ctx.add(reg.menus, { id: "menu.exportMd", menu: "conversation.header.menu", title: "导出会话（Markdown）", icon: <Download size={14} />, command: "session.exportMd", group: "export" });
      ctx.add(reg.menus, { id: "menu.exportJson", menu: "conversation.header.menu", title: "导出会话（JSON）", icon: <FileJson size={14} />, command: "session.exportJson", group: "export" });
    },
  };
}
