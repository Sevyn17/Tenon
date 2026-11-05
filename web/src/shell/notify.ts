// 后台运行感知（前端方案第五节"通知中心"、第十四节）：回合完成、需要确认时记一条通知；
// 窗口在后台时（或偏好为 always）发系统通知；标题栏显示待处理数。只看客户端 store，不另订阅协议事件。

import type { ShellState } from "./context";

export function watchActivity(sh: ShellState): () => void {
  const base = document.title || sh.brand.name;
  const running = new Map<string, boolean>();
  for (const x of sh.client.sessions.get().items) running.set(x.id, x.run_state === "running");
  let seen = new Set(sh.client.interactions.get().open.map((x) => x.id));

  const system = (title: string, body: string | undefined, session: string) => {
    const mode = sh.prefs.get().notify;
    if (mode === "never" || typeof Notification === "undefined") return;
    if (mode === "background" && !document.hidden) return;
    const fire = () => {
      const n = new Notification(title, { body, tag: `ybase:${session}` });
      n.onclick = () => {
        window.focus();
        sh.api.openSession(session);
        n.close();
      };
    };
    if (Notification.permission === "granted") fire();
    else if (Notification.permission === "default") void Notification.requestPermission().then((p) => p === "granted" && fire());
  };

  // 在前台且正看着这个会话时不打扰（只记进通知中心）。
  const quiet = (session: string) => !document.hidden && sh.nav.get().session === session;

  const offSessions = sh.client.sessions.subscribe((v) => {
    for (const x of v.items) {
      const now = x.run_state === "running";
      const was = running.get(x.id);
      running.set(x.id, now);
      if (was && !now) {
        const title = `回合完成：${x.title || x.id}`;
        sh.notices.add({ title, body: x.preview || undefined, tone: "success", session: x.id }, quiet(x.id));
        system(title, x.preview || undefined, x.id);
      }
    }
  });

  const offInter = sh.client.interactions.subscribe((v) => {
    const ids = new Set(v.open.map((x) => x.id));
    for (const it of v.open) {
      if (seen.has(it.id)) continue;
      const root = it.session.split("/")[0];
      const title = it.kind === "approval" ? `需要确认：${it.approval?.tool ?? it.title}` : `等你回答：${it.question?.text ?? it.title}`;
      sh.notices.add({ title, tone: "warning", session: root }, quiet(root));
      system(title, it.approval?.reason, root);
    }
    seen = ids;
    document.title = v.open.length ? `(${v.open.length}) ${base}` : base;
  });

  return () => {
    offSessions();
    offInter();
    document.title = base;
  };
}
