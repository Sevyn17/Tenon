// 浮层组件：Popover（锚定定位）、Menu（分组、键盘导航）、Dialog（焦点陷阱、Esc 关闭、焦点归还）、Toast。
// 只依赖 React；位置用 style 属性写（CSSOM，不受 CSP style-src 限制）。

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { X } from "lucide-react";
import { IconButton, Kbd } from "./controls";
import { cx } from "./cx";
import s from "./overlay.module.css";

// ---- Popover ----

export type Placement = "bottom-start" | "bottom-end" | "top-start" | "top-end" | "right-start";

export interface PopoverProps {
  anchor: RefObject<HTMLElement | null>;
  open: boolean;
  onClose: () => void;
  placement?: Placement;
  children: ReactNode;
  className?: string;
}

/** 锚定在 anchor 元素旁的浮层；点外面或 Esc 关闭，超出视口时翻转。 */
export function Popover({ anchor, open, onClose, placement = "bottom-start", children, className }: PopoverProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);

  useLayoutEffect(() => {
    if (!open) return;
    const place = () => {
      const a = anchor.current?.getBoundingClientRect();
      const el = ref.current;
      if (!a || !el) return;
      const w = el.offsetWidth;
      const h = el.offsetHeight;
      const gap = 4;
      let top: number;
      let left: number;
      if (placement === "right-start") {
        top = a.top;
        left = a.right + gap;
      } else {
        const up = placement.startsWith("top");
        top = up ? a.top - h - gap : a.bottom + gap;
        left = placement.endsWith("end") ? a.right - w : a.left;
        if (!up && top + h > innerHeight - 8 && a.top - h - gap > 8) top = a.top - h - gap;
        if (up && top < 8) top = a.bottom + gap;
      }
      left = Math.max(8, Math.min(left, innerWidth - w - 8));
      top = Math.max(8, Math.min(top, innerHeight - h - 8));
      setPos({ top, left });
    };
    place();
    addEventListener("resize", place);
    return () => removeEventListener("resize", place);
  }, [open, anchor, placement]);

  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (ref.current?.contains(t) || anchor.current?.contains(t)) return;
      onClose();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
        anchor.current?.focus();
      }
    };
    addEventListener("pointerdown", down, true);
    addEventListener("keydown", key, true);
    return () => {
      removeEventListener("pointerdown", down, true);
      removeEventListener("keydown", key, true);
    };
  }, [open, onClose, anchor]);

  if (!open) return null;
  return createPortal(
    <div
      ref={ref}
      className={cx(s.popover, className)}
      style={pos ? { top: pos.top, left: pos.left } : { top: -9999, left: -9999 }}
      role="dialog"
    >
      {children}
    </div>,
    document.body,
  );
}

// ---- Menu ----

export interface MenuItemSpec {
  id: string;
  label: ReactNode;
  icon?: ReactNode;
  description?: ReactNode;
  keybind?: string;
  hint?: ReactNode;
  disabled?: boolean;
  /** 停用原因（停用时悬停显示，不出现点了没反应的按钮）。 */
  reason?: string;
  danger?: boolean;
  checked?: boolean;
  run?: () => void;
}

export interface MenuGroupSpec {
  id: string;
  label?: ReactNode;
  items: MenuItemSpec[];
}

/** 菜单内容（放进 Popover 里用）：↑↓ 移动、Enter 选中、Home / End、首字母跳转。 */
export function MenuList({ groups, onDone }: { groups: MenuGroupSpec[]; onDone: () => void }) {
  const flat = groups.flatMap((g) => g.items);
  const [active, setActive] = useState(() => flat.findIndex((i) => !i.disabled));
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    ref.current?.focus();
  }, []);

  const move = (d: number) => {
    if (flat.length === 0) return;
    let i = active;
    for (let n = 0; n < flat.length; n++) {
      i = (i + d + flat.length) % flat.length;
      if (!flat[i].disabled) break;
    }
    setActive(i);
  };

  const choose = (it: MenuItemSpec) => {
    if (it.disabled) return;
    onDone();
    it.run?.();
  };

  const onKey = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        move(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        move(-1);
        break;
      case "Home":
        e.preventDefault();
        setActive(flat.findIndex((i) => !i.disabled));
        break;
      case "End":
        e.preventDefault();
        for (let i = flat.length - 1; i >= 0; i--)
          if (!flat[i].disabled) {
            setActive(i);
            break;
          }
        break;
      case "Enter":
      case " ":
        e.preventDefault();
        if (flat[active]) choose(flat[active]);
        break;
      default:
        if (e.key.length === 1) {
          const k = e.key.toLowerCase();
          const i = flat.findIndex((it, j) => j > active && !it.disabled && String(it.label).toLowerCase().startsWith(k));
          if (i >= 0) setActive(i);
        }
    }
  };

  let idx = -1;
  return (
    <div ref={ref} role="menu" tabIndex={-1} onKeyDown={onKey} className="ybMenu">
      {groups.map((g) =>
        g.items.length === 0 ? null : (
          <div key={g.id} className={s.menuGroup} role="group">
            {g.label && <div className={s.menuLabel}>{g.label}</div>}
            {g.items.map((it) => {
              const i = ++idx;
              return (
                <button
                  key={it.id}
                  type="button"
                  role={it.checked === undefined ? "menuitem" : "menuitemcheckbox"}
                  aria-checked={it.checked}
                  className={cx(s.menuItem, it.danger && s.danger)}
                  data-active={i === active}
                  disabled={it.disabled}
                  title={it.disabled ? it.reason : undefined}
                  onMouseEnter={() => !it.disabled && setActive(i)}
                  onClick={() => choose(it)}
                >
                  <span className={s.menuIcon}>{it.checked ? "✓" : it.icon}</span>
                  <span className={s.menuText}>
                    {it.label}
                    {it.description && <span className={s.menuDesc}>{it.description}</span>}
                  </span>
                  {it.hint && <span className={s.menuHint}>{it.hint}</span>}
                  {it.keybind && <Kbd bind={it.keybind} />}
                </button>
              );
            })}
          </div>
        ),
      )}
    </div>
  );
}

/** 触发按钮 + 下拉菜单。 */
export function Menu({
  trigger,
  groups,
  placement,
}: {
  trigger: (props: { ref: RefObject<HTMLButtonElement | null>; open: boolean; toggle: () => void }) => ReactNode;
  groups: MenuGroupSpec[] | (() => MenuGroupSpec[]);
  placement?: Placement;
}) {
  const ref = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const close = useCallback(() => setOpen(false), []);
  return (
    <>
      {trigger({ ref, open, toggle: () => setOpen((o) => !o) })}
      <Popover anchor={ref} open={open} onClose={close} placement={placement}>
        <MenuList groups={typeof groups === "function" ? groups() : groups} onDone={close} />
      </Popover>
    </>
  );
}

// ---- Dialog ----

const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])';

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  children: ReactNode;
  /** 点遮罩是否关闭（缺省 true）。 */
  dismissable?: boolean;
  className?: string;
  bodyClassName?: string;
}

/** 模态框：打开时焦点移进来、Tab 不会跑出去，关闭时还给打开前的元素。 */
export function Dialog({ open, onClose, title, actions, footer, wide, children, dismissable = true, className, bodyClassName }: DialogProps) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const prev = document.activeElement as HTMLElement | null;
    const first = ref.current?.querySelector<HTMLElement>("[data-autofocus]") ?? ref.current?.querySelector<HTMLElement>(FOCUSABLE);
    (first ?? ref.current)?.focus();
    return () => prev?.focus?.();
  }, [open]);
  if (!open) return null;
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key !== "Tab") return;
    const els = [...(ref.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [])];
    if (els.length === 0) return;
    const i = els.indexOf(document.activeElement as HTMLElement);
    if (e.shiftKey && i <= 0) {
      e.preventDefault();
      els[els.length - 1].focus();
    } else if (!e.shiftKey && i === els.length - 1) {
      e.preventDefault();
      els[0].focus();
    }
  };
  return createPortal(
    <div className={s.scrim} onPointerDown={(e) => dismissable && e.target === e.currentTarget && onClose()}>
      <div ref={ref} role="dialog" aria-modal="true" tabIndex={-1} className={cx(s.dialog, wide && s.dialogWide, className)} onKeyDown={onKey}>
        {(title || actions) && (
          <div className={s.dialogHead}>
            <h2 className={s.dialogTitle}>{title}</h2>
            {actions}
            <IconButton label="关闭" size="sm" onClick={onClose}>
              <X size={16} />
            </IconButton>
          </div>
        )}
        <div className={cx(s.dialogBody, bodyClassName)}>{children}</div>
        {footer && <div className={s.dialogFoot}>{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

// ---- Toast ----

export type ToastTone = "info" | "success" | "warning" | "danger";

export interface ToastSpec {
  id: number;
  text: ReactNode;
  tone: ToastTone;
  action?: { label: string; run: () => void };
}

interface ToastApi {
  show(text: ReactNode, opts?: { tone?: ToastTone; ms?: number; action?: ToastSpec["action"] }): void;
}

const ToastCtx = createContext<ToastApi>({ show: () => {} });

let toastSeq = 0;

/** 提示条容器：顶部居中，自动消失；同时最多 4 条。 */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastSpec[]>([]);
  const api = useRef<ToastApi>({
    show(text, opts = {}) {
      const id = ++toastSeq;
      setItems((xs) => [...xs.slice(-3), { id, text, tone: opts.tone ?? "info", action: opts.action }]);
      setTimeout(() => setItems((xs) => xs.filter((x) => x.id !== id)), opts.ms ?? 5000);
    },
  });
  return (
    <ToastCtx.Provider value={api.current}>
      {children}
      {createPortal(
        <div className={s.toasts} aria-live="polite">
          {items.map((t) => (
            <div key={t.id} className={cx(s.toast, t.tone !== "info" && s[`toast_${t.tone}`])} role="status">
              <span>{t.text}</span>
              {t.action && (
                <button
                  type="button"
                  className="ybLink"
                  onClick={() => {
                    t.action!.run();
                    setItems((xs) => xs.filter((x) => x.id !== t.id));
                  }}
                >
                  {t.action.label}
                </button>
              )}
            </div>
          ))}
        </div>,
        document.body,
      )}
    </ToastCtx.Provider>
  );
}

export function useToast(): ToastApi {
  return useContext(ToastCtx);
}
