// 对话框：交互（工具确认 approval、模块提问 question，06 10.6）与通用输入框。
// 交互来自快照的 interactions 与 interaction.opened 事件，回答经 interaction.respond（只收第一条）。

import { useState } from "react";
import type { Interaction } from "./client/wire.gen";
import { errText, pretty } from "./util";

export type Scope = "once" | "session";

export function ConfirmDialog(props: {
  it: Interaction;
  onDecide: (approve: boolean, scope: Scope) => Promise<void>;
}) {
  const { it } = props;
  const ap = it.approval;
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const decide = async (approve: boolean, scope: Scope) => {
    setBusy(true);
    setErr("");
    try {
      await props.onDecide(approve, scope);
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };
  const rules = ap?.session_rules ?? [];
  return (
    <div className="modal-back">
      <div className="modal">
        <h3>需要确认：{ap?.tool ?? it.title}</h3>
        <div className="muted">
          会话 {it.session}
          {ap?.reason ? ` · ${ap.reason}` : ""}
        </div>
        <pre className="args">{pretty(ap?.args)}</pre>
        {rules.length > 0 && (
          <>
            <div className="muted small">「本会话内允许」会生成规则：</div>
            <ul className="rules">
              {rules.map((r) => (
                <li key={r}>
                  <code>{r}</code>
                </li>
              ))}
            </ul>
          </>
        )}
        {(ap?.blocked ?? []).length > 0 && (
          <div className="warn small">不生成规则：{(ap?.blocked ?? []).join("、")}</div>
        )}
        <div className="row">
          <button disabled={busy} onClick={() => void decide(true, "once")}>
            允许一次
          </button>
          <button disabled={busy || rules.length === 0} onClick={() => void decide(true, "session")}>
            本会话内允许
          </button>
          <button disabled={busy} className="danger" onClick={() => void decide(false, "once")}>
            拒绝
          </button>
        </div>
        {err && <div className="error">{err}</div>}
      </div>
    </div>
  );
}

export function AskDialog(props: { it: Interaction; onAnswer: (text: string) => Promise<void> }) {
  const q = props.it.question;
  const [text, setText] = useState("");
  const [err, setErr] = useState("");
  const send = (t: string) => props.onAnswer(t).catch((e) => setErr(errText(e)));
  return (
    <div className="modal-back">
      <div className="modal">
        <h3>智能体提问</h3>
        <div className="question">{q?.text ?? props.it.title}</div>
        {q?.options && q.options.length > 0 && (
          <div className="col">
            {q.options.map((o) => (
              <button key={o} onClick={() => void send(o)}>
                {o}
              </button>
            ))}
          </div>
        )}
        <div className="row">
          <input
            autoFocus
            value={text}
            placeholder="自由回答"
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && text && void send(text)}
          />
          <button disabled={!text} onClick={() => void send(text)}>
            回答
          </button>
        </div>
        {err && <div className="error">{err}</div>}
      </div>
    </div>
  );
}

export interface PromptReq {
  title: string;
  label?: string;
  initial?: string;
  resolve: (v: string | null) => void;
}

export function PromptDialog({ req, onDone }: { req: PromptReq; onDone: () => void }) {
  const [v, setV] = useState(req.initial ?? "");
  const finish = (x: string | null) => {
    req.resolve(x);
    onDone();
  };
  return (
    <div className="modal-back">
      <div className="modal">
        <h3>{req.title}</h3>
        {req.label && <div className="muted">{req.label}</div>}
        <input
          autoFocus
          value={v}
          onChange={(e) => setV(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") finish(v);
            if (e.key === "Escape") finish(null);
          }}
        />
        <div className="row">
          <button onClick={() => finish(v)}>确定</button>
          <button onClick={() => finish(null)}>取消</button>
        </div>
      </div>
    </div>
  );
}
