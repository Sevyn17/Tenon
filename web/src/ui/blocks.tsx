// 内容块：Card、Tabs、EmptyState、Skeleton、ErrorBoundary、CodeBlock、DiffView、KeyValue、Progress / ProgressRing。

import { Component, useState, type ErrorInfo, type ReactNode } from "react";
import { Check, ChevronRight, Copy, WrapText } from "lucide-react";
import { cx } from "./cx";
import { IconButton } from "./controls";
import s from "./blocks.module.css";

// ---- Card ----

export interface CardProps {
  title: ReactNode;
  icon?: ReactNode;
  actions?: ReactNode;
  /** 可折叠：给 open / onToggle 受控，或只给 defaultOpen。不给时不可折叠。 */
  open?: boolean;
  defaultOpen?: boolean;
  onToggle?: (open: boolean) => void;
  collapsible?: boolean;
  flush?: boolean;
  className?: string;
  children?: ReactNode;
}

export function Card(p: CardProps) {
  const [own, setOwn] = useState(p.defaultOpen ?? true);
  const open = p.open ?? own;
  const toggle = () => {
    if (!p.collapsible) return;
    setOwn(!open);
    p.onToggle?.(!open);
  };
  return (
    <section className={cx(s.card, p.className)}>
      <header className={s.cardHead} data-toggle={p.collapsible ? "" : undefined} onClick={toggle}>
        {p.collapsible && <ChevronRight size={14} className={s.cardChevron} data-open={open} />}
        {p.icon}
        <span className={s.cardTitle}>{p.title}</span>
        {p.actions && <span onClick={(e) => e.stopPropagation()}>{p.actions}</span>}
      </header>
      {(!p.collapsible || open) && p.children !== undefined && (
        <div className={s.cardBody} data-flush={p.flush ? "" : undefined}>
          {p.children}
        </div>
      )}
    </section>
  );
}

// ---- Tabs ----

export function Tabs<T extends string>(p: {
  value: T;
  onChange: (v: T) => void;
  items: { value: T; label: ReactNode }[];
  className?: string;
}) {
  const move = (dir: number) => {
    const i = p.items.findIndex((x) => x.value === p.value);
    const n = p.items[(i + dir + p.items.length) % p.items.length];
    if (n) p.onChange(n.value);
  };
  return (
    <div
      className={cx(s.tabs, p.className)}
      role="tablist"
      onKeyDown={(e) => {
        if (e.key === "ArrowRight") move(1);
        else if (e.key === "ArrowLeft") move(-1);
      }}
    >
      {p.items.map((it) => (
        <button
          key={it.value}
          role="tab"
          className={s.tab}
          aria-selected={it.value === p.value}
          tabIndex={it.value === p.value ? 0 : -1}
          onClick={() => p.onChange(it.value)}
        >
          {it.label}
        </button>
      ))}
    </div>
  );
}

// ---- EmptyState / Skeleton ----

export function EmptyState(p: { icon?: ReactNode; title: ReactNode; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className={s.empty}>
      {p.icon && <div className={s.emptyIcon}>{p.icon}</div>}
      <div className={s.emptyTitle}>{p.title}</div>
      {p.children && <div>{p.children}</div>}
      {p.action}
    </div>
  );
}

export function Skeleton({ w = "100%", h = 12, className }: { w?: number | string; h?: number; className?: string }) {
  return <div className={cx(s.skeleton, className)} style={{ width: w, height: h }} />;
}

// ---- ErrorBoundary：贡献抛错只影响它自己的位置（错误隔离） ----

export interface ErrorBoundaryProps {
  /** 出错时显示在占位里的名字（贡献 id、区域名）。 */
  label?: string;
  fallback?: (err: Error, reset: () => void) => ReactNode;
  onError?: (err: Error, info: ErrorInfo) => void;
  /** 变化时重置（如会话切换）。 */
  resetKey?: unknown;
  children: ReactNode;
}

interface EBState {
  err: Error | null;
  key: unknown;
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, EBState> {
  state: EBState = { err: null, key: this.props.resetKey };
  static getDerivedStateFromError(err: Error): Partial<EBState> {
    return { err };
  }
  static getDerivedStateFromProps(p: ErrorBoundaryProps, st: EBState): Partial<EBState> | null {
    return p.resetKey !== st.key ? { err: null, key: p.resetKey } : null;
  }
  componentDidCatch(err: Error, info: ErrorInfo) {
    this.props.onError?.(err, info);
  }
  reset = () => this.setState({ err: null });
  render() {
    const { err } = this.state;
    if (!err) return this.props.children;
    if (this.props.fallback) return this.props.fallback(err, this.reset);
    return (
      <div className={s.errorBox} role="alert">
        {this.props.label ? <code>{this.props.label}</code> : null} 出错：{err.message}{" "}
        <button type="button" onClick={this.reset} style={{ background: "none", border: 0, color: "inherit", textDecoration: "underline", cursor: "pointer" }}>
          重试
        </button>
      </div>
    );
  }
}

// ---- CodeBlock ----

/** 复制到剪贴板，返回是否成功。 */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

export function CopyButton({ text, label = "复制" }: { text: string | (() => string); label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <IconButton
      size="sm"
      label={done ? "已复制" : label}
      onClick={async () => {
        if (await copyText(typeof text === "function" ? text() : text)) {
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        }
      }}
    >
      {done ? <Check size={14} /> : <Copy size={14} />}
    </IconButton>
  );
}

export function CodeBlock(p: { code: string; lang?: string; actions?: ReactNode; wrap?: boolean; className?: string }) {
  const [wrap, setWrap] = useState(p.wrap ?? false);
  return (
    <div className={cx(s.code, p.className)}>
      <div className={s.codeHead}>
        <span>{p.lang || "text"}</span>
        {p.actions}
        <IconButton size="sm" label={wrap ? "不换行" : "自动换行"} pressed={wrap} onClick={() => setWrap(!wrap)}>
          <WrapText size={14} />
        </IconButton>
        <CopyButton text={p.code} />
      </div>
      <pre data-wrap={wrap}>
        <code>{p.code}</code>
      </pre>
    </div>
  );
}

// ---- DiffView：统一 diff 文本按行着色 ----

export function DiffView({ diff, className }: { diff: string; className?: string }) {
  return (
    <div className={cx(s.diff, className)}>
      {diff.split("\n").map((l, i) => (
        <div
          key={i}
          className={cx(
            s.diffLine,
            l.startsWith("+") && !l.startsWith("+++") && s.diffAdd,
            l.startsWith("-") && !l.startsWith("---") && s.diffDel,
            l.startsWith("@@") && s.diffHunk,
          )}
        >
          {l || " "}
        </div>
      ))}
    </div>
  );
}

// ---- KeyValue / Progress ----

export function KeyValue({ rows, className }: { rows: [ReactNode, ReactNode][]; className?: string }) {
  return (
    <dl className={cx(s.kv, className)}>
      {rows.map(([k, v], i) => (
        <div key={i} style={{ display: "contents" }}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function Progress({ value, className }: { value: number; className?: string }) {
  return (
    <div className={cx(s.progress, className)} role="progressbar" aria-valuenow={Math.round(value * 100)}>
      <div style={{ width: `${Math.max(0, Math.min(1, value)) * 100}%` }} />
    </div>
  );
}

/** 环形进度（上下文占用）：超过 80% 变黄，95% 变红。 */
export function ProgressRing({ value, size = 18, title }: { value: number; size?: number; title?: string }) {
  const r = (size - 3) / 2;
  const c = 2 * Math.PI * r;
  const v = Math.max(0, Math.min(1, value));
  const level = v >= 0.95 ? "danger" : v >= 0.8 ? "warning" : undefined;
  return (
    <svg width={size} height={size} className={s.ring} role="img" aria-label={title}>
      {title && <title>{title}</title>}
      <circle className={s.ringTrack} cx={size / 2} cy={size / 2} r={r} />
      <circle
        className={s.ringFill}
        data-level={level}
        cx={size / 2}
        cy={size / 2}
        r={r}
        strokeDasharray={c}
        strokeDashoffset={c * (1 - v)}
      />
    </svg>
  );
}
