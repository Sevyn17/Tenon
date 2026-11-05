// 对话的导航工具（前端方案第七、十四节）：回合导航条、对话内查找、选中文本工具条。
// 它们不接数据，只按 MessageList 渲染出的 DOM 工作：滚动容器带 data-scroll，回合是 [data-turn]，消息是 [data-entry]。
// root 是包住 MessageList 的元素（对话区的 body）；找不到滚动容器时什么都不做。

import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { ChevronDown, ChevronUp, X } from "lucide-react";
import { IconButton, Tooltip } from "../controls";
import type { UIAction } from "./types";
import s from "./conversation.module.css";

const scroller = (root: HTMLElement | null) => root?.querySelector<HTMLElement>("[data-scroll]") ?? null;

/** 视口顶部所在的回合序号（在 sections 里的下标）。 */
function currentIndex(sc: HTMLElement, secs: HTMLElement[]): number {
  const top = sc.getBoundingClientRect().top + 80;
  let cur = 0;
  secs.forEach((x, i) => {
    if (x.getBoundingClientRect().top <= top) cur = i;
  });
  return cur;
}

export function scrollToTurn(root: HTMLElement | null, turn: number) {
  root?.querySelector<HTMLElement>(`[data-turn="${turn}"]`)?.scrollIntoView({ block: "start", behavior: "smooth" });
}

/** 上 / 下一回合（⌥↑ / ⌥↓）。 */
export function jumpTurn(root: HTMLElement | null, dir: -1 | 1) {
  const sc = scroller(root);
  if (!sc) return;
  const secs = [...sc.querySelectorAll<HTMLElement>("[data-turn]")];
  if (!secs.length) return;
  const i = Math.min(Math.max(currentIndex(sc, secs) + dir, 0), secs.length - 1);
  secs[i].scrollIntoView({ block: "start", behavior: "smooth" });
}

// ---- 回合导航条 ----

export interface TurnMark {
  turn: number;
  /** 悬停预览（用户输入的首句）。 */
  label: string;
}

/** 对话区右缘的细导航：每个用户回合一个刻度，悬停预览首句，点击跳转；当前回合高亮。少于两个回合时不显示。 */
export function TurnNav({ root, marks }: { root: RefObject<HTMLElement | null>; marks: TurnMark[] }) {
  const [cur, setCur] = useState<number | null>(null);
  useEffect(() => {
    const sc = scroller(root.current);
    if (!sc) return;
    let raf = 0;
    const on = () => {
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(() => {
        const secs = [...sc.querySelectorAll<HTMLElement>("[data-turn]")];
        if (secs.length) setCur(Number(secs[currentIndex(sc, secs)].dataset.turn));
      });
    };
    on();
    sc.addEventListener("scroll", on, { passive: true });
    return () => {
      cancelAnimationFrame(raf);
      sc.removeEventListener("scroll", on);
    };
  }, [root, marks.length]);
  if (marks.length < 2) return null;
  return (
    <nav className={s.turnNav} aria-label="回合导航">
      {marks.map((m) => (
        <Tooltip key={m.turn} label={m.label} side="left">
          <button
            type="button"
            className={s.turnTick}
            data-active={m.turn === cur || undefined}
            aria-label={`第 ${m.turn} 回合：${m.label}`}
            onClick={() => scrollToTurn(root.current, m.turn)}
          />
        </Tooltip>
      ))}
    </nav>
  );
}

// ---- 对话内查找 ----

// CSS Custom Highlight API（Chromium / WebView2、Safari 17.2+、Firefox 140+）：不改 DOM 就能高亮。
// 不支持时只跳转、不高亮。
interface HighlightRegistryLike {
  set(name: string, h: unknown): void;
  delete(name: string): void;
}
declare const Highlight: { new (...r: Range[]): unknown } | undefined;
const registry = (): HighlightRegistryLike | null =>
  typeof CSS !== "undefined" && "highlights" in CSS && typeof Highlight !== "undefined" ? (CSS as unknown as { highlights: HighlightRegistryLike }).highlights : null;

const MAX_HITS = 1000;

function findRanges(sc: HTMLElement, q: string): Range[] {
  const needle = q.toLowerCase();
  const out: Range[] = [];
  const walk = document.createTreeWalker(sc, NodeFilter.SHOW_TEXT);
  for (let n = walk.nextNode(); n && out.length < MAX_HITS; n = walk.nextNode()) {
    const text = (n.nodeValue ?? "").toLowerCase();
    for (let i = text.indexOf(needle); i >= 0 && out.length < MAX_HITS; i = text.indexOf(needle, i + needle.length)) {
      const r = document.createRange();
      r.setStart(n, i);
      r.setEnd(n, i + needle.length);
      out.push(r);
    }
  }
  return out;
}

/**
 * 查找条（⌘F）：在消息流里找文字，Enter 下一个、Shift+Enter 上一个、Esc 关闭。内容变化（流式）时重新计算。
 * 折叠起来的推理与工具详情不在 DOM 里，搜不到。
 */
export function FindBar({ root, onClose, focusSignal }: { root: RefObject<HTMLElement | null>; onClose: () => void; focusSignal?: number }) {
  const [q, setQ] = useState("");
  const [i, setI] = useState(0);
  const [n, setN] = useState(0);
  const ranges = useRef<Range[]>([]);
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, [focusSignal]);

  const paint = useCallback((at: number, scroll: boolean) => {
    const reg = registry();
    const rs = ranges.current;
    if (reg) {
      if (rs.length) reg.set("ybase-find", new Highlight!(...rs));
      else reg.delete("ybase-find");
      if (rs[at]) reg.set("ybase-find-current", new Highlight!(rs[at]));
      else reg.delete("ybase-find-current");
    }
    if (scroll && rs[at]) rs[at].startContainer.parentElement?.scrollIntoView({ block: "center" });
  }, []);

  const recompute = useCallback(
    (scroll: boolean) => {
      const sc = scroller(root.current);
      ranges.current = sc && q.trim() ? findRanges(sc, q.trim()) : [];
      setN(ranges.current.length);
      setI((x) => {
        const at = Math.min(x, Math.max(0, ranges.current.length - 1));
        paint(at, scroll);
        return at;
      });
    },
    [q, root, paint],
  );

  // 查询变化：从第一个开始；内容变化：保留位置、不滚动。
  useEffect(() => {
    setI(0);
    recompute(true);
    const sc = scroller(root.current);
    if (!sc) return;
    let t: ReturnType<typeof setTimeout> | undefined;
    const mo = new MutationObserver(() => {
      clearTimeout(t);
      t = setTimeout(() => recompute(false), 200);
    });
    mo.observe(sc, { subtree: true, childList: true, characterData: true });
    return () => {
      clearTimeout(t);
      mo.disconnect();
    };
  }, [q, root, recompute]);

  useEffect(
    () => () => {
      registry()?.delete("ybase-find");
      registry()?.delete("ybase-find-current");
    },
    [],
  );

  const go = (d: -1 | 1) => {
    if (!n) return;
    const at = (i + d + n) % n;
    setI(at);
    paint(at, true);
  };

  return (
    <div className={s.findBar} role="search">
      <input
        ref={input}
        className={s.findInput}
        value={q}
        placeholder="在对话中查找"
        aria-label="在对话中查找"
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing) return;
          if (e.key === "Enter") {
            e.preventDefault();
            go(e.shiftKey ? -1 : 1);
          } else if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            onClose();
          }
        }}
      />
      <span className={s.findCount} aria-live="polite">
        {q.trim() ? (n ? `${i + 1} / ${n}${n >= MAX_HITS ? "+" : ""}` : "无结果") : ""}
      </span>
      <IconButton label="上一个" size="sm" disabled={!n} onClick={() => go(-1)}>
        <ChevronUp size={14} />
      </IconButton>
      <IconButton label="下一个" size="sm" disabled={!n} onClick={() => go(1)}>
        <ChevronDown size={14} />
      </IconButton>
      <IconButton label="关闭" size="sm" onClick={onClose}>
        <X size={14} />
      </IconButton>
    </div>
  );
}

// ---- 选中文本工具条 ----

/** 在一条消息里选中文字时，选区上方浮出的小工具条（复制、引用、就这段追问……操作由宿主给）。跨消息的选区不显示。 */
export function SelectionToolbar({ root, actions }: { root: RefObject<HTMLElement | null>; actions: (text: string) => UIAction[] }) {
  const [sel, setSel] = useState<{ text: string; x: number; y: number } | null>(null);

  useEffect(() => {
    const check = () => {
      const s0 = getSelection();
      if (!s0 || s0.isCollapsed || !s0.rangeCount) return setSel(null);
      const r = s0.getRangeAt(0);
      const sc = scroller(root.current);
      if (!sc || !sc.contains(r.commonAncestorContainer)) return setSel(null);
      const node = r.commonAncestorContainer;
      const el = (node instanceof Element ? node : node.parentElement)?.closest("[data-entry]");
      const text = (s0.toString() || r.toString()).trim(); // 后台标签页里 Selection.toString 可能为空，退回选区本身的文本
      if (!el || !text) return setSel(null);
      const b = r.getBoundingClientRect();
      setSel({ text, x: b.left + b.width / 2, y: b.top });
    };
    const later = () => setTimeout(check, 0);
    const onChange = () => {
      if (getSelection()?.isCollapsed) setSel(null);
    };
    const sc = scroller(root.current);
    const hide = () => setSel(null);
    document.addEventListener("mouseup", later);
    document.addEventListener("keyup", later);
    document.addEventListener("selectionchange", onChange);
    sc?.addEventListener("scroll", hide, { passive: true });
    return () => {
      document.removeEventListener("mouseup", later);
      document.removeEventListener("keyup", later);
      document.removeEventListener("selectionchange", onChange);
      sc?.removeEventListener("scroll", hide);
    };
  }, [root]);

  if (!sel) return null;
  const list = actions(sel.text);
  if (!list.length) return null;
  return (
    <div className={s.selBar} style={{ left: sel.x, top: sel.y }} role="toolbar" aria-label="选中文本操作">
      {list.map((a) => (
        <button
          key={a.id}
          type="button"
          className={s.selBtn}
          disabled={a.disabled}
          title={a.reason ?? a.label}
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => {
            a.run();
            getSelection()?.removeAllRanges();
            setSel(null);
          }}
        >
          {a.icon}
          <span>{a.label}</span>
        </button>
      ))}
    </div>
  );
}
