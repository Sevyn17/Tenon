// 草稿会话的设置：首页还没建会话时，在输入框工具栏里选的模型与档位。发第一条消息（或加第一个附件）建会话时
// 应用到新会话（session.settings / mode.set），然后清空。工具栏贡献收到的 session 为 null 时读写这里。

import { createStore, type Store } from "../client";

export interface DraftSettings {
  /** 模型 id（models.list 里的 id）；空 = 后端缺省。 */
  model?: string;
  /** 档位 id；空 = 后端缺省。 */
  mode?: string;
  /** 选的是放宽了内置档位的项目档位：选的时候已经确认过，建会话后带 confirm 设置。 */
  modeConfirmed?: boolean;
}

export type DraftStore = Store<DraftSettings>;

export function createDraftSettings(): DraftStore {
  return createStore<DraftSettings>({});
}
