// 对话框：人工确认（human_requested）、ask_user 提问、通用输入框。

import { useState } from "react";
import type { AskEvent, Decision, HumanRequestedEvent } from "./protocol";
import { pretty } from "./util";

export interface ConfirmReq extends HumanRequestedEvent {
  session: string;
}

export function ConfirmDialog(props: {
  req: ConfirmReq;
  onDecide: (d: Decision) => Promise<string[] | undefined>;
  onClose: () => void;
}) {
  const { req } = props;
  const [busy, setBusy] = useState(false);
  const [rules, setRules] = useState<string[] | null>(null);
  const [err, setErr] = useState("");
  const decide = async (d: Decision) => {
    setBusy(true);
    setErr("");
    try {
      const r = await props.onDecide(d);
      if (d === "session" && r && r.length) setRules(r);
      else props.onClose();
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="modal-back">
      <div className="modal">
        <h3>需要确认：{req.tool}</h3>
        <div className="muted">会话 {req.session}{req.reason ? ` · ${req.reason}` : ""}</div>
        <pre className="args">{pretty(req.args)}</pre>
        {rules ? (
          <>
            <div>已为本会话生成允许规则：</div>
            <ul className="rules">
              {rules.map((r) => (
                <li key={r}>
                  <code>{r}</code>
                </li>
              ))}
            </ul>
            <div className="row">
              <button onClick={props.onClose}>关闭</button>
            </div>
          </>
        ) : (
          <div className="row">
            <button disabled={busy} onClick={() => void decide("once")}>
              允许一次
            </button>
            <button disabled={busy} onClick={() => void decide("session")}>
              本会话内允许
            </button>
            <button disabled={busy} className="danger" onClick={() => void decide("deny")}>
              拒绝
            </button>
          </div>
        )}
        {err && <div className="error">{err}</div>}
      </div>
    </div>
  );
}

export function AskDialog(props: { ask: AskEvent; onAnswer: (text: string) => Promise<void> }) {
  const [text, setText] = useState("");
  const [err, setErr] = useState("");
  const send = (t: string) => props.onAnswer(t).catch((e) => setErr(String(e instanceof Error ? e.message : e)));
  return (
    <div className="modal-back">
      <div className="modal">
        <h3>智能体提问</h3>
        <div className="question">{props.ask.question}</div>
        {props.ask.options && props.ask.options.length > 0 && (
          <div className="col">
            {props.ask.options.map((o) => (
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
