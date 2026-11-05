// 内置面板（前端方案第五、六节 ⑤）：待处理（全部会话未结束的交互）、工具活动（当前会话的工具调用）。
// 第 7 步补齐任务、改动、用量、子会话、工件查看。面板与插件面板走同一注册表，可用同 id 替换。

import { Activity, Inbox } from "lucide-react";
import type { Interaction } from "../../client";
import { useInteractions, useSession, useStore } from "../../client/react";
import { Dot, EmptyState } from "../../ui";
import type { ContributionPack } from "../registry";
import type { PanelCardProps, PanelProps } from "../contributions";
import type { ShellState } from "../context";
import { PanelCard } from "../regions/conversation";
import s from "./panels.module.css";

function what(it: Interaction): string {
  if (it.kind === "approval") return `确认 ${it.approval?.tool ?? it.title}`;
  if (it.kind === "question") return it.question?.text ?? it.title;
  return it.title;
}

function Pending({ sh }: { sh: ShellState }) {
  const v = useInteractions();
  const cur = useStore(sh.nav).session;
  if (!v.open.length) return <EmptyState icon={<Inbox size={20} />} title="没有等你处理的事" />;
  return (
    <ul className={s.list}>
      {v.open.map((it) => {
        const root = it.session.split("/")[0];
        const sub = it.session.slice(root.length + 1);
        return (
          <li key={it.id}>
            <button type="button" className={s.row} aria-current={root === cur ? "true" : undefined} onClick={() => sh.api.openSession(root)}>
              <Dot state="warning" />
              <span className={s.rowMain}>
                <span className={s.rowTitle}>{what(it)}</span>
                <span className={s.rowSub}>
                  {root}
                  {sub && ` › ${sub}`}
                </span>
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

function PendingCard({ open }: PanelCardProps) {
  const v = useInteractions();
  if (!v.open.length) return null;
  return (
    <PanelCard icon={<Inbox size={12} />} title="待处理" tone="warning" onOpen={open}>
      {v.open.length} 件事在等你：{what(v.open[0])}
    </PanelCard>
  );
}

function Tools({ session }: PanelProps) {
  const [st] = useSession(session!);
  // 已结束的调用来自条目里的 tool_calls + tool_result；进行中的来自 st.tools。
  const results = new Map<string, string>();
  for (const e of st.entries) if (e.item.kind === "tool_result" && e.item.call_id) results.set(e.item.call_id, e.item.status ?? "ok");
  const rows: { id: string; name: string; state: "running" | "success" | "danger" | "idle"; turn: number }[] = [];
  for (const e of st.entries)
    for (const c of e.item.tool_calls ?? []) {
      const r = results.get(c.id);
      rows.push({ id: c.id, name: c.name, turn: e.turn, state: r === undefined ? "idle" : r === "ok" ? "success" : "danger" });
    }
  for (const t of st.tools) if (!rows.some((r) => r.id === t.call_id)) rows.push({ id: t.call_id, name: t.name, turn: st.turn?.turn ?? 0, state: "running" });
  if (!rows.length) return <EmptyState icon={<Activity size={20} />} title="这个会话还没有调用工具" />;
  return (
    <ul className={s.list}>
      {rows.reverse().map((r) => (
        <li key={r.id} className={s.row}>
          <Dot state={r.state} />
          <span className={s.rowMain}>
            <span className={s.rowTitle}>{r.name}</span>
            <span className={s.rowSub}>第 {r.turn} 回合</span>
          </span>
        </li>
      ))}
    </ul>
  );
}

export function panelsPack(sh: ShellState): ContributionPack {
  return {
    id: "panels",
    title: "内置面板",
    builtin: true,
    setup(ctx) {
      ctx.add(sh.reg.panels, {
        id: "pending",
        order: 0,
        title: "待处理",
        icon: <Inbox size={16} />,
        keybind: "Mod+Shift+I",
        badge: (x) => x.client.interactions.get().open.length || undefined,
        card: PendingCard,
        render: () => <Pending sh={sh} />,
      });
      ctx.add(sh.reg.panels, {
        id: "tools",
        order: 10,
        title: "工具活动",
        icon: <Activity size={16} />,
        keybind: "Mod+Shift+E",
        needsSession: true,
        render: (p) => <Tools {...p} />,
      });
    },
  };
}
