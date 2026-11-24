package wire

// TS 类型生成（06 10.3）：协议类型是唯一来源，web/src/wire.gen.ts 由这里生成，不再手写。
// 类型清单与常量清单都直接引用 Go 符号：改名、删字段时编译或固定用例测试会失败。

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// Version 线上协议版本。
const Version = 2

// tsTypes 要生成的线上类型（按这个顺序输出）。
var tsTypes = []any{
	Entry{}, Item{}, Part{}, MediaRef{}, Reasoning{}, ToolCall{}, Usage{}, LogPos{}, Mode{}, Settings{},
	Interaction{}, Approval{}, Question{}, Choice{}, ChoiceOption{}, Form{}, Confirm{}, PlanPrompt{}, PlanAnswer{}, Live{}, LiveStream{}, LiveTool{}, SessionSnapshot{}, SessionInfo{},
	Principal{}, Stats{},
	Envelope{}, EntryAppended{}, LogRewound{}, LogCompacted{}, StateChanged{}, SettingsChanged{},
	InteractionResolved{}, SessionClosed{}, SessionForked{}, SessionUpdated{}, TurnStarted{}, TurnEnded{}, TurnRejected{},
	MessageStarted{}, ModelRetrying{}, MessageDelta{}, StreamRef{}, ToolStarted{}, ToolArgsDelta{}, ToolRunning{}, ToolProgress{},
	ToolEnded{}, Notice{}, CursorRef{}, SessionsChanged{},
	ProtocolRange{}, HelloArgs{}, HelloResult{}, ExtensionInfo{}, SessionArgs{}, ListArgs{}, ListResult{}, UpdateArgs{}, FeedbackArgs{}, Feedback{}, FeedbackListResult{}, FollowupsArgs{}, FollowupsResult{}, CreateArgs{},
	SubscribeArgs{}, EntriesArgs{}, EntriesResult{}, SendArgs{}, SendResult{}, SteerArgs{}, RewindArgs{},
	QueuedInput{}, QueueChanged{}, UnqueueArgs{}, UnqueueResult{},
	RewindResult{}, CompactArgs{}, CompactResult{}, ForkArgs{}, SettingsArgs{}, SettingsResult{}, ModelInfo{}, ModelsListResult{}, EffectiveModel{}, WorkspaceInfo{}, WorkspaceListResult{}, LocateArgs{}, LocateResult{}, WorkspaceOpenArgs{}, WorkspaceOpenResult{}, WorkspaceReloadArgs{}, WorkspaceReloadResult{}, Response{},
	RespondArgs{}, RespondResult{}, InteractionsResult{}, ModeInfo{}, ModeListResult{}, ModeSetArgs{},
	ModeSetResult{}, ModeShowArgs{}, ModeItem{}, ModeShowResult{},
	ModeItemDraft{}, ModeDraft{}, ModeSaveArgs{}, ModeSaveResult{}, ModeRemoveArgs{}, ModeRemoveResult{}, ModeSetDefaultArgs{}, ModeSetDefaultResult{},
	ArtifactGetArgs{}, ArtifactGetResult{},
	ArtifactPutArgs{}, ArtifactPutResult{}, TraceArgs{}, TraceResult{}, FilesArgs{}, FileInfo{}, FilesResult{},
	ChangesArgs{}, ChangeFile{}, ChangesResult{}, ChangesDiffArgs{}, ChangesDiffResult{}, ChangesRevertArgs{}, ChangeConflict{}, ChangesRevertResult{},
	SkillInfo{}, SkillsListResult{}, SkillsSetEnabledArgs{}, SkillsSetEnabledResult{}, SkillsGetArgs{}, SkillsGetResult{},
	MCPTool{}, MCPServerInfo{}, MCPListResult{}, MCPServerDraft{}, MCPTestArgs{}, MCPTestResult{}, MCPSaveArgs{}, MCPRemoveArgs{}, MCPSetEnabledArgs{}, MCPEditResult{},
	WorkspaceTrustPreviewResult{}, TrustMCPServer{}, TrustHook{}, TrustMode{}, TrustPackage{}, TrustPluginSetting{}, WorkspaceTrustArgs{}, WorkspaceTrustResult{},
	ConfigField{}, PluginInfo{},
	PluginPending{}, ProfileScope{}, PluginsListResult{}, PluginsSetArgs{}, PluginsSetResult{}, PackageInfo{}, UISlot{}, UIData{}, PackagesListResult{},
	PackagesPreviewArgs{}, PackagesPreviewResult{}, PackageConfig{}, PackageInstalled{}, PackagesInstallArgs{}, PackagesRemoveArgs{}, PackagesSetEnabledArgs{}, PackagesEditResult{},
	Empty{}, Error{},
}

// tsConsts 要生成的常量组：名字 → 取值（顺序即输出顺序）。
var tsConsts = []struct {
	Name   string
	Values []string
}{
	{"ItemKinds", []string{KindUser, KindAssistant, KindToolResult, KindCompaction, KindContext, KindUnknown}},
	{"RunStates", []string{RunIdle, RunRunning}},
	{"ReasoningEfforts", ReasoningEfforts},
	{"Roles", []string{RoleViewer, RoleOperator}},
	{"InteractionKinds", []string{InteractionApproval, InteractionQuestion, InteractionChoice, InteractionForm, InteractionConfirm, InteractionPlan}},
	{"WorkModes", []string{WorkModeExecute, WorkModePlan}},
	{"PlanChoices", PlanChoices},
	{"Outcomes", []string{OutcomeApproved, OutcomeDenied, OutcomeAnswered, OutcomeExpired, OutcomeVoided}},
	{"DurableEvents", []string{EvEntryAppended, EvLogRewound, EvLogCompacted, EvStateChanged, EvModeChanged,
		EvSettingsChanged, EvInteractionOpen, EvInteractionDone, EvSessionClosed, EvSessionForked, EvSessionUpdated}},
	{"VolatileEvents", []string{EvTurnStarted, EvTurnEnded, EvTurnRejected, EvMessageStarted, EvMessageDelta,
		EvMessageEnded, EvMessageDiscarded, EvToolStarted, EvToolArgsDelta, EvToolRunning, EvToolProgress,
		EvToolEnded, EvNotice, EvSynced, EvLagged, EvSessionsChanged, EvModelRetrying, EvQueueChanged}},
	{"ErrorCodes", []string{CodeBadRequest, CodeInvalid, CodeNotFound, CodeClosed, CodeNotIdle, CodeBusy,
		CodeConflict, CodeDenied, CodeNeedConfirm, CodeCursorExpired, CodeUnauthorized, CodeForbidden,
		CodeUnsupported, CodeInternal}},
	{"ConflictReasons", []string{ConflictExists, ConflictAnswered, ConflictStale, ConflictLeased, ConflictFiles}},
	{"InvalidReasons", []string{InvalidOtherWorkspace}},
	{"ChangeScopes", []string{ChangeScopeSession, ChangeScopeTurn, ChangeScopeFromTurn, ChangeScopeGit}},
	{"SkillScopes", []string{SkillScopeGlobal, SkillScopeWorkspace}},
	{"MCPScopes", []string{MCPScopeGlobal, MCPScopeWorkspace}},
	{"Risks", []string{RiskExec, RiskNetwork, RiskWrite, RiskConfig, RiskPrompt, RiskCommands}},
	{"VersionCompares", []string{VersionNewer, VersionSame, VersionOlder, VersionUnknown}},
	{"MCPStates", []string{MCPStateConnected, MCPStateStarting, MCPStateFailed, MCPStateDisabled}},
	{"MCPTransports", []string{MCPTransportStdio, MCPTransportHTTP}},
	{"NoticeLevels", []string{NoticeInfo, NoticeWarn, NoticeError}},
	{"TrustLevels", []string{TrustTrusted, TrustUntrusted}},
}

var rawType = reflect.TypeOf(json.RawMessage(nil))

// tsFieldOverride 线上是 json.RawMessage、但语义已经定死的字段：TS 侧给出精确类型，
// 而不是 unknown。键是「类型名.字段名」。
//
// 为什么不改 Go 侧的字段类型：`UISlot.View` 是 json.RawMessage 有实际原因——视图既可能是
// 单个节点也可能是节点数组，且要原样透传给前端（占位替换在客户端做）。改成具体类型会
// 改变公开面且丢掉透传语义。TS 侧收窄没有这些约束。
var tsFieldOverride = map[string]string{
	"UISlot.view": "DeclNode | DeclNode[]",
	// map[string]*string：null = 保留已保存的值（见 MCPServerDraft）。
	"MCPServerDraft.env":     "Record<string, string | null>",
	"MCPServerDraft.headers": "Record<string, string | null>",
}

// tsFieldType 字段的 TS 类型：先查 override，再按 Go 类型推。
func tsFieldType(owner string, f tsField, named map[reflect.Type]bool) string {
	if t, ok := tsFieldOverride[owner+"."+f.name]; ok {
		return t
	}
	return tsOf(f.typ, named)
}

// tsName 线上类型在 TS 里的名字：Error 改叫 ProtocolError，免得遮住 JS 内置的 Error。
func tsName(t reflect.Type) string {
	if t.Name() == "Error" {
		return "ProtocolError"
	}
	return t.Name()
}

// TypeScript 生成 web/src/wire.gen.ts 的全文。
func TypeScript() string {
	named := map[reflect.Type]bool{}
	for _, v := range tsTypes {
		named[reflect.TypeOf(v)] = true
	}
	var b strings.Builder
	b.WriteString("// 由 kernel/appapi/wire 生成，不要手改。重新生成：cd kernel && go test ./appapi/wire -run TestTypeScript -update\n\n")
	fmt.Fprintf(&b, "export const PROTOCOL = %d;\n\n", Version)
	for _, c := range tsConsts {
		writeConst(&b, c.Name, c.Values)
	}
	writeTypes(&b, tsTypes, named)
	// 声明式视图的节点是**可辨识联合**（按 type 分派），不是普通 interface：
	// 渲染器要能靠 type 收窄，所以单独生成，形状由 declKinds 表驱动（declarative.go）。
	b.WriteString(declActionTS() + "\n\n")
	b.WriteString(declNodeTS() + "\n")
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeConst(b *strings.Builder, name string, values []string) {
	qs := make([]string, len(values))
	for i, v := range values {
		qs[i] = fmt.Sprintf("%q", v)
	}
	fmt.Fprintf(b, "export const %s = [%s] as const;\n", name, strings.Join(qs, ", "))
	fmt.Fprintf(b, "export type %s = (typeof %s)[number];\n\n", strings.TrimSuffix(name, "s"), name)
}

func writeTypes(b *strings.Builder, types []any, named map[reflect.Type]bool) {
	for _, v := range types {
		t := reflect.TypeOf(v)
		fmt.Fprintf(b, "export interface %s {\n", tsName(t))
		for _, f := range fields(t) {
			fmt.Fprintf(b, "  %s%s: %s;\n", f.name, map[bool]string{true: "?", false: ""}[f.optional], tsFieldType(tsName(t), f, named))
		}
		b.WriteString("}\n\n")
	}
}

// GenOptions Generate 的选项。
type GenOptions struct {
	// Header 文件头注释（不含 "// "，可多行）；空 = 一行「生成，不要手改」。
	Header string
	// Import 基础类型所在的 TS 模块（如 "@tenon/client"）；Base 里被引用到的类型从这里 import type。
	Import string
	// Base 已在 Import 模块里生成过的类型（如 wire.Empty{}、wire.Part{}）：字段引用它们时按名字引用、不内联。
	Base []any
}

// Generate 按 Go 类型生成 TS 声明（产品扩展自己的 wire 类型用，docs/61 3.3）：每个类型一个 interface，
// 字段规则与 wire.gen.ts 相同（omitempty 或指针 = 可选，json.RawMessage = unknown）。consts 的值须是 []string，
// 每组生成 `export const X = [...] as const` 与 `export type X去掉末尾s`；按名字排序输出。
// types 里没有、Base 里也没有的结构体内联成对象类型。
func Generate(types []any, consts map[string]any, opts GenOptions) string {
	named := map[reflect.Type]bool{}
	for _, v := range types {
		named[reflect.TypeOf(v)] = true
	}
	for _, v := range opts.Base {
		named[reflect.TypeOf(v)] = true
	}
	var body strings.Builder
	names := make([]string, 0, len(consts))
	for k := range consts {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		vs, ok := consts[k].([]string)
		if !ok {
			panic(fmt.Sprintf("wire.Generate: const group %s must be []string", k))
		}
		writeConst(&body, k, vs)
	}
	writeTypes(&body, types, named)
	text := body.String()

	var b strings.Builder
	header := opts.Header
	if header == "" {
		header = "生成的文件，不要手改。"
	}
	for _, l := range strings.Split(strings.TrimRight(header, "\n"), "\n") {
		b.WriteString("// " + l + "\n")
	}
	b.WriteString("\n")
	if opts.Import != "" {
		var used []string
		for _, v := range opts.Base {
			n := tsName(reflect.TypeOf(v))
			if tsRefers(text, n) && !slices.Contains(used, n) {
				used = append(used, n)
			}
		}
		sort.Strings(used)
		if len(used) > 0 {
			fmt.Fprintf(&b, "import type { %s } from %q;\n\n", strings.Join(used, ", "), opts.Import)
		}
	}
	b.WriteString(text)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// tsRefers 生成的 TS 里是否以类型名引用了 name（按标识符边界）。
func tsRefers(text, name string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		s, e := i+j, i+j+len(name)
		before := s == 0 || !isIdent(text[s-1])
		after := e == len(text) || !isIdent(text[e])
		// 字段名后面跟 ":" 或 "?:"，不是类型引用
		if before && after && !(e < len(text) && (text[e] == ':' || text[e] == '?')) {
			return true
		}
		i = e
	}
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

type tsField struct {
	name     string
	typ      reflect.Type
	optional bool
}

func fields(t reflect.Type) []tsField {
	var out []tsField
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = sf.Name
		}
		opt := strings.Contains(opts, "omitempty") || sf.Type.Kind() == reflect.Pointer
		out = append(out, tsField{name: name, typ: sf.Type, optional: opt})
	}
	return out
}

func tsOf(t reflect.Type, named map[reflect.Type]bool) string {
	if t == rawType {
		return "unknown"
	}
	switch t.Kind() {
	case reflect.Pointer:
		return tsOf(t.Elem(), named)
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return tsOf(t.Elem(), named) + "[]"
	case reflect.Map:
		return "Record<string, " + tsOf(t.Elem(), named) + ">"
	case reflect.Interface:
		return "unknown"
	case reflect.Struct:
		if named[t] {
			return tsName(t)
		}
		var parts []string
		for _, f := range fields(t) {
			parts = append(parts, fmt.Sprintf("%s%s: %s", f.name, map[bool]string{true: "?", false: ""}[f.optional], tsOf(f.typ, named)))
		}
		sort.Strings(parts)
		return "{ " + strings.Join(parts, "; ") + " }"
	}
	return "unknown"
}
