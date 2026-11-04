package session

// session-core C1–C3：请求构造单一路径的对拍测试。
//
// 不变式：对任意合法日志与合法 upto，
//   BuildWithCompaction(ctx, s, upto) == Compact(s, upto) 之后的 BuildRequest(ctx)（JSON 逐字节）。
// BuildBranch 的前缀与 BuildRequest 逐字节一致。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// randomTurn 在 Idle 下随机推进一个回合（只记录 / 纯文本回复 / 带 1–2 个工具调用的两步回合）。
func randomTurn(t *testing.T, h *harness, r *rand.Rand, n int) {
	t.Helper()
	switch r.Intn(3) {
	case 0:
		if err := h.core.Record(txt(fmt.Sprintf("note-%d", n)), nil); err != nil {
			t.Fatalf("record: %v", err)
		}
	case 1:
		need, err := h.core.Input(txt(fmt.Sprintf("q-%d", n)), nil)
		if err != nil {
			t.Fatalf("input: %v", err)
		}
		if _, err := h.core.ModelDone(need.Model.RequestID, need.ID, AssistantItem{Content: txt(fmt.Sprintf("a-%d", n))}, StopReason{V: "EndTurn"}); err != nil {
			t.Fatalf("model done: %v", err)
		}
	default:
		need, err := h.core.Input(txt(fmt.Sprintf("q-%d", n)), nil)
		if err != nil {
			t.Fatalf("input: %v", err)
		}
		k := 1 + r.Intn(2)
		calls := make([]ToolCall, k)
		for i := range calls {
			calls[i] = ToolCall{Name: "echo", Args: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))}
		}
		need, err = h.core.ModelDone(need.Model.RequestID, need.ID, AssistantItem{ToolCalls: calls}, StopReason{V: "ToolUse"})
		if err != nil {
			t.Fatalf("model done tools: %v", err)
		}
		for need.Kind == NeedTools {
			c := need.Tools.Calls[0]
			need, err = h.core.SubmitResult(c.ID, txt("r-"+c.ID), ToolOK)
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
		}
		if need.Kind != NeedModel {
			t.Fatalf("工具结果到齐后应进入下一步: %+v", need)
		}
		if _, err := h.core.ModelDone(need.Model.RequestID, need.ID, AssistantItem{Content: txt(fmt.Sprintf("a-%d", n))}, StopReason{V: "EndTurn"}); err != nil {
			t.Fatalf("model done final: %v", err)
		}
	}
}

func TestBuildWithCompactionMatchesCompact(t *testing.T) {
	cases := 0
	for seed := int64(1); seed <= 60; seed++ {
		r := rand.New(rand.NewSource(seed))
		h := newHarness(t, 8)
		for n := 0; n < 12; n++ {
			randomTurn(t, h, r, n)
			if r.Intn(2) == 0 {
				continue
			}
			ctx := h.core.Context()
			lo := h.core.hdr.CompactionBase
			upto := lo + r.Intn(h.core.absLen()-lo+1)
			summary := txt(fmt.Sprintf("sum-%d-%d", seed, n))
			predicted, err := BuildWithCompaction(ctx, summary, upto)
			if err != nil {
				t.Fatalf("seed %d: BuildWithCompaction(%d): %v", seed, upto, err)
			}
			if err := h.core.Compact(summary, upto, nil); err != nil {
				continue // upto 落在调用与结果之间等非法位置，内核拒绝即可
			}
			actual, err := BuildRequest(h.core.Context())
			if err != nil {
				t.Fatalf("seed %d: BuildRequest: %v", seed, err)
			}
			if !bytes.Equal(mustJSON(t, predicted), mustJSON(t, actual)) {
				t.Fatalf("seed %d n %d upto %d：预演与生效后不一致\npredicted=%s\nactual=%s",
					seed, n, upto, mustJSON(t, predicted), mustJSON(t, actual))
			}
			cases++
		}
	}
	if cases < 200 {
		t.Fatalf("有效对拍用例只有 %d 个，少于 200", cases)
	}
}

// C2 回归：压缩点之后、压缩条目之前的消息必须保留。
func TestCompactKeepsTail(t *testing.T) {
	h := newHarness(t, 8)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		if err := h.core.Record(txt(s), nil); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if err := h.core.Compact(txt("sum"), 3, nil); err != nil {
		t.Fatalf("compact: %v", err)
	}
	req, err := BuildRequest(h.core.Context())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got []string
	for _, m := range req.Messages {
		got = append(got, m.Content[0].Text)
	}
	if fmt.Sprint(got) != "[sum d e]" {
		t.Fatalf("压缩后应为 [sum d e]，得到 %v", got)
	}
}

func TestBuildBranchPrefix(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	h := newHarness(t, 8)
	for n := 0; n < 6; n++ {
		randomTurn(t, h, r, n)
	}
	ctx := h.core.Context()
	base, _ := BuildRequest(ctx)
	branch, err := BuildBranch(ctx, txt("extra"))
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	if len(branch.Messages) != len(base.Messages)+1 {
		t.Fatalf("旁支应多一条消息")
	}
	branch.Messages = branch.Messages[:len(base.Messages)]
	if !bytes.Equal(mustJSON(t, base), mustJSON(t, branch)) {
		t.Fatalf("旁支前缀与 BuildRequest 不一致")
	}
}

func TestRequestHashDeterministic(t *testing.T) {
	h := newHarness(t, 8)
	if err := h.core.Record(txt("a"), nil); err != nil {
		t.Fatal(err)
	}
	r1, _ := BuildRequest(h.core.Context())
	r2, _ := BuildRequest(h.core.Context())
	h1, err := RequestHash(r1)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := RequestHash(r2)
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("哈希应确定且为 64 位十六进制: %s %s", h1, h2)
	}
	r2.Messages = append(r2.Messages, Message{Role: "user", Content: txt("b")})
	if h3, _ := RequestHash(r2); h3 == h1 {
		t.Fatalf("请求变化后哈希应变化")
	}
}

func TestAddToolsAppendOnlyDedup(t *testing.T) {
	h := newHarness(t, 8)
	n0 := len(h.core.hdr.Config.Tools)
	add := []ToolSpec{{Name: "echo"}, {Name: "grep"}, {Name: "grep"}}
	if _, err := h.core.SetConfig(ConfigPatch{AddTools: add}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	got := h.core.hdr.Config.Tools
	if len(got) != n0+1 || got[len(got)-1].Name != "grep" {
		t.Fatalf("AddTools 应只追加未存在的工具: %+v", got)
	}
}
