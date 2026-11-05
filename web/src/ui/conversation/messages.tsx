// 消息组件（前端方案第六之二、七节）：用户的话是气泡，助手的输出是文档；过程（思考、工具链）默认安静、可展开，
// 正在发生时有动静。组件只吃视图类型（types.ts），不依赖协议客户端；操作（复制、分叉……）由调用方以 UIAction 传入，
// shell 从 MessageAction 注册表生成。

import { useEffect, useState, type ReactNode } from "react";
import { Check, ChevronRight, CircleAlert, Clock, MoreHorizontal, Wrench, X } from "lucide-react";
import { IconButton, Spinner, Tooltip } from "../controls";
import { Menu } from "../overlay";
import { Markdown } from "../markdown";
import { CodeBlock } from "../blocks";
import { cx } from "../cx";
import type { UIAction, UIEntry, UIPart, UITool } from "./types";
import s from "./conversation.module.css";

/** 内容片段里的文本拼起来；非文本片段用占位符。 */
export function partsText(c: UIPart[] | undefined): string {
  if (!c) return "";
  return c.map((p) => (p.kind === "text" ? (p.text ?? "") : `[${p.media?.mime ?? p.kind}]`)).join("");
}

function fmtTime(ms: number): string {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

/** 秒数（进行中的过程每秒刷新）。 */
export function useElapsed(since: number | undefined, until?: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!since || until) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [since, until]);
  if (!since) return 0;
  return Math.max(0, Math.round(((until ?? now) - since) / 1000));
}

// ---- 操作条 ----

export function MessageActions({ actions, pinned }: { actions: UIAction[]; pinned?: boolean }) {
  const hover = actions.filter((a) => a.placement === "hover");
  const menu = actions.filter((a) => a.placement === "menu");
  if (!hover.length && !menu.length) return null;
  return (
    <div className={cx(s.actions, pinned && s.actionsPinned)}>
      {hover.map((a) => (
        <Tooltip key={a.id} label={a.disabled && a.reason ? a.reason : a.label} keybind={a.keybind}>
          <IconButton label={a.label} size="sm" disabled={a.disabled} onClick={a.run}>
            {a.icon ?? <span>{a.label}</span>}
          </IconButton>
        </Tooltip>
      ))}
      {menu.length > 0 && (
        <Menu
          placement="bottom-end"
          trigger={({ ref, toggle }) => (
            <IconButton ref={ref} label="更多" size="sm" onClick={toggle}>
              <MoreHorizontal size={14} />
            </IconButton>
          )}
          groups={[
            {
              id: "m",
              items: menu.map((a) => ({
                id: a.id,
                label: a.label,
                icon: a.icon,
                keybind: a.keybind,
                danger: a.danger,
                disabled: a.disabled,
                reason: a.reason,
                run: a.run,
              })),
            },
          ]}
        />
      )}
    </div>
  );
}

// ---- 用户消息 ----

const CLAMP = 600; // 超过这么多字符折叠

export function UserMessage(p: { entry: UIEntry; actions?: UIAction[]; before?: ReactNode; after?: ReactNode }) {
  const text = partsText(p.entry.item.content);
  const media = (p.entry.item.content ?? []).filter((x) => x.kind === "media");
  const [open, setOpen] = useState(false);
  const long = text.length > CLAMP;
  return (
    <div className={cx(s.msg, s.user)} data-entry={p.entry.id}>
      {p.before}
      {media.length > 0 && (
        <div className={s.chips}>
          {media.map((m, i) => (
            <span key={i} className={s.chip}>
              {m.media?.mime ?? "附件"}
            </span>
          ))}
        </div>
      )}
      <div className={cx(s.bubble, long && !open && s.bubbleClamp)}>{text}</div>
      {long && (
        <button type="button" className={s.expandBtn} onClick={() => setOpen(!open)}>
          {open ? "收起" : "展开全文"}
        </button>
      )}
      <div className={s.time}>{fmtTime(p.entry.at)}</div>
      {p.actions && <MessageActions actions={p.actions} />}
      {p.after}
    </div>
  );
}

// ---- 思考 ----

export function Thinking(p: { text: string; live?: boolean; startedAt?: number; endedAt?: number; defaultOpen?: boolean; retry?: number }) {
  const [open, setOpen] = useState(!!p.defaultOpen);
  const secs = useElapsed(p.startedAt, p.live ? undefined : (p.endedAt ?? p.startedAt));
  const tail = p.text.split("\n").filter(Boolean).slice(-3).join("\n");
  if (p.live) {
    return (
      <div className={s.thinking}>
        <div className={s.processHead}>
          <span className={s.shimmer}>{p.retry && p.retry > 1 ? `重试中（第 ${p.retry} 次）` : "思考中"}</span>
          {secs > 0 && <span className={s.stepMeta}>{secs} 秒</span>}
        </div>
        {tail && <div className={s.thinkTail}>{tail}</div>}
      </div>
    );
  }
  if (!p.text) return null;
  return (
    <div className={s.thinking}>
      <button type="button" className={s.processHead} aria-expanded={open} onClick={() => setOpen(!open)}>
        <ChevronRight size={14} className={cx(s.chev, open && s.chevOpen)} />
        <span>{secs > 0 ? `思考了 ${secs} 秒` : "思考过程"}</span>
      </button>
      {open && <div className={s.thinkBody}>{p.text}</div>}
    </div>
  );
}

// ---- 工具步骤 ----

/** 工具渲染器：按工具名给动词短语与展开后的详情；没有就用通用形式。shell 从 ToolRenderer 注册表填。 */
export interface ToolView {
  summary?(args: unknown): string | undefined;
  icon?: ReactNode;
  detail?(tool: UITool, args: unknown): ReactNode;
}

function parseArgs(t: string): unknown {
  try {
    return JSON.parse(t);
  } catch {
    return t;
  }
}

function defaultSummary(name: string, args: unknown): string {
  if (args && typeof args === "object") {
    const a = args as Record<string, unknown>;
    const v = a.path ?? a.file ?? a.command ?? a.cmd ?? a.query ?? a.url ?? a.pattern;
    if (typeof v === "string") return `${name} ${v.length > 60 ? v.slice(0, 60) + "…" : v}`;
  }
  return name;
}

function StepIcon({ phase }: { phase: UITool["phase"] }) {
  switch (phase) {
    case "args":
    case "running":
      return <Spinner size={12} />;
    case "awaiting":
      return <Clock size={13} />;
    case "ok":
      return <Check size={13} />;
    case "error":
      return <CircleAlert size={13} />;
    case "cancelled":
      return <X size={13} />;
  }
}

export function ToolStep(p: { tool: UITool; view?: ToolView; actions?: UIAction[]; trailing?: ReactNode }) {
  const t = p.tool;
  const args = parseArgs(t.args);
  const failed = t.phase === "error";
  const [open, setOpen] = useState(failed);
  useEffect(() => {
    if (failed) setOpen(true);
  }, [failed]);
  const secs = useElapsed(t.startedAt, t.phase === "running" || t.phase === "args" ? undefined : (t.endedAt ?? t.startedAt));
  const label =
    t.phase === "awaiting"
      ? `${t.name}：等待你的确认`
      : (p.view?.summary?.(args) ?? defaultSummary(t.name, args));
  const phaseCls = t.phase === "ok" ? s.step_ok : t.phase === "error" ? s.step_error : t.phase === "awaiting" ? s.step_awaiting : t.phase === "running" || t.phase === "args" ? s.step_running : undefined;
  return (
    <div className={cx(s.step, phaseCls)} data-call={t.call_id}>
      <button type="button" className={s.stepRow} aria-expanded={open} onClick={() => setOpen(!open)}>
        <span className={s.stepIcon}>{p.view?.icon ?? <StepIcon phase={t.phase} />}</span>
        <span className={s.stepLabel}>{label}</span>
        {(t.phase === "running" || t.phase === "args") && secs > 0 && <span className={s.stepMeta}>{secs}s</span>}
        {p.trailing}
        <ChevronRight size={13} className={cx(s.chev, open && s.chevOpen)} />
      </button>
      {open && (
        <div className={s.stepDetail}>
          {p.view?.detail ? (
            p.view.detail(t, args)
          ) : (
            <>
              <div className={s.detailLabel}>参数</div>
              <CodeBlock code={typeof args === "string" ? args : JSON.stringify(args, null, 2)} lang="json" />
              {t.output !== undefined && (
                <>
                  <div className={s.detailLabel}>{t.truncated ? "输出（已截断）" : "输出"}</div>
                  <CodeBlock code={t.output} />
                </>
              )}
            </>
          )}
          {p.actions && <MessageActions actions={p.actions} pinned />}
        </div>
      )}
    </div>
  );
}

/** 一组相邻的工具调用：进行中展开，结束后折叠成"执行了 N 个步骤"。 */
export function ToolSteps(p: {
  tools: UITool[];
  views?: (name: string) => ToolView | undefined;
  autoCollapse?: boolean;
  actionsFor?: (t: UITool) => UIAction[] | undefined;
  trailing?: (t: UITool) => ReactNode;
}) {
  const busy = p.tools.some((t) => t.phase === "args" || t.phase === "running" || t.phase === "awaiting");
  const failed = p.tools.some((t) => t.phase === "error");
  const [open, setOpen] = useState(true);
  useEffect(() => {
    if (busy) setOpen(true);
    else if (p.autoCollapse !== false && !failed && p.tools.length > 1) setOpen(false);
  }, [busy, failed, p.autoCollapse, p.tools.length]);
  if (p.tools.length === 0) return null;
  const body = (
    <div className={s.stepsBody}>
      {p.tools.map((t) => (
        <ToolStep key={t.call_id} tool={t} view={p.views?.(t.name)} actions={p.actionsFor?.(t)} trailing={p.trailing?.(t)} />
      ))}
    </div>
  );
  if (p.tools.length === 1) return <div className={s.steps}>{body}</div>;
  return (
    <div className={s.steps}>
      <button type="button" className={s.processHead} aria-expanded={open} onClick={() => setOpen(!open)}>
        <ChevronRight size={14} className={cx(s.chev, open && s.chevOpen)} />
        <Wrench size={13} />
        <span>{busy ? `正在执行（${p.tools.length} 个步骤）` : `执行了 ${p.tools.length} 个步骤`}</span>
      </button>
      {open && body}
    </div>
  );
}

// ---- 助手消息 ----

export function AssistantMessage(p: {
  text: string;
  streaming?: boolean;
  interrupted?: boolean;
  actions?: UIAction[];
  before?: ReactNode;
  after?: ReactNode;
  entryId?: string;
}) {
  if (!p.text && !p.streaming) return null;
  return (
    <div className={cx(s.msg, s.assistant)} data-entry={p.entryId}>
      {p.before}
      <Markdown text={p.text} streaming={p.streaming} />
      {p.streaming && <span className={s.cursor} aria-hidden />}
      {p.interrupted && <div className={s.interrupted}>（输出被中断）</div>}
      {p.actions && !p.streaming && <MessageActions actions={p.actions} />}
      {p.after}
    </div>
  );
}

/** 压缩点、注入等系统性条目的一行分隔。 */
export function Divider({ children }: { children: ReactNode }) {
  return <div className={s.divider}>{children}</div>;
}
