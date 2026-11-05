// 输入框（前端方案第九节）。不依赖协议客户端：发送、附件上传、命令都由调用方经 props 给。
//
// 结构：above（排队消息、插件 dock）→ 输入卡（附件条 → 编辑区 → 工具栏）→ below（提示行）。
// "+" 按钮与在开头输入 "/" 打开同一个菜单（ComposerItem 列表，分"添加"与"命令"两组）。
// takeover 非空时（阻塞式交互）在输入卡位置显示它，编辑区保持挂载、草稿不丢。

import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowUp, Paperclip, Plus, Square, X } from "lucide-react";
import { cx } from "../cx";
import { IconButton, Spinner, Tooltip } from "../controls";
import s from "./conversation.module.css";

/** "+" / "/" 菜单里的一项。 */
export interface ComposerItem {
  id: string;
  group: "add" | "command";
  title: string;
  description?: string;
  icon?: ReactNode;
  /** 斜杠名（不含 /）；有它才能用 /name 直接调出。 */
  slash?: string;
  keywords?: string[];
  disabled?: boolean;
  reason?: string;
  /** 选中后执行；返回的字符串会填进编辑区（如 "/compact "）。 */
  run: () => void | string | Promise<void | string>;
}

export interface Attachment {
  id: string;
  name: string;
  mime: string;
  size: number;
  state: "uploading" | "ready" | "failed";
  preview?: string; // 图片的 object URL
  error?: string;
}

export interface QueuedItem {
  id: string;
  text: string;
  state: "sending" | "failed";
  error?: string;
}

export interface ComposerProps {
  value: string;
  onChange: (v: string) => void;
  onSend: () => void;
  onStop?: () => void;
  running?: boolean;
  /** 发送中（防重复提交）。 */
  busy?: boolean;
  disabled?: boolean;
  disabledReason?: string;
  placeholder?: string;
  /** Enter 发送（缺省）还是 Ctrl/⌘+Enter 发送。 */
  sendKey?: "enter" | "mod-enter";
  items?: ComposerItem[];
  attachments?: Attachment[];
  onAttach?: (files: File[]) => void;
  onRemoveAttachment?: (id: string) => void;
  queued?: QueuedItem[];
  onRetryQueued?: (id: string) => void;
  onDiscardQueued?: (id: string) => void;
  /** 空编辑区按 ↑ 取上一条输入。 */
  onHistory?: (dir: -1 | 1) => string | undefined;
  /** 插槽。 */
  above?: ReactNode;
  toolbarLeft?: ReactNode;
  toolbarRight?: ReactNode;
  below?: ReactNode;
  status?: ReactNode;
  takeover?: ReactNode;
  autoFocus?: boolean;
}

export function Composer(p: ComposerProps) {
  const ta = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const [menu, setMenu] = useState<"plus" | "slash" | null>(null);
  const [active, setActive] = useState(0);
  const [drag, setDrag] = useState(false);

  // 自动增高（最高约 12 行）。
  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "0px"; // 从 0 量：height:auto 在 flex 列里会被父容器撑开，量出的 scrollHeight 偏大
    el.style.height = Math.min(el.scrollHeight, 280) + "px";
  }, [p.value]);

  useEffect(() => {
    if (p.autoFocus) ta.current?.focus();
  }, [p.autoFocus]);

  // "/" 菜单：编辑区以 /xxx 开头且没有空格时。
  const slashQuery = /^\/(\S*)$/.exec(p.value)?.[1];
  useEffect(() => {
    if (slashQuery !== undefined && (p.items?.length ?? 0) > 0) setMenu("slash");
    else if (menu === "slash") setMenu(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slashQuery]);

  const shown = useMemo(() => {
    const all = p.items ?? [];
    if (menu === "plus") return all;
    if (menu === "slash") {
      const q = (slashQuery ?? "").toLowerCase();
      return all.filter(
        (i) =>
          i.slash &&
          (i.slash.toLowerCase().includes(q) ||
            i.title.toLowerCase().includes(q) ||
            (i.keywords ?? []).some((k) => k.toLowerCase().includes(q))),
      );
    }
    return [];
  }, [menu, p.items, slashQuery]);

  useEffect(() => setActive(0), [menu, slashQuery]);

  const pick = async (it: ComposerItem) => {
    if (it.disabled) return;
    setMenu(null);
    if (menu === "slash") p.onChange("");
    const r = await it.run();
    if (typeof r === "string") p.onChange(r);
    ta.current?.focus();
  };

  const canSend = !p.disabled && !p.busy && (p.value.trim() !== "" || (p.attachments ?? []).some((a) => a.state === "ready"));
  const uploading = (p.attachments ?? []).some((a) => a.state === "uploading");

  const onKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return; // 输入法候选中
    if (menu && shown.length) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setActive((a) => (a + 1) % shown.length);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setActive((a) => (a - 1 + shown.length) % shown.length);
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        void pick(shown[active]);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setMenu(null);
        return;
      }
    }
    if (e.key === "Escape" && p.running && p.onStop) {
      // Esc 两次停止：第一次只提示
      const now = Date.now();
      const last = Number(ta.current?.dataset.esc ?? 0);
      if (now - last < 800) {
        p.onStop();
        if (ta.current) ta.current.dataset.esc = "0";
      } else if (ta.current) ta.current.dataset.esc = String(now);
      return;
    }
    if ((e.key === "ArrowUp" || e.key === "ArrowDown") && p.value === "" && p.onHistory) {
      const h = p.onHistory(e.key === "ArrowUp" ? -1 : 1);
      if (h !== undefined) {
        e.preventDefault();
        p.onChange(h);
      }
      return;
    }
    if (e.key === "Enter") {
      const mod = e.ctrlKey || e.metaKey;
      const send = p.sendKey === "mod-enter" ? mod : !e.shiftKey && !mod;
      const alt = p.sendKey === "mod-enter" ? false : mod; // Ctrl+Enter 在 enter 模式下也发送
      if (send || alt) {
        e.preventDefault();
        if (canSend && !uploading) p.onSend();
      }
    }
  };

  const onPaste = (e: React.ClipboardEvent) => {
    const files = [...e.clipboardData.files];
    if (files.length && p.onAttach) {
      e.preventDefault();
      p.onAttach(files);
    }
  };

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setDrag(false);
    const files = [...e.dataTransfer.files];
    if (files.length && p.onAttach) p.onAttach(files);
  };

  return (
    <div className={s.composer}>
      {(p.queued?.length || p.above) && (
        <div className={s.above}>
          {p.queued?.map((q) => (
            <div key={q.id} className={cx(s.queued, q.state === "failed" && s.queued_failed)}>
              {q.state === "sending" ? <Spinner size={12} /> : null}
              <span className={s.queuedText} title={q.text}>
                {q.text}
              </span>
              {q.state === "failed" && (
                <>
                  <button type="button" onClick={() => p.onRetryQueued?.(q.id)} title={q.error}>
                    重发
                  </button>
                  <button type="button" onClick={() => p.onDiscardQueued?.(q.id)}>
                    丢弃
                  </button>
                </>
              )}
            </div>
          ))}
          {p.above}
        </div>
      )}
      {p.status}
      {p.takeover ? (
        <div>{p.takeover}</div>
      ) : null}
      <div
        className={cx(s.composerBox, drag && s.composerDrop)}
        hidden={!!p.takeover}
        onDragOver={(e) => {
          if (p.onAttach && e.dataTransfer.types.includes("Files")) {
            e.preventDefault();
            setDrag(true);
          }
        }}
        onDragLeave={() => setDrag(false)}
        onDrop={onDrop}
      >
        {menu && shown.length > 0 && (
          <SlashMenu items={shown} active={active} onHover={setActive} onPick={(i) => void pick(i)} />
        )}
        {(p.attachments?.length ?? 0) > 0 && (
          <div className={s.attachments}>
            {p.attachments!.map((a) => (
              <div key={a.id} className={cx(s.attach, a.state === "failed" && s.chip_failed)} title={a.error ?? a.name}>
                {a.preview ? <img src={a.preview} alt={a.name} /> : <Paperclip size={14} />}
                <span>{a.name}</span>
                {a.state === "uploading" && <Spinner size={12} />}
                {p.onRemoveAttachment && (
                  <button type="button" aria-label={`移除 ${a.name}`} onClick={() => p.onRemoveAttachment!(a.id)}>
                    <X size={12} />
                  </button>
                )}
              </div>
            ))}
          </div>
        )}
        <textarea
          ref={ta}
          className={s.editor}
          rows={1}
          value={p.value}
          disabled={p.disabled}
          placeholder={p.disabled ? p.disabledReason : (p.placeholder ?? (p.running ? "回合进行中：输入作为插话，下一步生效" : "给智能体发消息，/ 调出命令"))}
          onChange={(e) => p.onChange(e.target.value)}
          onKeyDown={onKey}
          onPaste={onPaste}
          aria-label="消息输入"
        />
        <div className={s.toolbar}>
          {(p.items?.length ?? 0) > 0 && (
            <Tooltip label="添加与命令" keybind="/">
              <IconButton label="添加与命令" size="sm" pressed={menu === "plus"} onClick={() => setMenu(menu === "plus" ? null : "plus")}>
                <Plus size={16} />
              </IconButton>
            </Tooltip>
          )}
          {p.onAttach && (
            <>
              <Tooltip label="添加文件或图片">
                <IconButton label="添加文件或图片" size="sm" onClick={() => fileRef.current?.click()}>
                  <Paperclip size={15} />
                </IconButton>
              </Tooltip>
              <input
                ref={fileRef}
                type="file"
                multiple
                hidden
                onChange={(e) => {
                  const f = [...(e.target.files ?? [])];
                  if (f.length) p.onAttach!(f);
                  e.target.value = "";
                }}
              />
            </>
          )}
          {p.toolbarLeft}
          <span className={s.grow} />
          {p.toolbarRight}
          {p.running && p.onStop && p.value.trim() === "" ? (
            <Tooltip label="停止" keybind="Esc Esc">
              <button type="button" className={cx(s.sendBtn, s.stopBtn)} aria-label="停止" onClick={p.onStop}>
                <Square size={12} fill="currentColor" />
              </button>
            </Tooltip>
          ) : (
            <Tooltip label={p.running ? "插话" : "发送"} keybind={p.sendKey === "mod-enter" ? "Mod+Enter" : "Enter"}>
              <button type="button" className={s.sendBtn} aria-label={p.running ? "插话" : "发送"} disabled={!canSend || uploading} onClick={p.onSend}>
                {p.busy ? <Spinner size={14} /> : <ArrowUp size={16} />}
              </button>
            </Tooltip>
          )}
        </div>
      </div>
      {p.below && <div className={s.below}>{p.below}</div>}
    </div>
  );
}

function SlashMenu({
  items,
  active,
  onHover,
  onPick,
}: {
  items: ComposerItem[];
  active: number;
  onHover: (i: number) => void;
  onPick: (i: ComposerItem) => void;
}) {
  const groups: [string, ComposerItem[]][] = [
    ["添加", items.filter((i) => i.group === "add")],
    ["命令", items.filter((i) => i.group === "command")],
  ];
  let n = -1;
  return (
    <div className={s.slash} role="listbox" aria-label="添加与命令">
      {groups.map(([label, xs]) =>
        xs.length === 0 ? null : (
          <div key={label}>
            <div className={s.slashGroup}>{label}</div>
            {xs.map((it) => {
              n++;
              const i = n;
              return (
                <button
                  key={it.id}
                  type="button"
                  role="option"
                  aria-selected={i === active}
                  data-active={i === active}
                  className={s.slashItem}
                  disabled={it.disabled}
                  title={it.disabled ? it.reason : undefined}
                  onMouseEnter={() => onHover(i)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => onPick(it)}
                >
                  <span className={s.slashIcon}>{it.icon}</span>
                  <span className={s.slashTitle}>{it.title}</span>
                  {it.description && <span className={s.slashDesc}>{it.description}</span>}
                  {it.slash && <span className={s.slashName}>/{it.slash}</span>}
                </button>
              );
            })}
          </div>
        ),
      )}
    </div>
  );
}
