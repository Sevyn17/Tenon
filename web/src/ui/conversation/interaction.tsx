// 交互卡（前端方案第十节）："智能体在等人回答"。按 kind 渲染：approval 工具确认、question 提问、choice 选择
// （单选 / 多选）、form 表单（按 schema 生成）、confirm 是否继续；不认识的 kind 用通用卡（标题 + 原始内容 + 自由回答），不会卡死。
//
// 键盘：Enter 允许 / 提交、Esc 拒绝、a 本会话允许、数字键选选项；焦点在输入控件里时这些键不被截走。
// 结束后（answered / expired / voided / 被别处回答）卡片变只读并说明原因，不出现点了没反应的按钮。

import { useEffect, useRef, useState, type ReactNode } from "react";
import { Check, CircleHelp, ClipboardList, ListChecks, ShieldAlert, TriangleAlert, X } from "lucide-react";
import { Button, Kbd, Textarea } from "../controls";
import { CodeBlock } from "../blocks";
import { SchemaForm, type FormValue, type JSONSchema } from "../form";
import { cx } from "../cx";
import s from "./conversation.module.css";
import type { UIInteraction, UIResponse } from "./types";

export interface InteractionDone {
  outcome: string; // approved / denied / answered / expired / voided
  by?: string;
}

export interface InteractionCardProps {
  it: UIInteraction;
  /** 回答；返回后卡片进入"已提交"，结束以 done 为准。抛错时卡片恢复可操作并显示错误。 */
  onRespond: (r: UIResponse) => Promise<void>;
  /** 交互已结束（本端或别处回答、过期、作废）。 */
  done?: InteractionDone;
  /** 拒绝时附的说明（作为插话发给智能体）；没给时不显示"拒绝并说明"。 */
  onDenyWithReason?: (text: string) => Promise<void>;
  /** 第 i / n 个（同时有多个未结束交互时）。 */
  position?: { index: number; total: number; prev?: () => void; next?: () => void };
  /** 来源面包屑（子会话的交互："主会话 › reviewer"）。 */
  source?: ReactNode;
  /** 插件替换某个 kind 的正文（InteractionRenderer）。 */
  body?: ReactNode;
  /** 是否抢键盘（卡片接管输入框时为真；消息流里的只读记录为假）。 */
  keyboard?: boolean;
}

const KIND_ICON: Record<string, ReactNode> = {
  approval: <ShieldAlert size={16} />,
  question: <CircleHelp size={16} />,
  choice: <ListChecks size={16} />,
  form: <ClipboardList size={16} />,
  confirm: <TriangleAlert size={16} />,
};

const OUTCOME_TEXT: Record<string, string> = {
  approved: "已允许",
  denied: "已拒绝",
  answered: "已回答",
  expired: "已过期",
  voided: "已作废（回合已取消或会话已重启）",
};

function prettyArgs(v: unknown): string {
  if (v === undefined) return "";
  if (typeof v === "string") {
    try {
      return JSON.stringify(JSON.parse(v), null, 2);
    } catch {
      return v;
    }
  }
  return JSON.stringify(v, null, 2);
}

/** 截止倒计时：剩余秒数；没有截止返回 null。 */
function useCountdown(deadline?: number): number | null {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!deadline) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [deadline]);
  if (!deadline) return null;
  return Math.max(0, Math.round((deadline - now) / 1000));
}

function fmtLeft(sec: number): string {
  if (sec >= 3600) return `${Math.floor(sec / 3600)} 小时`;
  if (sec >= 60) return `${Math.floor(sec / 60)} 分 ${sec % 60} 秒`;
  return `${sec} 秒`;
}

export function InteractionCard(p: InteractionCardProps) {
  const { it } = p;
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [text, setText] = useState("");
  const [denying, setDenying] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  // 表单初值取 schema 的 default：界面显示的值就是会提交的值（不然开关显示为开、提交时却没有这个键）。
  const [form, setForm] = useState<FormValue>(() => {
    const props = (it.schema as JSONSchema | undefined)?.properties ?? {};
    return Object.fromEntries(Object.entries(props).filter(([, v]) => v.default !== undefined).map(([k, v]) => [k, v.default]));
  });
  const left = useCountdown(it.deadline);
  const root = useRef<HTMLDivElement>(null);
  const done = p.done;
  const live = !done && !busy;

  const answer = async (r: UIResponse) => {
    if (!live) return;
    setBusy(true);
    setErr("");
    try {
      await p.onRespond(r);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const denyWithReason = async () => {
    if (!p.onDenyWithReason) return;
    await answer({ approve: false });
    if (text.trim()) await p.onDenyWithReason(text.trim()).catch(() => {});
  };

  // 键盘：只在卡片接管输入框时抢键；焦点在输入控件里时不截走 Enter / 数字。
  useEffect(() => {
    if (!p.keyboard || done) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      const typing = t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable);
      if (it.kind === "approval") {
        if (typing && e.key !== "Escape") return;
        if (e.key === "Enter") {
          e.preventDefault();
          void answer({ approve: true, scope: "once" });
        } else if (e.key === "Escape") {
          e.preventDefault();
          void answer({ approve: false });
        } else if (e.key === "a" && it.approval?.session_rules?.length) {
          e.preventDefault();
          void answer({ approve: true, scope: "session" });
        }
      } else if (it.kind === "question" && !typing) {
        const opts = it.question?.options ?? [];
        const n = Number(e.key);
        if (n >= 1 && n <= opts.length) {
          e.preventDefault();
          void answer({ text: opts[n - 1] });
        }
      } else if (it.kind === "choice" && !typing && !it.choice?.multiple) {
        const opts = it.choice?.options ?? [];
        const n = Number(e.key);
        if (n >= 1 && n <= opts.length) {
          e.preventDefault();
          void answer({ value: opts[n - 1].id });
        }
      } else if (it.kind === "confirm" && !typing) {
        if (e.key === "Enter") {
          e.preventDefault();
          void answer({ approve: true });
        } else if (e.key === "Escape") {
          e.preventDefault();
          void answer({ approve: false });
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.keyboard, done, it.id, busy]);

  const head = (
    <div className={s.icardHead}>
      <span className={s.icardHeadIcon}>{KIND_ICON[it.kind] ?? <CircleHelp size={16} />}</span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div>{it.kind === "approval" ? `需要确认：${it.approval?.tool ?? it.title}` : it.title}</div>
        <div className={s.icardSub}>
          {p.source}
          {left !== null && !done && <span>{left > 0 ? `剩余 ${fmtLeft(left)}` : "已到截止时间"}</span>}
        </div>
      </div>
      {p.position && p.position.total > 1 && (
        <div className={s.icardMeta}>
          <Button size="sm" variant="ghost" disabled={!p.position.prev} onClick={p.position.prev}>
            ‹
          </Button>
          <span>
            {p.position.index + 1} / {p.position.total}
          </span>
          <Button size="sm" variant="ghost" disabled={!p.position.next} onClick={p.position.next}>
            ›
          </Button>
        </div>
      )}
    </div>
  );

  let body: ReactNode = p.body;
  let actions: ReactNode = null;

  if (!body && it.kind === "approval") {
    const ap = it.approval;
    body = (
      <>
        {ap?.reason && <div className={s.icardNote}>{ap.reason}</div>}
        {ap?.args !== undefined && <CodeBlock code={prettyArgs(ap.args)} lang="json" />}
        {ap?.session_rules && ap.session_rules.length > 0 && (
          <div>
            <div className={s.detailLabel}>"本会话内允许"会生成规则</div>
            <ul className={s.rules}>
              {ap.session_rules.map((r) => (
                <li key={r}>
                  <code>{r}</code>
                </li>
              ))}
            </ul>
          </div>
        )}
        {ap?.blocked && ap.blocked.length > 0 && (
          <div className={s.icardNote}>以下命令不会生成规则，每次仍会询问：{ap.blocked.join("、")}</div>
        )}
        {denying && (
          <Textarea
            autoFocus
            rows={2}
            value={text}
            placeholder="告诉智能体为什么拒绝（会作为插话发送）"
            aria-label="拒绝的理由"
            onChange={(e) => setText(e.target.value)}
          />
        )}
      </>
    );
    actions = denying ? (
      <>
        <Button variant="danger" disabled={!live} onClick={() => void denyWithReason()}>
          拒绝并发送说明
        </Button>
        <Button variant="ghost" disabled={!live} onClick={() => setDenying(false)}>
          返回
        </Button>
      </>
    ) : (
      <>
        <Button variant="primary" disabled={!live} icon={<Check size={14} />} onClick={() => void answer({ approve: true, scope: "once" })}>
          允许一次 <Kbd bind="Enter" />
        </Button>
        {ap?.session_rules && ap.session_rules.length > 0 && (
          <Button disabled={!live} onClick={() => void answer({ approve: true, scope: "session" })}>
            本会话内允许 <Kbd bind="a" />
          </Button>
        )}
        <span style={{ flex: 1 }} />
        {p.onDenyWithReason && (
          <Button variant="ghost" disabled={!live} onClick={() => setDenying(true)}>
            拒绝并说明
          </Button>
        )}
        <Button variant="danger" disabled={!live} icon={<X size={14} />} onClick={() => void answer({ approve: false })}>
          拒绝 <Kbd bind="Esc" />
        </Button>
      </>
    );
  } else if (!body && it.kind === "question") {
    const q = it.question;
    const opts = q?.options ?? [];
    body = (
      <>
        {q?.text && q.text !== it.title && <div>{q.text}</div>}
        {opts.length > 0 && (
          <div className={s.options}>
            {opts.map((o, i) => (
              <button key={o} type="button" className={s.option} disabled={!live} onClick={() => void answer({ text: o })}>
                <span className={s.optionKey}>{i + 1}</span>
                {o}
              </button>
            ))}
          </div>
        )}
        <Textarea
          rows={2}
          value={text}
          disabled={!live}
          placeholder={opts.length ? "或自由回答（Enter 提交）" : "输入回答（Enter 提交，Shift+Enter 换行）"}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing && text.trim()) {
              e.preventDefault();
              void answer({ text: text.trim() });
            }
          }}
        />
      </>
    );
    actions = (
      <Button variant="primary" disabled={!live || !text.trim()} onClick={() => void answer({ text: text.trim() })}>
        提交
      </Button>
    );
  } else if (!body && it.kind === "choice" && it.choice) {
    const c = it.choice;
    const multi = !!c.multiple;
    const toggle = (id: string) => setPicked((xs) => (xs.includes(id) ? xs.filter((x) => x !== id) : [...xs, id]));
    const tooFew = multi && (c.min ?? 1) > picked.length;
    const tooMany = multi && !!c.max && picked.length > c.max;
    body = (
      <>
        {c.text && c.text !== it.title && <div>{c.text}</div>}
        <div className={s.options} role={multi ? "group" : "radiogroup"} aria-label={it.title}>
          {c.options.map((o, i) => {
            const on = picked.includes(o.id);
            return (
              <button
                key={o.id}
                type="button"
                className={s.option}
                role={multi ? "checkbox" : "radio"}
                aria-checked={multi ? on : undefined}
                data-on={on || undefined}
                disabled={!live}
                onClick={() => (multi ? toggle(o.id) : void answer({ value: o.id }))}
              >
                <span className={s.optionKey}>{multi ? (on ? "✓" : "") : i + 1}</span>
                <span>
                  {o.label}
                  {o.description && <span className={s.optionDesc}>{o.description}</span>}
                </span>
              </button>
            );
          })}
        </div>
        {multi && (c.min || c.max) ? (
          <div className={s.icardNote}>
            {c.min && c.max ? `选 ${c.min}–${c.max} 项` : c.min ? `至少选 ${c.min} 项` : `最多选 ${c.max} 项`}
          </div>
        ) : null}
      </>
    );
    actions = multi ? (
      <Button variant="primary" disabled={!live || tooFew || tooMany} onClick={() => void answer({ value: picked })}>
        确定（{picked.length}）
      </Button>
    ) : null;
  } else if (!body && it.kind === "form") {
    const sc = (it.schema ?? { type: "object", properties: {} }) as JSONSchema;
    const missing = (sc.required ?? []).filter((k) => form[k] === undefined || form[k] === "");
    body = (
      <>
        {it.form?.text && <div>{it.form.text}</div>}
        <SchemaForm schema={sc} value={form} onChange={setForm} readOnly={!live} />
        {missing.length > 0 && <div className={s.icardNote}>还没填：{missing.map((k) => sc.properties?.[k]?.title ?? k).join("、")}</div>}
      </>
    );
    actions = (
      <Button variant="primary" disabled={!live || missing.length > 0} onClick={() => void answer({ value: form })}>
        {it.form?.submit || "提交"}
      </Button>
    );
  } else if (!body && it.kind === "confirm") {
    const c = it.confirm;
    body = c?.text && c.text !== it.title ? <div>{c.text}</div> : null;
    actions = (
      <>
        <Button variant={c?.danger ? "danger" : "primary"} disabled={!live} icon={<Check size={14} />} onClick={() => void answer({ approve: true })}>
          {c?.confirm || "继续"} <Kbd bind="Enter" />
        </Button>
        <Button variant="ghost" disabled={!live} onClick={() => void answer({ approve: false })}>
          {c?.cancel || "取消"} <Kbd bind="Esc" />
        </Button>
      </>
    );
  } else if (!body) {
    // 通用卡：不认识的 kind，显示原始内容 + 自由文本回答。
    body = (
      <>
        <div className={s.icardNote}>这个界面不认识交互种类 "{it.kind}"，下面是原始内容。</div>
        <CodeBlock code={JSON.stringify(it, null, 2)} lang="json" />
        <Textarea rows={2} value={text} disabled={!live} placeholder="回答" onChange={(e) => setText(e.target.value)} />
      </>
    );
    actions = (
      <Button variant="primary" disabled={!live || !text.trim()} onClick={() => void answer({ text: text.trim() })}>
        提交
      </Button>
    );
  }

  return (
    <div
      ref={root}
      className={cx(s.icard, (it.kind === "approval" || (it.kind === "confirm" && it.confirm?.danger)) && s.icard_approval, done && s.icardDone)}
      role="group"
      aria-label={it.title}
      data-interaction={it.id}
    >
      {head}
      <div className={s.icardBody}>{body}</div>
      {err && <div className={s.icardNote} data-tone="danger">{err}</div>}
      {done ? (
        <div className={s.icardActions}>
          <span className={s.icardNote}>
            {OUTCOME_TEXT[done.outcome] ?? done.outcome}
            {done.by ? `（由 ${done.by} 处理）` : ""}
          </span>
        </div>
      ) : (
        <div className={s.icardActions}>{actions}</div>
      )}
    </div>
  );
}

/** 交互结束后在消息流里留下的一行只读记录（"你允许了 write_file"）。 */
export function InteractionRecord({ it, done }: { it: UIInteraction; done: InteractionDone }) {
  const what = it.kind === "approval" ? it.approval?.tool ?? it.title : it.title;
  return (
    <div className={s.statusLine} data-interaction-record={it.id}>
      {OUTCOME_TEXT[done.outcome] ?? done.outcome}：{what}
      {done.by ? ` · ${done.by}` : ""}
    </div>
  );
}
