// 内置面板之二（前端方案第五、六节 ⑤）：任务、改动、上下文与用量、子会话、工件查看。
// 数据都从会话日志里的工具调用与协议命令合成，不另开事件订阅。依赖某个后端插件的面板放进声明了 requires 的包：
// 插件没装配时整包不激活，面板、竖栏按钮、小卡片一起消失（例如关掉 todo，"任务"就没有了）。

import { useEffect, useMemo, useState } from "react";
import { CircleCheck, CircleDot, Circle, FileDiff, FileText, Gauge, GitFork, ListChecks } from "lucide-react";
import type { Entry, SessionInfo, Stats } from "../../client";
import { useSession, useSessions } from "../../client/react";
import { CodeBlock, DiffView, Dot, EmptyState, KeyValue, Progress, Skeleton, cx } from "../../ui";
import type { ContributionPack } from "../registry";
import type { PanelCardProps, PanelProps } from "../contributions";
import type { ShellState } from "../context";
import { PanelCard } from "../regions/conversation";
import s from "./panels.module.css";

function argsOf(a: unknown): Record<string, unknown> | null {
  if (typeof a === "string") {
    try {
      return JSON.parse(a) as Record<string, unknown>;
    } catch {
      return null;
    }
  }
  return a && typeof a === "object" ? (a as Record<string, unknown>) : null;
}

/** 某会话里全部工具调用（按日志顺序），带结果状态。 */
function callsOf(entries: Entry[]) {
  const res = new Map<string, string>();
  for (const e of entries) if (e.item.kind === "tool_result" && e.item.call_id) res.set(e.item.call_id, e.item.status ?? "ok");
  const out: { id: string; name: string; args: Record<string, unknown> | null; status?: string; turn: number }[] = [];
  for (const e of entries) for (const c of e.item.tool_calls ?? []) out.push({ id: c.id, name: c.name, args: argsOf(c.args), status: res.get(c.id), turn: e.turn });
  return out;
}

// ---- 任务（todo 插件的 todo_write：列表的事实是最后一次调用的参数）----

interface Todo {
  content: string;
  status: "pending" | "in_progress" | "completed";
}

function useTodos(session: string | null): Todo[] | null {
  const [st] = useSession(session ?? "");
  return useMemo(() => {
    if (!session) return null;
    for (const t of [...st.tools].reverse()) {
      if (t.name !== "todo_write") continue;
      const a = argsOf(t.args);
      if (Array.isArray(a?.todos)) return a.todos as Todo[];
    }
    const calls = callsOf(st.entries).filter((c) => c.name === "todo_write" && Array.isArray(c.args?.todos));
    const last = calls[calls.length - 1];
    return last ? (last.args!.todos as Todo[]) : null;
  }, [session, st.tools, st.entries]);
}

function Tasks({ session }: PanelProps) {
  const todos = useTodos(session);
  if (!todos?.length) return <EmptyState icon={<ListChecks size={20} />} title="还没有任务计划" >多步任务开始时，智能体会在这里列出计划并随进度勾选。</EmptyState>;
  const done = todos.filter((t) => t.status === "completed").length;
  return (
    <div className={s.pad}>
      <div className={s.progressHead}>
        <span>
          {done} / {todos.length} 完成
        </span>
      </div>
      <Progress value={done / todos.length} />
      <ul className={s.todos}>
        {todos.map((t) => (
          <li key={t.content} className={cx(s.todo, s[`todo_${t.status}`])}>
            {t.status === "completed" ? <CircleCheck size={14} /> : t.status === "in_progress" ? <CircleDot size={14} /> : <Circle size={14} />}
            <span>{t.content}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function TasksCard({ session, open }: PanelCardProps) {
  const todos = useTodos(session);
  if (!todos?.length) return null;
  const done = todos.filter((t) => t.status === "completed").length;
  if (done === todos.length) return null;
  const cur = todos.find((t) => t.status === "in_progress");
  return (
    <PanelCard icon={<ListChecks size={12} />} title={`任务 ${done}/${todos.length}`} onOpen={open}>
      {cur?.content ?? "等待开始"}
    </PanelCard>
  );
}

// ---- 改动（fs 插件的 write_file / edit）----

interface FileChange {
  path: string;
  ops: { id: string; kind: "write" | "edit"; diff: string; ok: boolean; turn: number }[];
}

function useChanges(session: string | null): FileChange[] {
  const [st] = useSession(session ?? "");
  return useMemo(() => {
    const files = new Map<string, FileChange>();
    for (const c of callsOf(st.entries)) {
      if ((c.name !== "write_file" && c.name !== "edit") || typeof c.args?.path !== "string") continue;
      const path = c.args.path;
      let diff = "";
      if (c.name === "write_file") {
        const lines = String(c.args.content ?? "").split("\n");
        diff = lines.slice(0, 400).map((l) => "+" + l).join("\n") + (lines.length > 400 ? `\n@@ 还有 ${lines.length - 400} 行 @@` : "");
      } else {
        const edits = Array.isArray(c.args.edits) ? (c.args.edits as { old_text?: string; new_text?: string }[]) : [];
        diff = edits
          .map((e) => ["@@ 修改 @@", ...String(e.old_text ?? "").split("\n").filter((l, i, a) => l || i < a.length - 1).map((l) => "-" + l), ...String(e.new_text ?? "").split("\n").map((l) => "+" + l)].join("\n"))
          .join("\n");
      }
      const f = files.get(path) ?? { path, ops: [] };
      f.ops.push({ id: c.id, kind: c.name === "write_file" ? "write" : "edit", diff, ok: c.status === "ok", turn: c.turn });
      files.set(path, f);
    }
    return [...files.values()].filter((f) => f.ops.some((o) => o.ok));
  }, [st.entries]);
}

function Changes({ session }: PanelProps) {
  const files = useChanges(session);
  const [sel, setSel] = useState<string | null>(null);
  if (!files.length) return <EmptyState icon={<FileDiff size={20} />} title="这个会话还没有改动文件" />;
  const cur = files.find((f) => f.path === sel) ?? files[files.length - 1];
  return (
    <div className={s.split}>
      <ul className={s.list}>
        {files.map((f) => (
          <li key={f.path}>
            <button type="button" className={s.row} aria-current={f.path === cur.path ? "true" : undefined} onClick={() => setSel(f.path)}>
              <FileText size={14} />
              <span className={s.rowMain}>
                <span className={s.rowTitle}>{f.path}</span>
                <span className={s.rowSub}>
                  {f.ops.filter((o) => o.ok).length} 次 · 第 {f.ops[f.ops.length - 1].turn} 回合
                </span>
              </span>
            </button>
          </li>
        ))}
      </ul>
      <div className={s.diffs}>
        {cur.ops
          .filter((o) => o.ok)
          .map((o) => (
            <div key={o.id}>
              <div className={s.diffHead}>
                {o.kind === "write" ? "写入" : "编辑"} · 第 {o.turn} 回合
              </div>
              <DiffView diff={o.diff} />
            </div>
          ))}
      </div>
    </div>
  );
}

function ChangesCard({ session, open }: PanelCardProps) {
  const files = useChanges(session);
  if (!files.length) return null;
  return (
    <PanelCard icon={<FileDiff size={12} />} title="改动" onOpen={open}>
      {files.length} 个文件：{files.map((f) => f.path.split("/").pop()).slice(0, 3).join("、")}
      {files.length > 3 ? " …" : ""}
    </PanelCard>
  );
}

// ---- 上下文与用量（session.stats + 每回合条目的 usage）----

function fmt(n: number) {
  return n >= 10000 ? `${(n / 1000).toFixed(1)}k` : String(n);
}

function Usage({ sh, session, params }: PanelProps & { sh: ShellState }) {
  const [st] = useSession(session!);
  const [stats, setStats] = useState<Stats | null>(null);
  const [err, setErr] = useState("");
  const focusTurn = (params as { turn?: number } | undefined)?.turn;
  useEffect(() => {
    let live = true;
    sh.client
      .call("session.stats", { session: session! })
      .then((r) => live && (setStats(r), setErr("")))
      .catch((e) => live && setErr(e instanceof Error ? e.message : String(e)));
    return () => {
      live = false;
    };
  }, [sh.client, session, st.lastEnd, st.entries.length]);

  const turns = useMemo(() => {
    const m = new Map<number, { input: number; output: number; cache: number; steps: number; model: string }>();
    for (const e of st.entries) {
      if (e.item.kind !== "assistant" || !e.item.usage) continue;
      const t = m.get(e.turn) ?? { input: 0, output: 0, cache: 0, steps: 0, model: "" };
      t.input += e.item.usage.input;
      t.output += e.item.usage.output;
      t.cache += e.item.usage.cache_read;
      t.steps++;
      t.model = e.item.model ?? t.model;
      m.set(e.turn, t);
    }
    return [...m].reverse();
  }, [st.entries]);

  useEffect(() => {
    if (focusTurn === undefined) return;
    requestAnimationFrame(() => document.querySelector(`[data-usage-turn="${focusTurn}"]`)?.scrollIntoView({ block: "nearest" }));
  }, [focusTurn, turns.length]);

  return (
    <div className={s.pad}>
      {err ? (
        <div className={s.err}>{err}</div>
      ) : !stats ? (
        <Skeleton h={96} />
      ) : (
        <KeyValue
          rows={[
            ["输入", `${fmt(stats.input)} tokens`],
            ["输出", `${fmt(stats.output)} tokens`],
            ["缓存命中", `${Math.round(stats.hit_rate * 100)}%（读 ${fmt(stats.cache_read)} / 写 ${fmt(stats.cache_write)}）`],
            ["模型请求", `${stats.attempts} 次${stats.errors ? `，失败 ${stats.errors}` : ""}`],
            ["工具调用", String(stats.tool_calls)],
            ["压缩", `${stats.compacts} 次`],
          ]}
        />
      )}
      <h4 className={s.subhead}>每回合</h4>
      {turns.length === 0 && <div className={s.muted}>还没有回合</div>}
      <table className={s.table}>
        <tbody>
          {turns.map(([turn, t]) => (
            <tr key={turn} data-usage-turn={turn} data-focus={turn === focusTurn || undefined}>
              <td>第 {turn} 回合</td>
              <td>{t.steps} 步</td>
              <td>
                {fmt(t.input)} → {fmt(t.output)}
              </td>
              <td className={s.muted}>{t.input ? Math.round((t.cache / t.input) * 100) : 0}%</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ---- 子会话（parent 为当前会话的会话：子智能体、工作组成员）----

function SubSessions({ sh, session }: PanelProps & { sh: ShellState }) {
  const top = useSessions(); // 列表变化（sessions.changed）时重查
  const [items, setItems] = useState<SessionInfo[] | null>(null);
  useEffect(() => {
    let live = true;
    sh.client
      .call("sessions.list", { all: true, limit: 200 })
      .then((r) => live && setItems((r.items ?? []).filter((x) => x.parent === session || x.id.startsWith(session + "/"))))
      .catch(() => live && setItems([]));
    return () => {
      live = false;
    };
  }, [sh.client, session, top.items]);
  if (!items) return <div className={s.pad}><Skeleton h={48} /></div>;
  if (!items.length) return <EmptyState icon={<GitFork size={20} />} title="没有子会话" >子智能体与工作组成员的会话会挂在这里。</EmptyState>;
  return (
    <ul className={s.list}>
      {items.map((x) => (
        <li key={x.id}>
          <button type="button" className={s.row} onClick={() => sh.api.openSession(x.id)}>
            <Dot state={x.run_state === "running" ? "running" : "idle"} />
            <span className={s.rowMain}>
              <span className={s.rowTitle}>{x.title || x.id.slice(session!.length + 1) || x.id}</span>
              <span className={s.rowSub}>{x.preview || x.origin}</span>
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

// ---- 工件查看（工具输出全文、媒体）：openPanel("artifact", { ref, session }) ----

function ArtifactView({ sh, session, params }: PanelProps & { sh: ShellState }) {
  const p = params as { ref?: string; session?: string } | undefined;
  const sid = p?.session ?? session;
  const [r, setR] = useState<{ mime: string; size: number; text?: string; base64?: string } | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    if (!p?.ref || !sid) return;
    let live = true;
    setR(null);
    setErr("");
    sh.client
      .call("artifact.get", { session: sid, ref: p.ref, length: 1 << 20 })
      .then((x) => live && setR(x))
      .catch((e) => live && setErr(e instanceof Error ? e.message : String(e)));
    return () => {
      live = false;
    };
  }, [sh.client, sid, p?.ref]);
  if (!p?.ref) return <EmptyState icon={<FileText size={20} />} title="没有打开的工件">在工具步骤展开后点"查看全文"打开。</EmptyState>;
  if (err) return <div className={cx(s.pad, s.err)}>{err}</div>;
  if (!r) return <div className={s.pad}><Skeleton h={120} /></div>;
  return (
    <div className={s.pad}>
      <div className={s.muted}>
        {p.ref} · {r.mime} · {fmt(r.size)} 字节
      </div>
      {r.base64 && r.mime.startsWith("image/") ? (
        <img className={s.artifactImg} src={`data:${r.mime};base64,${r.base64}`} alt={p.ref} />
      ) : r.text !== undefined ? (
        <CodeBlock code={r.text} />
      ) : (
        <div className={s.muted}>二进制内容，不能预览</div>
      )}
    </div>
  );
}

export function workPanelsPack(sh: ShellState): ContributionPack {
  return {
    id: "panels.work",
    title: "会话面板",
    builtin: true,
    setup(ctx) {
      ctx.add(sh.reg.panels, { id: "usage", order: 30, title: "上下文与用量", icon: <Gauge size={16} />, keybind: "Mod+Shift+U", needsSession: true, render: (p) => <Usage sh={sh} {...p} /> });
      ctx.add(sh.reg.panels, { id: "subs", order: 40, title: "子会话", icon: <GitFork size={16} />, keybind: "Mod+Shift+S", needsSession: true, render: (p) => <SubSessions sh={sh} {...p} /> });
      ctx.add(sh.reg.panels, { id: "artifact", order: 50, title: "工件", icon: <FileText size={16} />, render: (p) => <ArtifactView sh={sh} {...p} /> });
    },
  };
}

/** 依赖 todo 插件。 */
export function tasksPack(sh: ShellState): ContributionPack {
  return {
    id: "panels.tasks",
    title: "任务面板",
    builtin: true,
    requires: { plugin: "todo" },
    setup(ctx) {
      ctx.add(sh.reg.panels, { id: "tasks", order: 5, title: "任务", icon: <ListChecks size={16} />, keybind: "Mod+Shift+O", needsSession: true, card: TasksCard, render: (p) => <Tasks {...p} /> });
    },
  };
}

/** 依赖 fs 插件。 */
export function changesPack(sh: ShellState): ContributionPack {
  return {
    id: "panels.changes",
    title: "改动面板",
    builtin: true,
    requires: { plugin: "fs" },
    setup(ctx) {
      ctx.add(sh.reg.panels, { id: "changes", order: 20, title: "改动", icon: <FileDiff size={16} />, keybind: "Mod+Shift+D", needsSession: true, card: ChangesCard, render: (p) => <Changes {...p} /> });
    },
  };
}
