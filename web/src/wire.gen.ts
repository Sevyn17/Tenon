// 由 kernel/appapi/wire 生成，不要手改。重新生成：cd kernel && go test ./appapi/wire -run TestTypeScript -update

export const PROTOCOL = 2;

export const ItemKinds = ["user", "assistant", "tool_result", "compaction", "context", "unknown"] as const;
export type ItemKind = (typeof ItemKinds)[number];

export const RunStates = ["idle", "running"] as const;
export type RunState = (typeof RunStates)[number];

export const ReasoningEfforts = ["low", "medium", "high"] as const;
export type ReasoningEffort = (typeof ReasoningEfforts)[number];

export const Roles = ["viewer", "operator"] as const;
export type Role = (typeof Roles)[number];

export const InteractionKinds = ["approval", "question", "choice", "form", "confirm", "plan"] as const;
export type InteractionKind = (typeof InteractionKinds)[number];

export const WorkModes = ["execute", "plan"] as const;
export type WorkMode = (typeof WorkModes)[number];

export const PlanChoices = ["approve", "revise", "continue"] as const;
export type PlanChoice = (typeof PlanChoices)[number];

export const Outcomes = ["approved", "denied", "answered", "expired", "voided"] as const;
export type Outcome = (typeof Outcomes)[number];

export const DurableEvents = ["entry.appended", "log.rewound", "log.compacted", "session.state_changed", "session.mode_changed", "session.settings_changed", "interaction.opened", "interaction.resolved", "session.closed", "session.forked", "session.updated"] as const;
export type DurableEvent = (typeof DurableEvents)[number];

export const VolatileEvents = ["turn.started", "turn.ended", "turn.rejected", "message.started", "message.delta", "message.ended", "message.discarded", "tool.started", "tool.args_delta", "tool.running", "tool.progress", "tool.ended", "notice", "session.synced", "session.lagged", "sessions.changed", "model.retrying", "queue.changed"] as const;
export type VolatileEvent = (typeof VolatileEvents)[number];

export const ErrorCodes = ["bad_request", "invalid", "not_found", "closed", "not_idle", "busy", "conflict", "denied", "need_confirm", "cursor_expired", "unauthorized", "forbidden", "unsupported", "internal"] as const;
export type ErrorCode = (typeof ErrorCodes)[number];

export const ConflictReasons = ["exists", "answered", "stale", "leased", "files"] as const;
export type ConflictReason = (typeof ConflictReasons)[number];

export const InvalidReasons = ["other_workspace"] as const;
export type InvalidReason = (typeof InvalidReasons)[number];

export const ChangeScopes = ["session", "turn", "from_turn", "git"] as const;
export type ChangeScope = (typeof ChangeScopes)[number];

export const SkillScopes = ["global", "workspace"] as const;
export type SkillScope = (typeof SkillScopes)[number];

export const MCPScopes = ["global", "workspace"] as const;
export type MCPScope = (typeof MCPScopes)[number];

export const Risks = ["exec", "network", "write", "config", "prompt", "commands"] as const;
export type Risk = (typeof Risks)[number];

export const VersionCompares = ["newer", "same", "older", "unknown"] as const;
export type VersionCompare = (typeof VersionCompares)[number];

export const MCPStates = ["connected", "starting", "failed", "disabled"] as const;
export type MCPState = (typeof MCPStates)[number];

export const MCPTransports = ["stdio", "http"] as const;
export type MCPTransport = (typeof MCPTransports)[number];

export const NoticeLevels = ["info", "warn", "error"] as const;
export type NoticeLevel = (typeof NoticeLevels)[number];

export const TrustLevels = ["trusted", "untrusted"] as const;
export type TrustLevel = (typeof TrustLevels)[number];

export interface Entry {
  id: string;
  idx: number;
  turn: number;
  at: number;
  source?: string;
  client_ids?: string[];
  from?: string;
  replies?: { entry?: string; from: string; send?: boolean }[];
  item: Item;
}

export interface Item {
  kind: string;
  content?: Part[];
  reasoning?: Reasoning[];
  tool_calls?: ToolCall[];
  model?: string;
  usage?: Usage;
  interrupted?: boolean;
  call_id?: string;
  output?: Part[];
  status?: string;
  cancel_reason?: string;
  truncated?: boolean;
  output_ref?: string;
  upto?: number;
  summary?: Part[];
  source?: string;
  text?: string;
  raw?: unknown;
}

export interface Part {
  kind: string;
  text?: string;
  media?: MediaRef;
  data?: unknown;
  schema?: string;
}

export interface MediaRef {
  ref: string;
  mime: string;
  size?: number;
  sha256?: string;
}

export interface Reasoning {
  text: string;
}

export interface ToolCall {
  id: string;
  name: string;
  args: unknown;
}

export interface Usage {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  source: string;
}

export interface LogPos {
  base: number;
  len: number;
}

export interface Mode {
  mode: string;
  label: string;
  sandbox: string;
}

export interface Settings {
  max_steps: number;
  model?: string;
  reasoning?: string;
  work_mode?: string;
}

export interface Interaction {
  id: string;
  session: string;
  turn?: number;
  kind: string;
  title: string;
  approval?: Approval;
  question?: Question;
  choice?: Choice;
  form?: Form;
  confirm?: Confirm;
  plan?: PlanPrompt;
  schema?: unknown;
  blocking?: boolean;
  responders: string[];
  deadline?: number;
  opened_at: number;
  creator_session?: string;
  chain_root?: string;
  chain_depth?: number;
}

export interface Approval {
  tool: string;
  call_id?: string;
  args?: unknown;
  reason?: string;
  detail?: ApprovalDetail;
  session_rules?: string[];
  blocked?: string[];
}

export interface ApprovalDetail {
  mode?: string;
  mode_label?: string;
  mode_builtin?: boolean;
  item?: string;
  item_title?: string;
  action?: string;
  subject?: string;
  pattern?: string;
  cap?: string;
  untrusted?: boolean;
}

export interface Question {
  text: string;
  options?: string[];
}

export interface Choice {
  text?: string;
  options: ChoiceOption[];
  multiple?: boolean;
  min?: number;
  max?: number;
}

export interface ChoiceOption {
  id: string;
  label: string;
  description?: string;
}

export interface Form {
  text?: string;
  submit?: string;
}

export interface Confirm {
  text?: string;
  danger?: boolean;
  confirm?: string;
  cancel?: string;
}

export interface PlanPrompt {
  plan: string;
  steps?: string[];
}

export interface PlanAnswer {
  choice: string;
  feedback?: string;
}

export interface Live {
  turn: number;
  streams: LiveStream[];
  tools: LiveTool[];
}

export interface LiveStream {
  stream_id: string;
  entry_id: string;
  part: string;
  text: string;
}

export interface LiveTool {
  call_id: string;
  name: string;
  args?: unknown;
}

export interface SessionSnapshot {
  id: string;
  parent?: string;
  origin: string;
  title: string;
  pinned?: boolean;
  archived?: boolean;
  agent?: string;
  creator_session?: string;
  chain_depth?: number;
  run_state: string;
  mode?: Mode;
  settings: Settings;
  log: LogPos;
  interactions: Interaction[];
  live?: Live;
  cursor: number;
  last_end?: TurnEnded;
  queued?: QueuedInput[];
  source?: string;
  ref?: string;
}

export interface SessionInfo {
  id: string;
  title: string;
  preview: string;
  run_state: string;
  origin: string;
  parent?: string;
  updated_at: number;
  pinned?: boolean;
  workspace?: string;
  workspace_id?: string;
  agent?: string;
  creator_session?: string;
  chain_depth?: number;
  archived?: boolean;
  last_end?: TurnEnded;
  source?: string;
  ref?: string;
}

export interface Principal {
  id: string;
  role: string;
}

export interface Stats {
  attempts: number;
  reported: number;
  missing: number;
  errors: number;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  hit_rate: number;
  tool_calls: number;
  compacts: number;
  last_request?: string;
  composition?: unknown;
}

export interface Envelope {
  v: number;
  session: string;
  kind: string;
  at: number;
  cursor?: number;
  turn?: number;
  data: unknown;
}

export interface EntryAppended {
  entry: Entry;
}

export interface LogRewound {
  to: number;
  voided_interactions: string[];
}

export interface LogCompacted {
  upto: number;
  entry_idx: number;
}

export interface StateChanged {
  run_state: string;
  last_end?: TurnEnded;
}

export interface SettingsChanged {
  settings: Settings;
}

export interface InteractionResolved {
  id: string;
  session: string;
  outcome: string;
  by?: string;
}

export interface SessionClosed {
  reason: string;
}

export interface SessionForked {
  from: string;
  upto: number;
}

export interface SessionUpdated {
  title: string;
  pinned: boolean;
  archived?: boolean;
}

export interface TurnStarted {
  turn: number;
  inputs: string[];
}

export interface TurnEnded {
  turn: number;
  kind: string;
  code?: string;
  message?: string;
  at?: number;
}

export interface TurnRejected {
  inputs: string[];
  reason: string;
}

export interface MessageStarted {
  stream_id: string;
  entry_id: string;
  part: string;
  attempt: number;
}

export interface ModelRetrying {
  attempt: number;
  max: number;
  wait_ms: number;
  reason: string;
}

export interface MessageDelta {
  stream_id: string;
  text: string;
}

export interface StreamRef {
  stream_id: string;
}

export interface ToolStarted {
  call_id: string;
  name: string;
  entry_id: string;
}

export interface ToolArgsDelta {
  call_id: string;
  text: string;
}

export interface ToolRunning {
  call_id: string;
  args: unknown;
}

export interface ToolProgress {
  call_id: string;
  text?: string;
  data?: unknown;
}

export interface ToolEnded {
  call_id: string;
  status: string;
  preview: string;
  output_ref?: string;
}

export interface Notice {
  level: string;
  source?: string;
  text: string;
  key?: string;
  args?: Record<string, string>;
  replay?: boolean;
}

export interface CursorRef {
  cursor: number;
}

export interface SessionsChanged {
  id: string;
  run_state: string;
  title: string;
  updated_at: number;
  pinned: boolean;
  deleted?: boolean;
  preview?: string;
  parent?: string;
  origin?: string;
  workspace?: string;
  agent?: string;
  creator_session?: string;
  chain_depth?: number;
  archived?: boolean;
  last_end?: TurnEnded;
}

export interface ProtocolRange {
  min: number;
  max: number;
}

export interface HelloArgs {
  client: string;
  protocol: ProtocolRange;
}

export interface HelloResult {
  protocol: number;
  server: string;
  principal: Principal;
  capabilities: string[];
  model: string;
  workdir: string;
  trusted: boolean;
  max_output: number;
  workspace?: string;
  workspace_id?: string;
  extensions?: ExtensionInfo[];
}

export interface ExtensionInfo {
  namespace: string;
  version?: string;
}

export interface SessionArgs {
  session: string;
  all_workspaces?: boolean;
}

export interface ListArgs {
  cursor?: number;
  limit?: number;
  all?: boolean;
  pinned?: boolean;
  all_workspaces?: boolean;
  query?: string;
  parent?: string;
  archived?: string;
  agent?: string;
  workspace_id?: string;
  exclude_origins?: string[];
}

export interface ListResult {
  items: SessionInfo[];
  next: number;
}

export interface UpdateArgs {
  session: string;
  title?: string;
  pinned?: boolean;
  archived?: boolean;
}

export interface FeedbackArgs {
  session: string;
  entry: string;
  vote: string;
  note?: string;
}

export interface Feedback {
  entry: string;
  vote: string;
  note?: string;
  by: string;
  at: number;
}

export interface FeedbackListResult {
  items: Feedback[];
}

export interface FollowupsArgs {
  session: string;
  turn?: number;
  all_workspaces?: boolean;
}

export interface FollowupsResult {
  turn: number;
  items: string[];
}

export interface CreateArgs {
  session?: string;
  definition?: string;
  client_id?: string;
  source?: string;
  ref?: string;
  title?: string;
}

export interface SubscribeArgs {
  session: string;
  after?: number;
  children?: boolean;
  all_workspaces?: boolean;
}

export interface EntriesArgs {
  session: string;
  from?: number;
  to?: number;
  tail?: number;
  turn?: number;
  all_workspaces?: boolean;
}

export interface EntriesResult {
  entries: Entry[];
  base: number;
  len: number;
}

export interface SendArgs {
  session: string;
  content?: Part[];
  text?: string;
  client_id: string;
  queue?: boolean;
}

export interface SendResult {
  client_id: string;
  queued: boolean;
}

export interface SteerArgs {
  session: string;
  text: string;
  client_id: string;
}

export interface RewindArgs {
  session: string;
  to_turn: number;
  if_cursor?: number;
  restore_files?: boolean;
  force?: boolean;
}

export interface QueuedInput {
  id: string;
  content: Part[];
  at: number;
}

export interface QueueChanged {
  queued: QueuedInput[];
}

export interface UnqueueArgs {
  session: string;
  id: string;
}

export interface UnqueueResult {
  removed: boolean;
}

export interface RewindResult {
  to: number;
  files?: string[];
}

export interface CompactArgs {
  session: string;
  upto?: number;
  if_cursor?: number;
}

export interface CompactResult {
  summary: string;
  upto: number;
  tokens_before?: number;
  tokens_after?: number;
}

export interface ForkArgs {
  session: string;
  to?: string;
  upto?: number;
  upto_turn?: number;
  origin?: string;
  if_cursor?: number;
  client_id?: string;
}

export interface SettingsArgs {
  session: string;
  max_steps?: number;
  model?: string;
  reasoning?: string;
  work_mode?: string;
}

export interface SettingsResult {
  settings: Settings;
  committed: boolean;
}

export interface ModelInfo {
  id: string;
  alias?: boolean;
  provider: string;
  name?: string;
  label?: string;
  context_window?: number;
  images?: boolean;
  reasoning?: boolean;
  efforts?: string[];
  available: boolean;
  reason?: string;
}

export interface ModelsListResult {
  models: ModelInfo[];
  default: string;
  effective?: EffectiveModel;
}

export interface EffectiveModel {
  model: string;
  provider?: string;
  protocol?: string;
  base_url?: string;
  name?: string;
  label?: string;
  cred_kind: string;
  cred_label?: string;
  origin?: string;
  origin_ref?: string;
  available: boolean;
  reason?: string;
  notice?: string;
}

export interface WorkspaceInfo {
  id: string;
  path: string;
  name: string;
  trusted?: boolean;
  current?: boolean;
  opened_at?: number;
  missing?: boolean;
}

export interface WorkspaceListResult {
  items: WorkspaceInfo[];
}

export interface LocateArgs {
  session: string;
  entry?: string;
}

export interface LocateResult {
  workspace: string;
  workspace_id?: string;
  turn?: number;
  rewound?: boolean;
}

export interface WorkspaceOpenArgs {
  path: string;
}

export interface WorkspaceOpenResult {
  workspace: WorkspaceInfo;
}

export interface WorkspaceReloadArgs {
  force?: boolean;
}

export interface WorkspaceReloadResult {
  workspace: WorkspaceInfo;
}

export interface Response {
  approve?: boolean;
  scope?: string;
  text?: string;
  value?: unknown;
}

export interface RespondArgs {
  interaction: string;
  response: Response;
  client_id?: string;
}

export interface RespondResult {
  outcome: string;
}

export interface InteractionsResult {
  items: Interaction[];
}

export interface ModeInfo {
  id: string;
  label: string;
  short?: string;
  builtin: boolean;
  project: boolean;
  widened?: string[];
  description?: string;
}

export interface ModeListResult {
  modes: ModeInfo[];
  default?: string;
}

export interface ModeSetArgs {
  session: string;
  mode: string;
  confirm?: boolean;
}

export interface ModeSetResult {
  state: Mode;
  committed: boolean;
}

export interface ModeShowArgs {
  session?: string;
  mode?: string;
}

export interface ModeItem {
  item: string;
  title: string;
  value: string;
  source: string;
  bounded: boolean;
  class?: string;
  type?: string;
  owner?: string;
  tools?: string[];
  levels?: string[];
  allow?: string[];
  ask?: string[];
  deny?: string[];
  overridden?: boolean;
}

export interface ModeShowResult {
  mode: string;
  items: ModeItem[];
  name?: string;
  base?: string;
  description?: string;
  builtin?: boolean;
  project?: boolean;
  editable?: boolean;
  revision?: string;
}

export interface ModeItemDraft {
  default?: string;
  level?: string;
  allow?: string[];
  ask?: string[];
  deny?: string[];
}

export interface ModeDraft {
  id: string;
  name?: string;
  base?: string;
  description?: string;
  items?: Record<string, ModeItemDraft>;
}

export interface ModeSaveArgs {
  mode: ModeDraft;
  revision?: string;
  create?: boolean;
}

export interface ModeSaveResult {
  mode: ModeInfo;
  revision: string;
}

export interface ModeRemoveArgs {
  id: string;
  revision?: string;
}

export interface ModeRemoveResult {
  id: string;
  default: string;
  fell_back?: boolean;
  note?: string;
}

export interface ModeSetDefaultArgs {
  id: string;
}

export interface ModeSetDefaultResult {
  default: string;
  pinned?: string;
}

export interface ArtifactGetArgs {
  session: string;
  ref: string;
  offset?: number;
  length?: number;
  all_workspaces?: boolean;
}

export interface ArtifactGetResult {
  mime: string;
  size: number;
  text?: string;
  base64?: string;
}

export interface ArtifactPutArgs {
  session: string;
  mime: string;
  base64: string;
}

export interface ArtifactPutResult {
  ref: string;
}

export interface TraceArgs {
  trace: string;
}

export interface TraceResult {
  text: string;
}

export interface FilesArgs {
  query?: string;
  limit?: number;
}

export interface FileInfo {
  path: string;
  size: number;
  mtime: number;
}

export interface FilesResult {
  files: FileInfo[];
  truncated: boolean;
}

export interface ChangesArgs {
  session: string;
  scope?: string;
  turn?: number;
  all_workspaces?: boolean;
}

export interface ChangeFile {
  path: string;
  status: string;
  added: number;
  removed: number;
  binary?: boolean;
  sources?: string[];
  turns?: number[];
  untracked?: boolean;
  rewound?: boolean;
  revertible: boolean;
  why?: string;
  conflict?: string;
}

export interface ChangesResult {
  files: ChangeFile[];
  scan_skipped?: string;
}

export interface ChangesDiffArgs {
  session: string;
  path: string;
  scope?: string;
  turn?: number;
  all_workspaces?: boolean;
}

export interface ChangesDiffResult {
  path: string;
  diff: string;
  binary?: boolean;
  old_size: number;
  new_size: number;
  truncated?: boolean;
  unavailable?: string;
}

export interface ChangesRevertArgs {
  session: string;
  paths?: string[];
  scope?: string;
  turn?: number;
  dry_run?: boolean;
  force?: boolean;
}

export interface ChangeConflict {
  path: string;
  reason: string;
  message: string;
  by?: string;
  forceable: boolean;
}

export interface ChangesRevertResult {
  reverted: string[];
  conflicts?: ChangeConflict[];
}

export interface SkillInfo {
  name: string;
  description: string;
  source: string;
  path: string;
  model_invocable: boolean;
  enabled: boolean;
  disabled_scope?: string;
  disabled_reason?: string;
}

export interface SkillsListResult {
  skills: SkillInfo[];
  workspace?: string;
  warnings?: string[];
}

export interface SkillsSetEnabledArgs {
  name: string;
  enabled: boolean;
  scope?: string;
}

export interface SkillsSetEnabledResult {
  skill: SkillInfo;
}

export interface SkillsGetArgs {
  name: string;
  workspace?: string;
}

export interface SkillsGetResult {
  skill: SkillInfo;
  body: string;
  truncated?: boolean;
  size: number;
}

export interface MCPTool {
  name: string;
  public: string;
  description: string;
}

export interface MCPServerInfo {
  name: string;
  source: string;
  transport: string;
  command?: string;
  args: string[];
  url?: string;
  cwd?: string;
  effective_cwd?: string;
  timeout?: number;
  scope: string;
  trust?: string;
  sandbox?: string;
  enabled: boolean;
  disabled_scope?: string;
  state: string;
  error?: string;
  tools: MCPTool[];
  env_keys: string[];
  header_keys: string[];
  readonly?: boolean;
}

export interface MCPListResult {
  servers: MCPServerInfo[];
  user_path?: string;
  project_path?: string;
  revision: string;
  editable: boolean;
  workspace?: string;
  notes?: string[];
}

export interface MCPServerDraft {
  name: string;
  transport?: string;
  command?: string;
  args?: string[];
  cwd?: string;
  url?: string;
  timeout?: number;
  trust?: string;
  sandbox?: string;
  scope?: string;
  env?: Record<string, string | null>;
  headers?: Record<string, string | null>;
  env_remove?: string[];
  header_remove?: string[];
}

export interface MCPTestArgs {
  server: MCPServerDraft;
}

export interface MCPTestResult {
  ok: boolean;
  error?: string;
  tools: MCPTool[];
}

export interface MCPSaveArgs {
  server: MCPServerDraft;
  revision?: string;
  force?: boolean;
}

export interface MCPRemoveArgs {
  name: string;
  revision?: string;
  force?: boolean;
}

export interface MCPSetEnabledArgs {
  name: string;
  enabled: boolean;
  scope?: string;
  force?: boolean;
}

export interface MCPEditResult {
  revision: string;
  reloaded: boolean;
  workspace: WorkspaceInfo;
}

export interface WorkspaceTrustPreviewResult {
  root: string;
  key: string;
  trusted: boolean;
  level?: string;
  why?: string;
  forced?: boolean;
  files: string[];
  mcp_servers: TrustMCPServer[];
  hooks: TrustHook[];
  widened_modes: TrustMode[];
  default_mode?: string;
  packages: TrustPackage[];
  plugins: TrustPluginSetting[];
  notes?: string[];
}

export interface TrustMCPServer {
  name: string;
  command?: string;
  args?: string[];
  url?: string;
  source: string;
}

export interface TrustHook {
  event: string;
  matcher?: string;
  command: string;
  source: string;
}

export interface TrustMode {
  id: string;
  title?: string;
  widened: string[];
  approved?: boolean;
}

export interface TrustPackage {
  name: string;
  version?: string;
  description?: string;
  dir: string;
}

export interface TrustPluginSetting {
  name: string;
  enabled?: boolean;
  keys?: string[];
  ignored?: string[];
}

export interface WorkspaceTrustArgs {
  level: string;
  force?: boolean;
}

export interface WorkspaceTrustResult {
  workspace: WorkspaceInfo;
  key: string;
  level: string;
  reloaded?: boolean;
}

export interface ConfigField {
  key: string;
  type: string;
  default?: string;
  title?: string;
  description?: string;
  group?: string;
  widget?: string;
  elem?: string;
}

export interface PluginInfo {
  name: string;
  version?: string;
  description?: string;
  on: boolean;
  default: boolean;
  source: string;
  effects: string[];
  schema: ConfigField[];
  config: Record<string, unknown>;
  mandatory?: boolean;
  pending?: PluginPending;
  risks?: string[];
}

export interface PluginPending {
  on: boolean;
  config: Record<string, unknown>;
}

export interface ProfileScope {
  scope: string;
  path: string;
  revision: string;
  writable: boolean;
  reason?: string;
}

export interface PluginsListResult {
  plugins: PluginInfo[];
  scopes: ProfileScope[];
  denied?: Record<string, string[]>;
}

export interface PluginsSetArgs {
  name: string;
  enabled?: boolean;
  config?: Record<string, unknown>;
  scope: string;
  if_revision?: string;
}

export interface PluginsSetResult {
  revision: string;
  restart: boolean;
  plugin: PluginInfo;
}

export interface PackageInfo {
  name: string;
  version: string;
  description: string;
  scope: string;
  source: string;
  contents: string;
  ui?: UISlot[];
  enabled: boolean;
  dir?: string;
  risks?: string[];
  managed?: boolean;
  error?: string;
}

export interface UISlot {
  id: string;
  slot: string;
  title?: string;
  order?: number;
  requires?: string;
  view: DeclNode | DeclNode[];
  data?: UIData;
}

export interface UIData {
  cmd: string;
  args?: Record<string, unknown>;
}

export interface PackagesListResult {
  packages: PackageInfo[];
}

export interface PackagesPreviewArgs {
  source: string;
}

export interface PackagesPreviewResult {
  name: string;
  version?: string;
  description?: string;
  contents: string;
  dir: string;
  mcp_servers: TrustMCPServer[];
  hooks: TrustHook[];
  skills: string[];
  prompts: string[];
  agents: string[];
  config: PackageConfig[];
  ui: UISlot[];
  ui_commands: string[];
  risks: string[];
  installed?: PackageInstalled;
  notes?: string[];
}

export interface PackageConfig {
  plugin: string;
  key: string;
  value: unknown;
  ignored?: boolean;
}

export interface PackageInstalled {
  version?: string;
  enabled: boolean;
  compare: string;
  error?: string;
}

export interface PackagesInstallArgs {
  source: string;
  force?: boolean;
}

export interface PackagesRemoveArgs {
  name: string;
  force?: boolean;
}

export interface PackagesSetEnabledArgs {
  name: string;
  enabled: boolean;
  force?: boolean;
}

export interface PackagesEditResult {
  name: string;
  trashed?: string;
  reloaded: boolean;
  workspace: WorkspaceInfo;
}

export interface Empty {
}

export interface ProtocolError {
  code: string;
  message: string;
  retry: boolean;
  details?: Record<string, unknown>;
}

export type DeclAction =
  | { kind: "call"; cmd: string; args?: unknown }
  | { kind: "openPanel"; panel: string; params?: unknown }
  | { kind: "insert"; text: string }
  | { kind: "respond"; interaction: string; response: unknown };

export type DeclNode =
  | { type: "stack"; children: DeclNode[]; gap?: number }
  | { type: "row"; children: DeclNode[]; gap?: number; align?: string }
  | { type: "text"; text: string; tone?: string; size?: string; bold?: boolean }
  | { type: "markdown"; text: string }
  | { type: "badge"; text: string; tone?: string }
  | { type: "code"; code: string; lang?: string }
  | { type: "diff"; diff: string }
  | { type: "list"; items: DeclNode[]; ordered?: boolean }
  | { type: "table"; columns: string[]; rows: (string | number)[][] }
  | { type: "keyValue"; rows: [string, string][] }
  | { type: "progress"; value: number; label?: string }
  | { type: "button"; label: string; action: DeclAction; variant?: string }
  | { type: "link"; label: string; action: DeclAction }
  | { type: "form"; schema: Record<string, unknown>; submit: string; action: DeclAction; value?: Record<string, unknown> }
  | { type: "divider" };
