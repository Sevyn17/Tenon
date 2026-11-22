package checkpoint

// 检查点按工具属性判断、不认工具名（Mortise docs/61 T3 前置、docs/53 §7）：产品层的同类工具
// （自己的命令执行工具、自己的后台任务读取工具）只要声明同样的属性就能接入扫描与回合末补扫。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// toolCall 以任意工具名与属性走一次中间件；run 是工具本身。
func (h *harness) toolCall(sid, tool string, risk toolhub.Risk, attrs map[string]any, args string, run func()) {
	h.t.Helper()
	h.n++
	req := component.ToolCallReq{Tool: tool, Args: json.RawMessage(args), SessionID: sid, Risk: risk,
		CallID: "c" + itoa(h.n), Attrs: attrs, Scope: h.scope}
	if _, err := h.e.Middleware()(context.Background(), req,
		func(context.Context, component.ToolCallReq) (component.ToolCallResp, error) {
			run()
			return component.ToolCallResp{Output: toolhub.Text("ok")}, nil
		}); err != nil {
		h.t.Fatal(err)
	}
}

// 自定义的 may_write_files 后台工具：调用返回（done:false）后才写的文件，回合末补扫记为改动。
func TestCustomBackgroundToolRescan(t *testing.T) {
	h := newHarness(t)
	h.e.NoGit = true
	defer h.e.Close()
	runAttrs := map[string]any{toolhub.AttrMayWrite: true, toolhub.AttrCommandArg: "command"}
	h.toolCall("s1", "custom_run", toolhub.Dangerous, runAttrs, `{"command":"make"}`, func() {}) // done:false，立即返回
	h.put("built.txt", "from background\n")
	h.e.AfterTurn("s1", session.EndReason{})
	cs := h.list("s1")
	if len(cs) != 1 || cs[0].Path != "built.txt" || cs[0].Source != SourceShell || cs[0].CallID != "turn-1/scan" || cs[0].Turn != 1 {
		t.Fatalf("回合末应补记 built.txt：%+v", cs)
	}

	// 第 2 回合只调了声明 background_task 的读取工具（Read 风险、没有 may_write_files）：照样补扫。
	h.scope.turn = 2
	h.toolCall("s1", "custom_read", toolhub.Read, map[string]any{toolhub.AttrBackgroundTask: true}, `{}`, func() {})
	h.put("built.txt", "more\n")
	h.e.AfterTurn("s1", session.EndReason{})
	if cs := h.list("s1"); len(cs) != 2 || cs[1].CallID != "turn-2/scan" || cs[1].Turn != 2 {
		t.Fatalf("background_task 工具的回合应补扫：%+v", cs)
	}

	// 第 3 回合同名但没声明属性的工具：不补扫（只看属性，不看名字）。
	h.scope.turn = 3
	h.toolCall("s1", "job_output", toolhub.Read, nil, `{}`, func() {})
	h.put("built.txt", "user\n")
	h.e.AfterTurn("s1", session.EndReason{})
	if cs := h.list("s1"); len(cs) != 2 {
		t.Fatalf("没声明属性的工具不应触发补扫：%+v", cs)
	}
}

// 自定义命令工具声明 command_arg 后，只读命令同样免扫，回合末兜底。
func TestCustomCommandToolReadOnly(t *testing.T) {
	h := newHarness(t)
	h.e.NoGit = true
	defer h.e.Close()
	h.put("a.txt", "a\n")
	attrs := map[string]any{toolhub.AttrMayWrite: true, toolhub.AttrCommandArg: "line"}
	h.toolCall("s1", "custom_run", toolhub.Dangerous, attrs, `{"line":"touch x"}`, func() {}) // 建立快照
	full := h.e.treeFor().Stats()
	h.toolCall("s1", "custom_run", toolhub.Dangerous, attrs, `{"line":"cat a.txt"}`, func() { h.put("sneaky.txt", "x\n") })
	if st := h.e.treeFor().Stats(); st != full {
		t.Fatalf("只读命令不应扫描：%+v → %+v", full, st)
	}
	h.e.AfterTurn("s1", session.EndReason{})
	if cs := h.list("s1"); len(cs) != 1 || cs[0].Path != "sneaky.txt" {
		t.Fatalf("回合末补扫应兜底记下：%+v", cs)
	}
}
