// 工作台的导航状态：当前会话、主区页面、设置覆盖层、命令面板。可订阅（区域按它重渲染），
// 当前会话写进地址栏的 #s=<id>，刷新后回到同一会话。

import { createStore, type Store } from "../client";

export interface NavState {
  session: string | null;
  /** 主区页面（MainPage id）；null = 对话。 */
  page: string | null;
  /** 设置覆盖层：null = 关闭；否则是打开的页面与要定位的条目。 */
  settings: { page: string; entry?: string } | null;
  /** 命令面板：null = 关闭。 */
  palette: "commands" | "sessions" | null;
  /** 快捷键速查。 */
  shortcuts: boolean;
  /** 窄屏（sm）下左栏抽屉是否打开。 */
  drawer: boolean;
  /** 专注模式：隐藏左栏与面板区。 */
  focus: boolean;
}

/** hash 为假时不读写地址栏（嵌入到别人页面里时用）。 */
export function createNav(initial?: string | null, opts: { hash?: boolean } = {}): Store<NavState> {
  const hash = opts.hash !== false;
  if (initial === undefined) initial = hash ? readHash() : null;
  const nav = createStore<NavState>({ session: initial, page: null, settings: null, palette: null, shortcuts: false, focus: false, drawer: false });
  let last = initial;
  nav.subscribe((v) => {
    if (!hash || v.session === last) return;
    last = v.session;
    try {
      const h = v.session ? `#s=${encodeURIComponent(v.session)}` : "";
      if (location.hash !== h) history.replaceState(null, "", location.pathname + location.search + h);
    } catch {
      /* 非浏览器环境 */
    }
  });
  return nav;
}

function readHash(): string | null {
  try {
    const m = /(?:^#|&)s=([^&]+)/.exec(location.hash);
    return m ? decodeURIComponent(m[1]) : null;
  } catch {
    return null;
  }
}
