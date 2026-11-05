// 回合"尾巴"（前端方案第八节）：回合结束后最后一条助手消息下方的一行。内置三块（统计、回合操作、后续建议），
// 每块都可由宿主替换或关掉；异常结束时变成状态行；回合进行中显示"工作中…"与已用时长。

import type { ReactNode } from "react";
import { Square } from "lucide-react";
import { Button, Spinner } from "../controls";
import { cx } from "../cx";
import { MessageActions, useElapsed } from "./messages";
import type { UIAction, UITurnEnd } from "./types";
import s from "./conversation.module.css";

/** 回合统计（数据由宿主从条目 usage 与回合时间算好）。 */
export interface TurnStats {
  durationMs?: number;
  steps?: number;
  tools?: number;
  input?: number;
  output?: number;
  cacheRead?: number;
  model?: string;
}

export function fmtDuration(ms: number): string {
  const sec = Math.round(ms / 1000);
  if (sec < 60) return `${sec} 秒`;
  const m = Math.floor(sec / 60);
  return `${m} 分 ${sec % 60} 秒`;
}

function fmtTokens(n: number): string {
  return n >= 10000 ? `${(n / 1000).toFixed(1)}k` : String(n);
}

export function StatsLine({ stats, onOpen }: { stats: TurnStats; onOpen?: () => void }) {
  const parts: string[] = [];
  if (stats.durationMs) parts.push(fmtDuration(stats.durationMs));
  if (stats.steps) parts.push(`${stats.steps} 步`);
  if (stats.tools) parts.push(`${stats.tools} 次工具`);
  if (stats.input || stats.output) {
    let t = `${fmtTokens(stats.input ?? 0)} → ${fmtTokens(stats.output ?? 0)} tokens`;
    if (stats.cacheRead && stats.input) t += ` · 缓存 ${Math.round((stats.cacheRead / stats.input) * 100)}%`;
    parts.push(t);
  }
  if (stats.model) parts.push(stats.model);
  if (parts.length === 0) return null;
  return (
    <button type="button" className={s.tailStats} onClick={onOpen} disabled={!onOpen} title="查看本回合每步明细">
      {parts.join(" · ")}
    </button>
  );
}

const END_TEXT: Record<string, string> = {
  cancelled: "已取消",
  max_steps: "达到每回合步数上限",
  denied: "输入被拒绝",
  failed: "回合失败",
};

/** 回合异常结束时的状态行（取消 / 步数上限 / 失败）。正常结束不显示。 */
export function EndStatus({ end, onContinue, onRetry }: { end: UITurnEnd; onContinue?: () => void; onRetry?: () => void }) {
  if (end.kind === "done") return null;
  const tone = end.kind === "failed" || end.kind === "denied" ? "danger" : end.kind === "cancelled" ? "muted" : "warning";
  return (
    <div className={cx(s.tailStatus, s[`tailStatus_${tone}`])} role="status">
      <span>
        {END_TEXT[end.kind] ?? end.kind}
        {end.message ? `：${end.message}` : end.code ? `（${end.code}）` : ""}
      </span>
      {end.kind === "max_steps" && onContinue && (
        <Button size="sm" onClick={onContinue}>
          继续
        </Button>
      )}
      {(end.kind === "failed" || end.kind === "cancelled") && onRetry && (
        <Button size="sm" onClick={onRetry}>
          重试
        </Button>
      )}
    </div>
  );
}

/** 回合进行中的状态（代替尾巴）：工作中 · 第 N 步 · 已用时长 · 停止。 */
export function Working({ startedAt, step, label, onStop }: { startedAt: number; step?: string; label?: string; onStop?: () => void }) {
  const sec = useElapsed(startedAt);
  return (
    <div className={s.tailWorking} role="status" aria-live="polite">
      <Spinner size={12} />
      <span className={s.shimmer}>{label ?? "工作中"}</span>
      {step && <span>· {step}</span>}
      <span>· {fmtDuration(sec * 1000)}</span>
      {onStop && (
        <Button size="sm" variant="ghost" icon={<Square size={11} />} onClick={onStop}>
          停止
        </Button>
      )}
    </div>
  );
}

/** 后续建议：点了填入输入框（不直接发）。 */
export function Followups({ items, onPick }: { items: string[]; onPick: (t: string) => void }) {
  if (items.length === 0) return null;
  return (
    <div className={s.followups}>
      {items.map((t) => (
        <button key={t} type="button" className={s.followup} onClick={() => onPick(t)}>
          {t}
        </button>
      ))}
    </div>
  );
}

/**
 * 回合尾巴：统计 · 回合操作 · 后续建议，再加宿主插槽（extra）。
 * 每块都是可选的：宿主不给就不渲染，便于整体替换或关掉某一块。
 */
export function TurnTail(p: {
  stats?: TurnStats;
  onOpenStats?: () => void;
  actions?: UIAction[];
  followups?: string[];
  onFollowup?: (t: string) => void;
  end?: UITurnEnd | null;
  onContinue?: () => void;
  onRetry?: () => void;
  extra?: ReactNode;
}) {
  return (
    <div className={s.tail}>
      {p.end && <EndStatus end={p.end} onContinue={p.onContinue} onRetry={p.onRetry} />}
      <div className={s.tailRow}>
        {p.stats && <StatsLine stats={p.stats} onOpen={p.onOpenStats} />}
        {p.actions && p.actions.length > 0 && <MessageActions actions={p.actions} pinned />}
        {p.extra}
      </div>
      {p.followups && p.onFollowup && <Followups items={p.followups} onPick={p.onFollowup} />}
    </div>
  );
}
