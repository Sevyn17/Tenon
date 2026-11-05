// 旧界面用的传输入口：只是 @ybase/client 连接的一层包装（实现在 client/transport.ts）。
// 新工作台（shell/）直接用 createClient；旧界面删除时本文件一并删除。

import { autoTransport, clientID as newClientID, connect, type Listener } from "./client/transport";
import type { CommandMap } from "./client/commands";

const conn = connect(autoTransport());

/** 执行一条 protocol 2 命令；失败抛 ApiError（含错误码与 details）。 */
export function call<K extends keyof CommandMap>(cmd: K, args: CommandMap[K][0]): Promise<CommandMap[K][1]> {
  return conn.call(cmd, args);
}

/** 订阅连接上的全部事件（含本地事件 local.reconnected）；返回取消订阅函数。 */
export function onEvent(fn: Listener): () => void {
  return conn.onEvent(fn);
}

/** 客户端幂等键。 */
export const clientID = newClientID;
