// protocol 2 的命令表（docs/01 - 设计文档/06 - 应用协议契约.md 10.4）。线上类型全部来自 wire.gen.ts（由 Go 生成，不手写）；
// 这里只把命令名对到参数与结果类型。COMMANDS 由 Go 测试 transport/bind/bind_test.go（TestProtocolCommands）
// 校验与 appapi.ConnCommands() 一致。

import type * as W from "./wire.gen";

export const COMMANDS = [
  "hello",
  "sessions.list",
  "session.create",
  "session.update",
  "session.delete",
  "feedback.set",
  "feedback.list",
  "session.followups",
  "session.get",
  "session.subscribe",
  "session.unsubscribe",
  "session.entries",
  "session.send",
  "session.steer",
  "session.unqueue",
  "session.cancel",
  "session.rewind",
  "session.compact",
  "session.fork",
  "session.stats",
  "session.settings",
  "interaction.respond",
  "interactions.list",
  "mode.list",
  "mode.get",
  "mode.set",
  "mode.show",
  "mode.save",
  "mode.remove",
  "mode.setDefault",
  "plugins.list",
  "plugins.set",
  "packages.list",
  "packages.preview",
  "packages.install",
  "packages.remove",
  "packages.setEnabled",
  "workspace.files",
  "changes.list",
  "changes.diff",
  "changes.revert",
  "skills.list",
  "skills.setEnabled",
  "mcp.list",
  "mcp.test",
  "mcp.save",
  "mcp.remove",
  "mcp.setEnabled",
  "workspace.list",
  "workspace.open",
  "workspace.reload",
  "workspace.trustPreview",
  "workspace.trust",
  "models.list",
  "artifact.get",
  "artifact.put",
  "trace.explain",
] as const;

export type Command = (typeof COMMANDS)[number];

/**
 * 命令名 → [参数, 结果]（可声明合并，docs/61 3.3）。产品扩展命令这样加：
 * `declare module "@tenon/client" { interface CommandMap { "mortise.system.storage": [Empty, StorageResult] } }`
 * 合并后 `call<K extends keyof CommandMap>` 直接认新命令。COMMANDS 只列核心命令。
 */
export interface CommandMap extends CoreCommandMap {}

/** 核心命令表（tenon 自己的命令，与 COMMANDS 一一对应）。 */
export interface CoreCommandMap {
  hello: [W.HelloArgs, W.HelloResult];
  "sessions.list": [W.ListArgs, W.ListResult];
  "session.create": [W.CreateArgs, W.SessionSnapshot];
  "session.update": [W.UpdateArgs, W.SessionSnapshot];
  "session.delete": [W.SessionArgs, W.Empty];
  "feedback.set": [W.FeedbackArgs, W.Feedback];
  "feedback.list": [W.SessionArgs, W.FeedbackListResult];
  "session.followups": [W.FollowupsArgs, W.FollowupsResult];
  "session.get": [W.SessionArgs, W.SessionSnapshot];
  "session.subscribe": [W.SubscribeArgs, W.Empty];
  "session.unsubscribe": [W.SessionArgs, W.Empty];
  "session.entries": [W.EntriesArgs, W.EntriesResult];
  "session.send": [W.SendArgs, W.SendResult];
  "session.steer": [W.SteerArgs, W.Empty];
  "session.unqueue": [W.UnqueueArgs, W.UnqueueResult];
  "session.cancel": [W.SessionArgs, W.Empty];
  "session.rewind": [W.RewindArgs, W.RewindResult];
  "session.compact": [W.CompactArgs, W.CompactResult];
  "session.fork": [W.ForkArgs, W.SessionSnapshot];
  "session.stats": [W.SessionArgs, W.Stats];
  "session.settings": [W.SettingsArgs, W.SettingsResult];
  "interaction.respond": [W.RespondArgs, W.RespondResult];
  "interactions.list": [W.Empty, W.InteractionsResult];
  "mode.list": [W.Empty, W.ModeListResult];
  "mode.get": [W.SessionArgs, W.ModeSetResult];
  "mode.set": [W.ModeSetArgs, W.ModeSetResult];
  "mode.show": [W.ModeShowArgs, W.ModeShowResult];
  "mode.save": [W.ModeSaveArgs, W.ModeSaveResult];
  "mode.remove": [W.ModeRemoveArgs, W.ModeRemoveResult];
  "mode.setDefault": [W.ModeSetDefaultArgs, W.ModeSetDefaultResult];
  "plugins.list": [W.Empty, W.PluginsListResult];
  "plugins.set": [W.PluginsSetArgs, W.PluginsSetResult];
  "packages.list": [W.Empty, W.PackagesListResult];
  "packages.preview": [W.PackagesPreviewArgs, W.PackagesPreviewResult];
  "packages.install": [W.PackagesInstallArgs, W.PackagesEditResult];
  "packages.remove": [W.PackagesRemoveArgs, W.PackagesEditResult];
  "packages.setEnabled": [W.PackagesSetEnabledArgs, W.PackagesEditResult];
  "workspace.files": [W.FilesArgs, W.FilesResult];
  "changes.list": [W.ChangesArgs, W.ChangesResult];
  "changes.diff": [W.ChangesDiffArgs, W.ChangesDiffResult];
  "changes.revert": [W.ChangesRevertArgs, W.ChangesRevertResult];
  "skills.list": [W.Empty, W.SkillsListResult];
  "skills.setEnabled": [W.SkillsSetEnabledArgs, W.SkillsSetEnabledResult];
  "mcp.list": [W.Empty, W.MCPListResult];
  "mcp.test": [W.MCPTestArgs, W.MCPTestResult];
  "mcp.save": [W.MCPSaveArgs, W.MCPEditResult];
  "mcp.remove": [W.MCPRemoveArgs, W.MCPEditResult];
  "mcp.setEnabled": [W.MCPSetEnabledArgs, W.MCPEditResult];
  "workspace.list": [W.Empty, W.WorkspaceListResult];
  "workspace.open": [W.WorkspaceOpenArgs, W.WorkspaceOpenResult];
  "workspace.reload": [W.WorkspaceReloadArgs, W.WorkspaceReloadResult];
  "workspace.trustPreview": [W.Empty, W.WorkspaceTrustPreviewResult];
  "workspace.trust": [W.WorkspaceTrustArgs, W.WorkspaceTrustResult];
  "models.list": [W.Empty, W.ModelsListResult];
  "artifact.get": [W.ArtifactGetArgs, W.ArtifactGetResult];
  "artifact.put": [W.ArtifactPutArgs, W.ArtifactPutResult];
  "trace.explain": [W.TraceArgs, W.TraceResult];
}

// 编译期核对：COMMANDS 与 CoreCommandMap 的键一一对应（产品合并进 CommandMap 的命令不在核对范围）。
type _Missing = Exclude<Command, keyof CoreCommandMap>;
type _Extra = Exclude<keyof CoreCommandMap, Command>;
const _check: [_Missing, _Extra] extends [never, never] ? true : never = true;
void _check;

/** 命令失败：code 见 wire.gen.ts 的 ErrorCodes；message 只给人看，不要解析（10.7）。 */
export class ApiError extends Error {
  code: string;
  retry: boolean;
  details?: Record<string, unknown>;
  constructor(b: W.ProtocolError) {
    super(b.message);
    this.code = b.code;
    this.retry = !!b.retry;
    this.details = b.details;
  }
  /** conflict 的 details.reason（exists / answered / stale / leased）。 */
  get reason(): string {
    const r = this.details?.["reason"];
    return typeof r === "string" ? r : "";
  }
}

/** 本地事件：传输层重连（WebSocket 断线后连接级订阅全部丢失，订阅方据此从最后的游标重新订阅）。不是线上事件。 */
export const LOCAL_RECONNECTED = "local.reconnected";
