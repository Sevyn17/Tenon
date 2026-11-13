# Tenon 协议客户端（`@tenon/client`）

Tenon 的前端**只有协议客户端这一层**，没有界面代码。界面属于应用层：
工作台、组件库、hooks 都在产品仓库（如 `Mortise/client/web`）。

```
src/
├── index.ts            createClient() 与 Client 接口
├── transport.ts        Wails / WebSocket 传输，断线重连
├── session.ts          SessionStore：快照 → 日志尾部 → 按游标订阅，流式与工具调用合成
├── session-reducer.ts  纯 reducer（可单测，不碰 IO）
├── sessions.ts         会话列表
├── interactions.ts     未结束交互
├── commands.ts         命令表（与 transport/bind 的测试对表）
├── store.ts            ReadStore 最小订阅接口
└── wire.gen.ts         线上类型（由 appapi/wire 生成，不手写）
```

`wire.gen.ts` 由 `go test ./appapi/wire -run TestTypeScript -update` 生成；
`commands.ts` 的 `COMMANDS` 由 `transport/bind` 的 `TestProtocolCommands` 校验。
两者都不手写，改协议只改 Go 侧。

```bash
pnpm install
pnpm test        # 19 个用例：reducer、会话列表、传输层
pnpm typecheck   # tsc --noEmit
```

## 为什么框架只留这一层

界面（布局、组件、快捷键）是**产品决策**，每个应用不一样，放在框架里只会变成负担。
但协议客户端不是界面——它把 WebSocket 帧解成会话状态，是 `appapi/wire` 的运行时对应物。

留它的理由：

1. **协议语义需要真实消费者验证**。「快照 + 游标订阅能还原完整状态」这个保证，
   光有 Go 结构体无法验证，得有一个客户端真跑一遍。
2. **避免每个应用重写**。约 2000 行状态机，各写一遍容易在「流式合并」「游标断点」上分叉。

它不依赖 React，界面用 Vue、原生 JS、移动端、TUI 都能直接用它。

## 用法

```ts
import { createClient } from "@tenon/client";

const client = createClient();          // 缺省自动选 Wails 或 WebSocket
await client.start();                   // hello 握手
const s = client.session("my-session");
await s.open({ create: true });
s.subscribe((v) => render(v.entries, v.streams, v.tools, v.runState));
await s.send("你好");
```

产品的界面层（`@tenon/web` 时期的工作台）见 `Mortise/client/web`。
