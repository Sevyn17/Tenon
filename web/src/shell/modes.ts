// 切换权限档位（输入框工具栏与 设置 › 权限档位 共用）。项目档位放宽了内置档位时后端回 need_confirm：
// 先问用户，同意后带 confirm 再设一次。

import { ApiError, type Client, type ModeInfo } from "../client";
import type { ShellApi } from "./contributions";

/** 项目档位要先确认（说明它放宽了什么）。返回 false = 用户取消。 */
export function confirmProjectMode(api: ShellApi, m: ModeInfo, detail?: string): Promise<boolean> {
  return api.confirm({
    title: `切换到 ${m.label}`,
    body: `这是项目档位，会放宽：${(m.widened ?? []).join("、") || detail || "内置档位的限制"}`,
    confirm: "切换",
    danger: true,
  });
}

/** 设置会话档位。返回 true = 已切换；false = 用户取消确认。失败抛错。 */
export async function setSessionMode(client: Client, api: ShellApi, session: string, m: ModeInfo, confirmed = false): Promise<boolean> {
  try {
    await client.call("mode.set", { session, mode: m.id, confirm: confirmed || undefined });
    return true;
  } catch (e) {
    if (!(e instanceof ApiError && e.code === "need_confirm") || confirmed) throw e;
    if (!(await confirmProjectMode(api, m, e.message))) return false;
    await client.call("mode.set", { session, mode: m.id, confirm: true });
    return true;
  }
}

/** 档位的短名（标签里括号前的部分）：工具栏只放短名，完整标签放悬停提示。 */
export function shortModeLabel(label: string | undefined, id: string): string {
  return label?.split(/[（(]/)[0].trim() || id;
}
