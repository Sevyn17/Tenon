package wire

// TS 类型生成（06 10.3）：协议类型是唯一来源，web/src/client/wire.gen.ts 由这里生成，不再手写。
// 类型清单与常量清单都直接引用 Go 符号：改名、删字段时编译或固定用例测试会失败。

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Version 线上协议版本。
const Version = 2

// tsTypes 要生成的线上类型（按这个顺序输出）。
var tsTypes = []any{
	Entry{}, Item{}, Part{}, MediaRef{}, Reasoning{}, ToolCall{}, Usage{}, LogPos{}, Mode{}, Settings{},
	Interaction{}, Approval{}, Question{}, Choice{}, ChoiceOption{}, Form{}, Confirm{}, Live{}, LiveStream{}, LiveTool{}, SessionSnapshot{}, SessionInfo{},
	Principal{}, Stats{},
	Envelope{}, EntryAppended{}, LogRewound{}, LogCompacted{}, StateChanged{}, SettingsChanged{},
	InteractionResolved{}, SessionClosed{}, SessionForked{}, SessionUpdated{}, TurnStarted{}, TurnEnded{}, TurnRejected{},
	MessageStarted{}, MessageDelta{}, StreamRef{}, ToolStarted{}, ToolArgsDelta{}, ToolRunning{}, ToolProgress{},
	ToolEnded{}, Notice{}, CursorRef{}, SessionsChanged{},
	ProtocolRange{}, HelloArgs{}, HelloResult{}, SessionArgs{}, ListArgs{}, ListResult{}, UpdateArgs{}, FeedbackArgs{}, Feedback{}, FeedbackListResult{}, FollowupsArgs{}, FollowupsResult{}, CreateArgs{},
	SubscribeArgs{}, EntriesArgs{}, EntriesResult{}, SendArgs{}, SendResult{}, SteerArgs{}, RewindArgs{},
	RewindResult{}, CompactArgs{}, CompactResult{}, ForkArgs{}, SettingsArgs{}, SettingsResult{}, ModelInfo{}, ModelsListResult{}, Response{},
	RespondArgs{}, RespondResult{}, InteractionsResult{}, ModeInfo{}, ModeListResult{}, ModeSetArgs{},
	ModeSetResult{}, ModeShowArgs{}, ModeItem{}, ModeShowResult{}, ArtifactGetArgs{}, ArtifactGetResult{},
	ArtifactPutArgs{}, ArtifactPutResult{}, TraceArgs{}, TraceResult{}, FilesArgs{}, FileInfo{}, FilesResult{}, ConfigField{}, PluginInfo{},
	PluginPending{}, ProfileScope{}, PluginsListResult{}, PluginsSetArgs{}, PluginsSetResult{}, PackageInfo{}, UISlot{}, UIData{}, PackagesListResult{}, Empty{}, Error{},
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
	{"InteractionKinds", []string{InteractionApproval, InteractionQuestion, InteractionChoice, InteractionForm, InteractionConfirm}},
	{"Outcomes", []string{OutcomeApproved, OutcomeDenied, OutcomeAnswered, OutcomeExpired, OutcomeVoided}},
	{"DurableEvents", []string{EvEntryAppended, EvLogRewound, EvLogCompacted, EvStateChanged, EvModeChanged,
		EvSettingsChanged, EvInteractionOpen, EvInteractionDone, EvSessionClosed, EvSessionForked, EvSessionUpdated}},
	{"VolatileEvents", []string{EvTurnStarted, EvTurnEnded, EvTurnRejected, EvMessageStarted, EvMessageDelta,
		EvMessageEnded, EvMessageDiscarded, EvToolStarted, EvToolArgsDelta, EvToolRunning, EvToolProgress,
		EvToolEnded, EvNotice, EvSynced, EvLagged, EvSessionsChanged}},
	{"ErrorCodes", []string{CodeBadRequest, CodeInvalid, CodeNotFound, CodeClosed, CodeNotIdle, CodeBusy,
		CodeConflict, CodeDenied, CodeNeedConfirm, CodeCursorExpired, CodeUnauthorized, CodeForbidden,
		CodeUnsupported, CodeInternal}},
	{"ConflictReasons", []string{ConflictExists, ConflictAnswered, ConflictStale, ConflictLeased}},
}

var rawType = reflect.TypeOf(json.RawMessage(nil))

// tsName 线上类型在 TS 里的名字：Error 改叫 ProtocolError，免得遮住 JS 内置的 Error。
func tsName(t reflect.Type) string {
	if t.Name() == "Error" {
		return "ProtocolError"
	}
	return t.Name()
}

// TypeScript 生成 web/src/client/wire.gen.ts 的全文。
func TypeScript() string {
	named := map[reflect.Type]bool{}
	for _, v := range tsTypes {
		named[reflect.TypeOf(v)] = true
	}
	var b strings.Builder
	b.WriteString("// 由 kernel/appapi/wire 生成，不要手改。重新生成：cd kernel && go test ./appapi/wire -run TestTypeScript -update\n\n")
	fmt.Fprintf(&b, "export const PROTOCOL = %d;\n\n", Version)
	for _, c := range tsConsts {
		qs := make([]string, len(c.Values))
		for i, v := range c.Values {
			qs[i] = fmt.Sprintf("%q", v)
		}
		fmt.Fprintf(&b, "export const %s = [%s] as const;\n", c.Name, strings.Join(qs, ", "))
		fmt.Fprintf(&b, "export type %s = (typeof %s)[number];\n\n", strings.TrimSuffix(c.Name, "s"), c.Name)
	}
	for _, v := range tsTypes {
		t := reflect.TypeOf(v)
		fmt.Fprintf(&b, "export interface %s {\n", tsName(t))
		for _, f := range fields(t) {
			fmt.Fprintf(&b, "  %s%s: %s;\n", f.name, map[bool]string{true: "?", false: ""}[f.optional], tsOf(f.typ, named))
		}
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
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
