// 文案（前端方案第二节）：工作台的界面文字全部走 t(key, vars)。中 / 英两份词表；嵌入方用 createShell 的 messages
// 覆盖或补充任意键。缺键时依次退回中文词表、键名本身（不会显示空白）。

export type Locale = "zh" | "en";
export type Messages = Record<string, string>;

const zh: Messages = {
  "app.name": "YBase",
  "nav.new": "新建会话",
  "nav.search": "搜索",
  "nav.market": "插件市场",
  "sessions.pinned": "置顶",
  "sessions.today": "今天",
  "sessions.yesterday": "昨天",
  "sessions.week": "7 天内",
  "sessions.older": "更早",
  "sessions.empty": "还没有会话",
  "sessions.more": "加载更多",
  "sessions.children": "含子会话",
  "conv.empty.title": "今天想做点什么？",
  "conv.empty.hint": "描述你的任务，智能体会读写工作目录里的文件、运行命令。",
  "conv.placeholder": "给智能体发消息，/ 调出命令，@ 引用",
  "conv.placeholder.running": "回合进行中：输入作为插话，下一步生效",
  "conv.notfound": "会话不存在",
  "conv.closed": "会话已关闭（{reason}）",
  "conv.reconnecting": "连接中断，正在重连…",
  "panel.toggle": "面板",
  "panel.empty": "从右侧竖栏打开面板",
  "panel.needsSession": "先选择一个会话",
  "avatar.settings": "设置",
  "avatar.shortcuts": "快捷键",
  "avatar.theme": "主题",
  "avatar.about": "关于",
  "settings.title": "设置",
  "settings.search": "搜索设置",
  "palette.placeholder": "输入命令或会话名…",
  "palette.empty": "没有匹配的命令",
  "common.cancel": "取消",
  "common.confirm": "确定",
  "common.close": "关闭",
  "common.retry": "重试",
  "common.unavailable": "不可用：{reason}",
  "pack.failed": "{pack} 出错：{error}",
};

const en: Messages = {
  "app.name": "YBase",
  "nav.new": "New session",
  "nav.search": "Search",
  "nav.market": "Plugin market",
  "sessions.pinned": "Pinned",
  "sessions.today": "Today",
  "sessions.yesterday": "Yesterday",
  "sessions.week": "Previous 7 days",
  "sessions.older": "Older",
  "sessions.empty": "No sessions yet",
  "sessions.more": "Load more",
  "sessions.children": "Include sub-sessions",
  "conv.empty.title": "What are we working on?",
  "conv.empty.hint": "Describe a task. The agent can read and write files in the workspace and run commands.",
  "conv.placeholder": "Message the agent, / for commands, @ to reference",
  "conv.placeholder.running": "Turn in progress: your message will steer the next step",
  "conv.notfound": "Session not found",
  "conv.closed": "Session closed ({reason})",
  "conv.reconnecting": "Connection lost, reconnecting…",
  "panel.toggle": "Panels",
  "panel.empty": "Open a panel from the rail",
  "panel.needsSession": "Select a session first",
  "avatar.settings": "Settings",
  "avatar.shortcuts": "Keyboard shortcuts",
  "avatar.theme": "Theme",
  "avatar.about": "About",
  "settings.title": "Settings",
  "settings.search": "Search settings",
  "palette.placeholder": "Type a command or session name…",
  "palette.empty": "No matching commands",
  "common.cancel": "Cancel",
  "common.confirm": "OK",
  "common.close": "Close",
  "common.retry": "Retry",
  "common.unavailable": "Unavailable: {reason}",
  "pack.failed": "{pack} failed: {error}",
};

export const BUILTIN_MESSAGES: Record<Locale, Messages> = { zh, en };

export type T = (key: string, vars?: Record<string, string | number>) => string;

export function createT(locale: Locale, extra: Partial<Record<Locale, Messages>> = {}): T {
  const table = { ...BUILTIN_MESSAGES[locale], ...(extra[locale] ?? {}) };
  const fallback = { ...zh, ...(extra.zh ?? {}) };
  return (key, vars) => {
    let s = table[key] ?? fallback[key] ?? key;
    if (vars) for (const [k, v] of Object.entries(vars)) s = s.split(`{${k}}`).join(String(v));
    return s;
  };
}
