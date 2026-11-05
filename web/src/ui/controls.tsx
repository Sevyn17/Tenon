// 基础控件。全部只依赖 React 与设计令牌；状态由调用方持有（受控组件）。

import {
  cloneElement,
  forwardRef,
  isValidElement,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type CSSProperties,
  type InputHTMLAttributes,
  type ReactElement,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { createPortal } from "react-dom";
import s from "./controls.module.css";
import { cx, keyLabels } from "./cx";

export type ButtonVariant = "default" | "primary" | "ghost" | "danger" | "outline";
export type Size = "sm" | "md" | "lg";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: Size;
  block?: boolean;
  icon?: ReactNode;
  loading?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "default", size = "md", block, icon, loading, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type="button"
      className={cx(
        s.btn,
        variant !== "default" && s[variant],
        size === "sm" && s.sm,
        size === "lg" && s.lg,
        block && s.block,
        className,
      )}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...rest}
    >
      {loading ? <Spinner size={12} /> : icon}
      {children}
    </button>
  );
});

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  label: string; // 无障碍名与提示文字
  keybind?: string;
  size?: "sm" | "md";
  pressed?: boolean;
  tooltip?: boolean;
}

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { label, keybind, size = "md", pressed, tooltip = true, className, children, ...rest },
  ref,
) {
  const b = (
    <button
      ref={ref}
      type="button"
      aria-label={label}
      aria-pressed={pressed}
      className={cx(s.icon, size === "sm" && s.iconSm, pressed && s.pressed, className)}
      {...rest}
    >
      {children}
    </button>
  );
  return tooltip ? (
    <Tooltip label={label} keybind={keybind}>
      {b}
    </Tooltip>
  ) : (
    b
  );
});

export function Kbd({ bind, className }: { bind: string; className?: string }) {
  return (
    <span className={cx(s.kbd, className)}>
      {keyLabels(bind).map((k, i) => (
        <kbd key={i}>{k}</kbd>
      ))}
    </span>
  );
}

export type Tone = "neutral" | "accent" | "success" | "danger" | "warning";

export function Badge({ tone = "neutral", children, className }: { tone?: Tone; children: ReactNode; className?: string }) {
  return <span className={cx(s.badge, tone !== "neutral" && s[`badge_${tone}`], className)}>{children}</span>;
}

export function Count({ n, className }: { n: number; className?: string }) {
  if (n <= 0) return null;
  return <span className={cx(s.count, className)}>{n > 99 ? "99+" : n}</span>;
}

export function Spinner({ size = 14, className }: { size?: number; className?: string }) {
  return <span className={cx(s.spinner, className)} style={{ ["--sp" as string]: `${size}px` } as CSSProperties} role="status" aria-label="loading" />;
}

export type DotState = "idle" | "running" | "warning" | "success" | "danger";

export function Dot({ state = "idle", className, title }: { state?: DotState; className?: string; title?: string }) {
  return <span className={cx(s.dot, state !== "idle" && s[`dot_${state}`], className)} title={title} />;
}

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input(
  { className, ...rest },
  ref,
) {
  return <input ref={ref} className={cx(s.input, className)} {...rest} />;
});

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(function Textarea(
  { className, ...rest },
  ref,
) {
  return <textarea ref={ref} className={cx(s.input, className)} {...rest} />;
});

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(function Select(
  { className, ...rest },
  ref,
) {
  return <select ref={ref} className={cx(s.input, className)} {...rest} />;
});

export function Switch(props: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; label?: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={props.checked}
      aria-label={props.label}
      disabled={props.disabled}
      className={s.switch}
      onClick={() => props.onChange(!props.checked)}
    />
  );
}

export function Segmented<T extends string>(props: {
  value: T;
  options: { value: T; label: ReactNode }[];
  onChange: (v: T) => void;
  label?: string;
}) {
  return (
    <div className={s.seg} role="group" aria-label={props.label}>
      {props.options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === props.value} onClick={() => props.onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

// ---- Tooltip：悬停 400ms 后出现，贴在目标下方（放不下就上方），带快捷键 ----

export function Tooltip({
  label,
  keybind,
  children,
  side = "bottom",
}: {
  label: ReactNode;
  keybind?: string;
  children: ReactElement;
  side?: "bottom" | "top" | "right" | "left";
}) {
  const [anchor, setAnchor] = useState<DOMRect | null>(null);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  if (!isValidElement(children)) return children;
  const props = children.props as Record<string, unknown>;
  const show = (e: { currentTarget: Element }) => {
    const el = e.currentTarget;
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setAnchor(el.getBoundingClientRect()), 400);
  };
  const hide = () => {
    window.clearTimeout(timer.current);
    setAnchor(null);
  };
  const chain =
    (name: string, f: (e: never) => void) =>
    (e: never) => {
      f(e);
      (props[name] as ((e: never) => void) | undefined)?.(e);
    };
  return (
    <>
      {cloneElement(children as ReactElement<Record<string, unknown>>, {
        onMouseEnter: chain("onMouseEnter", show),
        onMouseLeave: chain("onMouseLeave", hide),
        onFocus: chain("onFocus", show),
        onBlur: chain("onBlur", hide),
        onMouseDown: chain("onMouseDown", hide),
      })}
      {anchor && createPortal(<TooltipBox anchor={anchor} side={side} label={label} keybind={keybind} />, document.body)}
    </>
  );
}

function TooltipBox({ anchor, side, label, keybind }: { anchor: DOMRect; side: string; label: ReactNode; keybind?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number }>({ left: -9999, top: -9999 });
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const w = el.offsetWidth;
    const h = el.offsetHeight;
    const gap = 6;
    let left = anchor.left + anchor.width / 2 - w / 2;
    let top = anchor.bottom + gap;
    if (side === "top" || (side === "bottom" && top + h > window.innerHeight - 4)) top = anchor.top - h - gap;
    if (side === "right") {
      left = anchor.right + gap;
      top = anchor.top + anchor.height / 2 - h / 2;
    }
    if (side === "left") {
      left = anchor.left - w - gap;
      top = anchor.top + anchor.height / 2 - h / 2;
    }
    left = Math.max(4, Math.min(left, window.innerWidth - w - 4));
    setPos({ left, top });
  }, [anchor, side]);
  return (
    <div ref={ref} className={s.tooltip} style={pos} role="tooltip">
      <span>{label}</span>
      {keybind && <Kbd bind={keybind} />}
    </div>
  );
}
