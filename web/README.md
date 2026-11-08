# Tenon 前端

Tenon 的前端和后端 SDK 一样是框架：可以直接用内置的工作台，也可以只用其中一层，或者按[应用协议契约](<../docs/06 - 应用协议契约.md>)另写一套界面。

```bash
pnpm install
pnpm dev         # 开发服务器（需要后端在跑，见下文"本地运行"）
pnpm typecheck   # 依赖方向检查 + tsc
pnpm test        # vitest：会话 reducer、布局 reducer、快捷键、注册表、传输层心跳；组件测试（happy-dom + Testing Library）：交互卡、Markdown、声明式视图
pnpm build       # 产物在 dist/，由 embed.go 嵌进 Go 二进制
```

开发时的陈列页：`?gallery` 是基础组件（深浅主题对照），`?gallery=chat` 是会话组件（用固定数据，不连后端）。

写插件时可以打开插槽高亮：地址加 `?slots`（网页服务要先用 `?token=` 登录一次，之后用 `/?token=&slots`），或在命令面板里执行"插槽高亮"。每个插槽外面画一圈虚线，标出位置、种类和已贡献的条目数。设置 › 开发者页还有原始事件流和贡献清单。

## 一、四层结构

每一层只依赖它下面的层，`scripts/check-deps.mjs` 在 `typecheck` / `build` 时强制检查导入方向。

| 层 | 目录 | 内容 | 依赖 |
|---|---|---|---|
| L0 | `src/client` | 协议客户端与状态机：传输（Wails / WebSocket，断线重连）、`SessionStore`（快照 → 日志尾部 → 按游标订阅，流式与工具调用合成）、会话列表、未结束交互 | 无（不依赖 React） |
| L1 | `src/client/react` | 无样式 hooks：`useSession`、`useSessions`、`useInteractions`、`useHello`、`useConnection` | L0 + React |
| L2 | `src/ui` | 设计令牌与主题、基础组件、Markdown、JSON Schema 表单、声明式视图，以及会话组件（消息列表、工具步骤、回合尾巴、交互卡、输入框、查找 / 回合导航 / 选中工具条） | React |
| L3 | `src/shell` | 工作台：注册表与贡献包、区域与布局预设、动态面板区、命令与快捷键、设置、通知 | 以上全部 |

选用到哪一层：

- **另写界面**（Vue、原生 JS、移动端、TUI）：只用 L0，`createClient()` 拿到会话状态，自己渲染。
- **自己的组件库**：L0 + L1。
- **一个简单聊天窗**：L0–L2，拼 `MessageList` + `Composer` + `InteractionCard`。L2 不依赖协议客户端，组件按 `ui/conversation/types.ts` 的最小视图类型工作。
- **完整工作台**：`createShell()`。
- **单会话嵌入**：`<TenonConversation session="…" />`（`shell/embed.tsx`）：只有对话区、输入框与交互卡，面板以浮层打开，不读写地址栏，存储用独立命名空间 `tenon.embed`。和完整工作台用同一套贡献包。

```tsx
import { TenonConversation } from "./shell/embed";

<TenonConversation session="support-42" className="chat-box" contributions={[myPack]} />
```

L0 最小用法：

```ts
import { createClient } from "./client";

const client = createClient();          // 缺省自动选 Wails 或 WebSocket
await client.start();                   // hello 握手
const s = client.session("my-session");
await s.open({ create: true });
s.subscribe((v) => render(v.entries, v.streams, v.tools, v.runState));
await s.send("你好");
```

## 二、工作台：createShell

```ts
import { createShell } from "./shell";

const shell = createShell({
  ns: "myapp",                                  // localStorage 键前缀（偏好、布局、草稿）
  brand: { name: "MyAgent", greeting: "今天做什么？", examples: ["…"] },
  prefs: { theme: "dark", preset: "chat" },     // 偏好初值，用户改过的以存储为准
  disable: ["panels.changes"],                  // 关掉内置贡献包
  contributions: [myPack],                      // 自己的贡献包
  messages: { en: { "conv.empty.title": "…" } },// 覆盖文案
});
createRoot(el).render(<shell.Root />);
```

定制分三档，越往下改得越多：

1. **配置**：主题（覆盖 `ui/tokens.css` 的语义变量）、品牌、文案、偏好、`disable`。
2. **贡献**：往注册表与插槽里加东西，或用同 id、更小的 `priority` 替换内置项（内置为 100）。
3. **布局**：区域（`Region`）和布局预设（`LayoutPreset`）本身也是注册表项，可以换掉左栏、主区，或者整个布局。

### 区域

```
① 菜单区（新建、搜索、nav.primary 菜单项）   ③ 对话区（头部含面板按钮 · 消息流 · ④ 对话内小卡片 · ⑦ 输入框）   ⑤ 动态面板区
② 会话列表
⑧ 项目与设置入口（菜单项仍用 `avatar.menu` 这一位置名）
覆盖层：⑨ 设置 · 命令面板 ⌘K · 快捷键速查 ? · 连接状态条 · 通知
```

内置布局预设：`workbench`（缺省，右侧和底部停靠区；面板入口在对话顶栏）、`chat`（面板变抽屉）、`embed`（无左栏，面板浮动）。窗口不足 1100px 时面板区变成覆盖在对话上的抽屉；窄屏（< 768）时左栏也变成抽屉；768–1024 时左栏收成图标条。

## 三、贡献包与注册表

一切可插入的东西都经注册表贡献，内置功能也一样（见 `shell/builtin/`）。

```tsx
import type { ContributionPack } from "./shell";

export const myPack: ContributionPack = {
  id: "my.review",
  title: "代码审阅",
  requires: { plugin: "fs" },                // 后端没装配 fs 插件时整包不激活
  setup(ctx) {
    ctx.add(reg.panels, {
      id: "review",
      title: "审阅",
      icon: <Eye size={16} />,
      keybind: "Mod+Shift+R",                // 自动生成"切换面板"命令，可在设置里改绑
      needsSession: true,
      card: ReviewCard,                      // ④ 对话内小卡片（组件，无内容时返回 null）
      render: ({ session, params, visible }) => <Review session={session} />,
    });
    ctx.add(reg.commands, { id: "review.run", title: "开始审阅", slash: "review", run: (x) => x.shell.openPanel("review") });
    ctx.add(reg.slots, { id: "review.badge", at: "conversation.header.actions", render: (p) => <ReviewBadge session={p.session as string} /> });
  },
};
```

`reg` 是 `shell.state.reg`。包的 `setup` 同步执行；它加的一切在包停用时一起撤销（`ctx.onDispose` 放额外的清理，`ctx.signal` 给异步操作）。

**规则**

- **可用性四态**：pending（还没握手）/ active / disabled（带原因：设置里关了、后端缺插件或能力）/ failed（带错误）。设置 › 插件页能看到每个包的状态和它贡献了什么。
- **错误隔离**：每份贡献外面一层 ErrorBoundary，抛错只影响它自己的位置，所属包记为 failed。
- **不出现点了没反应的按钮**：命令、菜单项、消息操作的 `when` 返回字符串时显示为停用并给出原因，返回 false 时不显示。
- **同 id 替换**：`priority` 小的胜出，嵌入方据此改掉任何内置项。

**注册表**（`shell/contributions.ts`）

| 注册表 | 用途 |
|---|---|
| `commands` | 命令：标题、分组、`keybind`、`slash`（也作输入框 `/` 命令）、`when`、`run`（返回 false 把按键让给焦点元素） |
| `menus` | 菜单项，位置为 `nav.primary` / `avatar.menu` / `session.row.menu` / `conversation.header.menu` |
| `slots` | 插槽内容，`render(props)` 或声明式 `view` |
| `regions` / `presets` | 区域与布局预设 |
| `panels` | 动态面板区的面板（`dock` 缺省停靠区、`badge`、`card`、`needsSession`） |
| `pages` | 主区页面（菜单区项打开，替换对话区） |
| `messageActions` | 消息操作：`appliesTo` 为 user / assistant / tool / turn / selection，`placement` 为 hover / menu |
| `toolRenderers` | 按工具名（可带 `*` 前缀匹配）给步骤行的动词短语、图标与展开详情 |
| `markdown` | Markdown 扩展（代码块语言、链接处理等） |
| `interactionRenderers` | 按交互 kind 替换交互卡正文 |
| `entryRenderers` | 按条目 kind 渲染组件不认识的条目（后端插件的自定义条目；没有渲染器时显示一行说明） |
| `notificationRenderers` | 按通知 `kind` 替换通知中心里那一行（内置 kind：`turn.done`、`interaction`） |
| `followups` | 回合尾巴的后续建议来源 |
| `composerActions` | 输入框 "+" 菜单（与 `/` 菜单同一份） |
| `mentions` | 输入框 `@` 的候选来源 |
| `settingsPages` / `settingsSections` | 设置页（分组 app / backend / plugin / about）与贡献到某页的区块；`entries` 供设置搜索 |

**插槽**：三种。list 全部渲染；single 只渲染 priority 最小的一个；chain 依次询问，第一个返回非空内容的接管。

| 位置 | 插槽 |
|---|---|
| 左栏 | `nav.brand`（single）、`sidebar.section`、`sidebar.empty`、`sidebar.footer`、`session.row.leading` / `.badge` / `.hover` |
| 对话区 | `conversation.header.leading` / `.actions`、`conversation.banner`、`conversation.empty`（single）、`conversation.card` |
| 消息流 | `message.user.after`、`message.assistant.after`、`message.turn.before` / `.after`、`turn.tail` |
| 输入框 | `composer.above`、`composer.toolbar.left` / `.right`、`composer.below`、`composer.takeover`（chain，阻塞式交互接管输入框） |
| 其他 | `shell.overlay` |

**宿主接口**：命令、操作、菜单项的上下文里有 `shell`（`ShellApi`）：`openSession`、`newSession`、`openPanel(id, params)`、`togglePanel`、`openPage`、`openSettings(page, entry)`、`openPalette`、`insert(text)`、`focusComposer`、`run(commandId)`、`notify`、`toast`、`confirm`、`prompt`。

**声明式 UI**：后端插件不写 TS 也能贡献界面。插槽项给 `view`（`ui/declarative.tsx` 的 `DeclNode`：stack / row / text / markdown / badge / list / table / keyValue / progress / code / diff / button / link / form），按钮的动作只能是调协议命令、打开面板、填入输入框、回答交互；会删数据、改配置的命令（`session.delete`、`session.rewind`、`session.compact`、`plugins.set`、`mode.set`）点了先确认。

**插件包下发**：插件包清单（`tenon-plugin.toml`）的 `[[ui]]` 段经 `packages.list` 到前端，每个包变成一个贡献包 `pkg:<名>`（设置 › 插件里可单独关）：

```toml
[[ui]]
id = "usage-card"
slot = "conversation.card"      # 插槽位置，见上面的插槽清单
title = "本会话用量"
order = 90
requires = "mcp"                # 可选：这个后端插件开着才显示
view_file = "ui/usage.json"     # 或内联 view = { type = "text", text = "…" }
[ui.data]                       # 可选：只读命令，结果替换视图里的 {{路径}}
cmd = "session.stats"
args = { session = "{{session}}" }
```

`{{session}}` 是插槽所在的会话；`data` 只能是只读命令（`runtime/pkgs/ui.go` 的 `UIReadCommands`），每 30 秒与会话变化时重取。单个视图不超过 64 KiB，一个包最多 32 条。

## 四、样式与约束

- CSS Modules + CSS 变量，只用类名。动态尺寸经 React `style` 属性设置，不放宽服务端的 CSP（`style-src 'self'`、`img-src 'self' data:`；所以附件预览用 data URL）。
- 依赖只有 `react`、`lucide-react`，不引 UI 框架与拖拽库。长会话不用虚拟列表：已结束的回合 memo，屏幕外的回合用 `content-visibility` 跳过布局（对话内查找与回合导航按 DOM 工作，回合要一直在 DOM 里）。
- 文案走 `t(key)`（`shell/i18n.ts`，中 / 英两份）。
- 尊重 `prefers-reduced-motion`；模态层有焦点陷阱与焦点归还；回合结束、新交互经 `aria-live` 播报。

## 五、和后端的约定

- 线上类型 `src/client/wire.gen.ts` 由 Go 生成：`go test ./appapi/wire -run TestTypeScript -update`，不要手改。
- 命令表 `src/client/commands.ts` 的 `COMMANDS` 由 `transport/bind/bind_test.go` 的 `TestProtocolCommands` 校验与服务端一致。
- 语义（快照 + 游标、易失事件、交互、错误码）以 [docs/06](<../docs/06 - 应用协议契约.md>) 为准。

## 六、本地运行

```bash
go build -C ../client -o ../framework/bin/tenon-server.exe ./server   # 在 framework/ 下，先 pnpm build，dist/ 会嵌进去
bin/tenon-server.exe -addr 127.0.0.1:8765 -dir <工作目录>
```

启动后终端会打印带 `?token=` 的地址，用它打开。
