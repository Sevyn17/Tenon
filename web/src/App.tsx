// 外壳：左侧会话列表，右侧聊天 / 档位 / 插件 / 统计；全局对话框（人工确认、ask_user、输入框）与提示条。

import { useCallback, useEffect, useState } from "react";
import { Chat } from "./Chat";
import { AskDialog, ConfirmDialog, PromptDialog, type ConfirmReq, type PromptReq } from "./Dialogs";
import { ModesPanel, PluginsPanel, SessionList, StatsPanel } from "./Panels";
import { EV, PROTOCOL, type AskEvent, type HelloResult, type HumanRequestedEvent, type NoticeEvent } from "./protocol";
import { call, onEvent } from "./transport";
import { errText, newSessionID } from "./util";

type Tab = "chat" | "modes" | "plugins" | "stats";

interface Notice {
  id: number;
  text: string;
}

let noticeSeq = 0;

export function App() {
  const [hello, setHello] = useState<HelloResult | null>(null);
  const [err, setErr] = useState("");
  const [session, setSession] = useState("default");
  const [tab, setTab] = useState<Tab>("chat");
  const [refresh, setRefresh] = useState(0);
  const [confirms, setConfirms] = useState<ConfirmReq[]>([]);
  const [asks, setAsks] = useState<AskEvent[]>([]);
  const [prompt, setPrompt] = useState<PromptReq | null>(null);
  const [notices, setNotices] = useState<Notice[]>([]);

  const notice = useCallback((text: string) => {
    const id = ++noticeSeq;
    setNotices((ns) => [...ns.slice(-4), { id, text }]);
    setTimeout(() => setNotices((ns) => ns.filter((n) => n.id !== id)), 8000);
  }, []);

  useEffect(() => {
    call("hello", { client: "ybase-web", version: PROTOCOL })
      .then(setHello)
      .catch((e) => setErr(errText(e)));
  }, []);

  useEffect(
    () =>
      onEvent((ev) => {
        switch (ev.kind) {
          case EV.humanRequested: {
            const d = ev.data as HumanRequestedEvent;
            setConfirms((cs) => (cs.some((c) => c.call_id === d.call_id) ? cs : [...cs, { ...d, session: ev.session }]));
            break;
          }
          case EV.ask:
            setAsks((as) => [...as, ev.data as AskEvent]);
            break;
          case EV.notice:
            notice((ev.session ? `[${ev.session}] ` : "") + (ev.data as NoticeEvent).text);
            break;
          case EV.turnStart:
          case EV.turnEnd:
            setRefresh((r) => r + 1);
            break;
        }
      }),
    [notice],
  );

  const ask = (title: string, label?: string, initial?: string) =>
    new Promise<string | null>((resolve) => setPrompt({ title, label, initial, resolve }));

  const newSession = async () => {
    const v = await ask("新会话", "会话名（字母、数字、连字符）", newSessionID());
    if (!v) return;
    setSession(v.trim());
    setTab("chat");
    setRefresh((r) => r + 1);
  };

  const confirm = confirms[0];
  const askItem = asks[0];

  return (
    <div className="app">
      <aside className="side">
        <div className="brand">
          YBase
          {hello && <div className="muted small">{hello.model} · {hello.trusted ? "受信项目" : "未受信项目"}</div>}
          {hello && <div className="muted small path" title={hello.workdir}>{hello.workdir}</div>}
        </div>
        <SessionList
          current={session}
          refresh={refresh}
          onPick={(id) => {
            setSession(id);
            setTab("chat");
          }}
          onNew={() => void newSession()}
        />
      </aside>
      <main className="main">
        <nav className="tabs">
          {(
            [
              ["chat", "对话"],
              ["modes", "权限档位"],
              ["plugins", "插件"],
              ["stats", "统计"],
            ] as [Tab, string][]
          ).map(([t, l]) => (
            <button key={t} className={tab === t ? "on" : ""} onClick={() => setTab(t)}>
              {l}
            </button>
          ))}
          <span className="spacer" />
          <span className="muted">会话 {session}</span>
        </nav>
        {err && <div className="error">连接失败：{err}</div>}
        <section className="content">
          {tab === "chat" && (
            <Chat
              key={session}
              session={session}
              ask={ask}
              onNotice={notice}
              onForked={(id) => {
                setSession(id);
                setRefresh((r) => r + 1);
              }}
            />
          )}
          {tab === "modes" && <ModesPanel session={session} />}
          {tab === "plugins" && <PluginsPanel />}
          {tab === "stats" && <StatsPanel session={session} />}
        </section>
      </main>
      <div className="notices">
        {notices.map((n) => (
          <div key={n.id} className="notice">
            {n.text}
          </div>
        ))}
      </div>
      {confirm && (
        <ConfirmDialog
          key={confirm.call_id}
          req={confirm}
          onDecide={async (decision) => {
            const r = await call("session.confirm", {
              session: confirm.session,
              call_id: confirm.call_id,
              decision,
            });
            return r.rules;
          }}
          onClose={() => setConfirms((cs) => cs.filter((c) => c.call_id !== confirm.call_id))}
        />
      )}
      {!confirm && askItem && (
        <AskDialog
          key={askItem.ask_id}
          ask={askItem}
          onAnswer={async (text) => {
            await call("session.answer", { ask_id: askItem.ask_id, text });
            setAsks((as) => as.filter((a) => a.ask_id !== askItem.ask_id));
          }}
        />
      )}
      {prompt && <PromptDialog req={prompt} onDone={() => setPrompt(null)} />}
    </div>
  );
}
