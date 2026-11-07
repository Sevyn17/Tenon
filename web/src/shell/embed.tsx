// 单会话嵌入（前端方案第一节"嵌入形态"）：<TenonConversation session="…" /> 只有对话区、输入框与交互卡，
// 面板以浮层打开（embed 布局预设：没有左栏、停靠区与竖栏）。和完整工作台用同一套贡献包。
// 不读写地址栏；存储用独立的命名空间（缺省 "tenon.embed"），不影响同一站点上完整工作台的偏好与布局。

import { useEffect, useMemo } from "react";
import type { Client } from "../client";
import { createShell, type ShellOptions } from "./index";

export interface TenonConversationProps extends Omit<ShellOptions, "session" | "hash" | "preset"> {
  /** 要显示的会话；不存在时新建。 */
  session: string;
  client?: Client;
  /** 外框的 class（嵌入方给宽高）。 */
  className?: string;
}

export function TenonConversation({ session, className, ...o }: TenonConversationProps) {
  // 工作台实例只建一次：会话换了只改导航，不重建客户端与注册表。
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const shell = useMemo(() => createShell({ ns: "tenon.embed", ...o, session, hash: false, preset: "embed" }), []);
  useEffect(() => {
    if (shell.state.nav.get().session !== session) shell.state.api.openSession(session);
  }, [shell, session]);
  const Root = shell.Root;
  return (
    <div className={className} data-tenon-embed>
      <Root />
    </div>
  );
}
