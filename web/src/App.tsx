// 外壳：左侧会话列表，右侧聊天 / 档位 / 插件 / 统计；全局对话框（交互：工具确认与模块提问、输入框）与提示条。
//
// 交互（06 10.6）：启动时 interactions.list 取当前身份可以回答的全部未结束交互，之后按订阅会话上的
// interaction.opened / interaction.resolved 更新；子会话的交互也写在根会话上，订阅根会话即可看到。
// 重连后重新取一次列表。

import { useCallback, useEffect, useState } from "react";
import { LOCAL_RECONNECTED } from "./client/commands";
import { Chat } from "./Chat";
import { AskDialog, ConfirmDialog, PromptDialog, type PromptReq } from "./Dialogs";
import { ModesPanel, PluginsPanel, SessionList, StatsPanel } from "./Panels";
import { call, clientID, onEvent } from "./transport";
import { errText, newSessionID } from "./util";
import {
  PROTOCOL,
  type HelloResult,
  type Interaction,
  type InteractionResolved,
  type Notice as NoticeData,
} from "./client/wire.gen";

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
  const [pending, setPending] = useState<Interaction[]>([]);
  const [prompt, setPrompt] = useState<PromptReq | null>(null);
  const [notices, setNotices] = useState<Notice[]>([]);

  const notice = useCallback((text: string) => {
    const id = ++noticeSeq;
    setNotices((ns) => [...ns.slice(-4), { id, text }]);
    setTimeout(() => setNotices((ns) => ns.filter((n) => n.id !== id)), 8000);
  }, []);

  const loadInteractions = useCallback(async () => {
    try {
      const r = await call("interactions.list", {});
      setPending(r.items ?? []);
    } catch (e) {
      notice("读取待处理交互失败：" + errText(e));
    }
  }, [notice]);

  useEffect(() => {
    call("hello", { client: "ybase-web", protocol: { min: PROTOCOL, max: PROTOCOL } })
      .then((h) => {
        setHello(h);
        void loadInteractions();
      })
      .catch((e) => setErr(errText(e)));
  }, [loadInteractions]);

  useEffect(
    () =>
      onEvent((ev) => {
        switch (ev.kind) {
          case LOCAL_RECONNECTED:
            void loadInteractions();
            setRefresh((r) => r + 1);
            break;
          case "interaction.opened": {
            const it = ev.data as Interaction;
            setPending((ps) => (ps.some((p) => p.id === it.id) ? ps : [...ps, it]));
            break;
          }
          case "interaction.resolved": {
            const d = ev.data as InteractionResolved;
            setPending((ps) => ps.filter((p) => p.id !== d.id));
            break;
          }
          case "notice": {
            const d = ev.data as NoticeData;
            notice((ev.session ? `[${ev.session}] ` : "") + d.text);
            break;
          }
          case "sessions.changed":
          case "turn.started":
          case "turn.ended":
            setRefresh((r) => r + 1);
            break;
        }
      }),
    [notice, loadInteractions],
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

  // 回答交互：已被别处回答（conflict answered）或已失效时从列表里去掉，不算错误。
  const respond = async (it: Interaction, response: { approve?: boolean; scope?: string; text?: string }) => {
    try {
      await call("interaction.respond", { interaction: it.id, response, client_id: clientID() });
    } catch (e) {
      const ae = e as { code?: string; reason?: string };
      if (!(ae.code === "conflict" && ae.reason === "answered") && ae.code !== "not_found") throw e;
      notice("这个交互已在别处处理");
    } finally {
      setPending((ps) => ps.filter((p) => p.id !== it.id));
    }
  };

  const current = pending[0];

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
          {pending.length > 1 && <span className="tag">待处理 {pending.length}</span>}
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
      {current && current.kind === "approval" && (
        <ConfirmDialog
          key={current.id}
          it={current}
          onDecide={(approve, scope) => respond(current, { approve, scope })}
        />
      )}
      {current && current.kind === "question" && (
        <AskDialog key={current.id} it={current} onAnswer={(text) => respond(current, { text })} />
      )}
      {prompt && <PromptDialog req={prompt} onDone={() => setPrompt(null)} />}
    </div>
  );
}
