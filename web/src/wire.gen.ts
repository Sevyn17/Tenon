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

export const InteractionKinds = ["approval", "question", "choice", "form", "confirm"] as const;
export type InteractionKind = (typeof InteractionKinds)[number];

export const Outcomes = ["approved", "denied", "answered", "expired", "voided"] as const;
export type Outcome = (typeof Outcomes)[number];

export const DurableEvents = ["entry.appended", "log.rewound", "log.compacted", "session.state_changed", "session.mode_changed", "session.settings_changed", "interaction.opened", "interaction.resolved", "session.closed", "session.forked", "session.updated"] as const;
export type DurableEvent = (typeof DurableEvents)[number];

export const VolatileEvents = ["turn.started", "turn.ended", "turn.rejected", "message.started", "message.delta", "message.ended", "message.discarded", "tool.started", "tool.args_delta", "tool.running", "tool.progress", "tool.ended", "notice", "session.synced", "session.lagged", "sessions.changed"] as const;
export type VolatileEvent = (typeof VolatileEvents)[number];

export const ErrorCodes = ["bad_request", "invalid", "not_found", "closed", "not_idle", "busy", "conflict", "denied", "need_confirm", "cursor_expired", "unauthorized", "forbidden", "unsupported", "internal"] as const;
export type ErrorCode = (typeof ErrorCodes)[number];

export const ConflictReasons = ["exists", "answered", "stale", "leased"] as const;
export type ConflictReason = (typeof ConflictReasons)[number];

export interface Entry {
  id: string;
  idx: number;
  turn: number;
  at: number;
  source?: string;
  client_ids?: string[];
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
  schema?: unknown;
  blocking?: boolean;
  responders: string[];
  deadline?: number;
  opened_at: number;
}

export interface Approval {
  tool: string;
  call_id?: string;
  args?: unknown;
  reason?: string;
  session_rules?: string[];
  blocked?: string[];
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
  run_state: string;
  mode?: Mode;
  settings: Settings;
  log: LogPos;
  interactions: Interaction[];
  live?: Live;
  cursor: number;
  last_end?: TurnEnded;
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
}

export interface SessionArgs {
  session: string;
}

export interface ListArgs {
  cursor?: number;
  limit?: number;
  all?: boolean;
  pinned?: boolean;
  all_workspaces?: boolean;
  unassigned?: boolean;
}

export interface ListResult {
  items: SessionInfo[];
  next: number;
}

export interface UpdateArgs {
  session: string;
  title?: string;
  pinned?: boolean;
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
}

export interface FollowupsResult {
  turn: number;
  items: string[];
}

export interface CreateArgs {
  session?: string;
  definition?: string;
  client_id?: string;
}

export interface SubscribeArgs {
  session: string;
  after?: number;
  children?: boolean;
}

export interface EntriesArgs {
  session: string;
  from?: number;
  to?: number;
  tail?: number;
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
}

export interface RewindResult {
  to: number;
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
  origin?: string;
  if_cursor?: number;
  client_id?: string;
}

export interface SettingsArgs {
  session: string;
  max_steps?: number;
  model?: string;
  reasoning?: string;
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
}

export interface ModeListResult {
  modes: ModeInfo[];
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
}

export interface ModeShowResult {
  mode: string;
  items: ModeItem[];
}

export interface ArtifactGetArgs {
  session: string;
  ref: string;
  offset?: number;
  length?: number;
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

export interface ConfigField {
  key: string;
  type: string;
  default?: string;
  title?: string;
  description?: string;
  group?: string;
  widget?: string;
}

export interface PluginInfo {
  name: string;
  on: boolean;
  default: boolean;
  source: string;
  effects: string[];
  schema: ConfigField[];
  config: Record<string, unknown>;
  mandatory?: boolean;
  pending?: PluginPending;
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

export interface AccountStatus {
  configured: boolean;
  server_url?: string;
  server_source?: string;
  logged_in?: boolean;
  tenant?: string;
  subject?: string;
  product?: string;
  channel?: string;
  version?: string;
  operator?: boolean;
}

export interface AccountLoginArgs {
  step: string;
  device_code?: string;
}

export interface AccountLoginResult {
  done?: boolean;
  user_code?: string;
  verification_uri?: string;
  expires_in?: number;
  interval?: number;
  device_code?: string;
  pending?: boolean;
  status?: AccountStatus;
}

export interface AccountEntitlements {
  configured: boolean;
  cached?: boolean;
  features?: Record<string, boolean>;
  limits?: Record<string, number>;
  aliases?: string[];
  ttl?: number;
  offline_grace?: number;
  version?: number;
}

export interface APIKey {
  id: string;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
  revoked: boolean;
}

export interface APIKeyListResult {
  configured: boolean;
  keys: APIKey[];
}

export interface APIKeyCreateArgs {
  name: string;
  expires_in?: number;
}

export interface APIKeyCreateResult {
  id: string;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
  revoked: boolean;
  key: string;
}

export interface APIKeyRevokeArgs {
  id: string;
}

export interface APIKeyRevokeResult {
}

export interface BYOKProfile {
  id: string;
  name: string;
  protocol: string;
  base_url: string;
  model: string;
  masked_key: string;
  created_at: string;
  is_default: boolean;
}

export interface BYOKListResult {
  profiles: BYOKProfile[];
  path?: string;
}

export interface BYOKSaveArgs {
  id?: string;
  name: string;
  protocol: string;
  base_url: string;
  model: string;
  api_key: string;
}

export interface BYOKSaveResult {
  profile: BYOKProfile;
}

export interface BYOKRemoveArgs {
  id: string;
}

export interface BYOKRemoveResult {
}

export interface BYOKSetDefaultArgs {
  id: string;
}

export interface BYOKSetDefaultResult {
}

export interface BYOKTestArgs {
  id?: string;
  protocol?: string;
  base_url?: string;
  model?: string;
  api_key?: string;
}

export interface BYOKTestResult {
  ok: boolean;
  latency_ms?: number;
  method?: string;
  model_found?: boolean;
  models?: string[];
  error_kind?: string;
  status?: number;
  message: string;
}

export interface PlatformConfigureArgs {
  server_url: string;
  test?: boolean;
  dry_run?: boolean;
}

export interface PlatformConfigureResult {
  saved: boolean;
  logged_out?: boolean;
  test?: BYOKTestResult;
  status: AccountStatus;
}

export interface AdminModelProvider {
  id: string;
  key: string;
  name: string;
  protocol: string;
  base_url: string;
  status: string;
  key_count: number;
}

export interface AdminModel {
  id: string;
  provider_key: string;
  vendor_model: string;
  kind: string;
}

export interface AdminAlias {
  alias: string;
  kind: string;
  plan_id: string;
  visible: boolean;
  step_count: number;
}

export interface AdminRateCard {
  id: string;
  version: number;
  status: string;
  rates: string;
}

export interface AdminModelsResult {
  providers: AdminModelProvider[];
  models: AdminModel[];
  aliases: AdminAlias[];
  rate_cards: AdminRateCard[];
  read_only: boolean;
}

export interface PasswordAuthArgs {
  email: string;
  password: string;
  name?: string;
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
