package toolhub

// Mortise docs/63 F2：工具清单与执行一致。
//   - 冻结清单里有、当前没有的工具报"已移除"，两边都没有才报"不存在"；
//   - schema 漂移：参数通过当前校验就执行，否则回当前 schema 摘要；
//   - 档位放宽后可见、但不在冻结清单里的工具：提示新会话生效。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

func okCall(ran *int) func(context.Context, *Cx, json.RawMessage) (Output, error) {
	return func(context.Context, *Cx, json.RawMessage) (Output, error) { *ran++; return Text("ok"), nil }
}

func specWith(name, schema string) session.ToolSpec {
	return session.ToolSpec{Name: name, Schema: json.RawMessage(schema)}
}

// 冻结里有、hub 已不登记：已移除；冻结里有、当前定义不再允许：已移除；两边都没有：不存在。
func TestF2_RemovedVsMissing(t *testing.T) {
	ran := 0
	h := New(Options{})
	_ = h.Register(&Tool{Spec: spec("read"), Call: okCall(&ran)})
	_ = h.Register(&Tool{Spec: spec("narrowed"), Call: okCall(&ran)})
	ts := h.SpecsFor([]string{"read"}) // narrowed 已不在当前定义里
	ts.SetFrozen([]session.ToolSpec{spec("read"), spec("narrowed"), spec("gone")})
	rs := drain(h.ExecuteIn(context.Background(), ts, kctx("s"), []session.ToolCall{
		call("1", "gone", `{}`), call("2", "narrowed", `{}`), call("3", "never", `{}`),
	}, nil))
	for i, want := range []string{"已移除", "已移除", "不存在"} {
		if rs[i].Output.Status != session.ToolError || !strings.Contains(text(rs[i].Output), want) {
			t.Fatalf("第 %d 个调用应报 %s：%q", i+1, want, text(rs[i].Output))
		}
	}
	if strings.Contains(text(rs[2].Output), "已移除") || ran != 0 {
		t.Fatalf("真不存在的不该报已移除，且都不应执行：%q ran=%d", text(rs[2].Output), ran)
	}
}

// schema 漂移：参数通过当前校验照常执行。
func TestF2_DriftArgsPassCurrent(t *testing.T) {
	ran := 0
	h := New(Options{})
	_ = h.Register(&Tool{Spec: specWith("w", `{"type":"object","properties":{"path":{"type":"string"},"mode":{"type":"string"}},"required":["path"]}`), Call: okCall(&ran)})
	ts := h.SpecsFor(nil)
	ts.SetFrozen([]session.ToolSpec{specWith("w", `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)})
	rs := drain(h.ExecuteIn(context.Background(), ts, kctx("s"), []session.ToolCall{call("1", "w", `{"path":"a"}`)}, nil))
	if ran != 1 || text(rs[0].Output) != "ok" {
		t.Fatalf("通过当前校验应执行：ran=%d %q", ran, text(rs[0].Output))
	}
}

// schema 漂移：参数不通过当前校验，回当前 schema 摘要让模型改参数重试。
func TestF2_DriftArgsFailCurrent(t *testing.T) {
	ran := 0
	h := New(Options{})
	_ = h.Register(&Tool{Spec: specWith("w", `{"type":"object","properties":{"file":{"type":"string"},"n":{"type":"integer"}},"required":["file"]}`), Call: okCall(&ran)})
	ts := h.SpecsFor(nil)
	ts.SetFrozen([]session.ToolSpec{specWith("w", `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)})
	rs := drain(h.ExecuteIn(context.Background(), ts, kctx("s"), []session.ToolCall{call("1", "w", `{"path":"a"}`)}, nil))
	out := text(rs[0].Output)
	if ran != 0 || rs[0].Output.Status != session.ToolError {
		t.Fatalf("不通过不应执行：ran=%d %q", ran, out)
	}
	for _, want := range []string{"参数格式已更新", "file: string（必填）", "n: integer", "重试"} {
		if !strings.Contains(out, want) {
			t.Fatalf("摘要缺 %q：%q", want, out)
		}
	}
}

// 没有漂移时参数错误仍是普通文案，不带"已更新"。
func TestF2_NoDriftPlainError(t *testing.T) {
	ran := 0
	h := New(Options{})
	sc := `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`
	_ = h.Register(&Tool{Spec: specWith("w", sc), Call: okCall(&ran)})
	ts := h.SpecsFor(nil)
	ts.SetFrozen([]session.ToolSpec{specWith("w", sc)})
	rs := drain(h.ExecuteIn(context.Background(), ts, kctx("s"), []session.ToolCall{call("1", "w", `{}`)}, nil))
	out := text(rs[0].Output)
	if ran != 0 || !strings.Contains(out, "缺少必填字段 path") || strings.Contains(out, "已更新") {
		t.Fatalf("普通参数错误：ran=%d %q", ran, out)
	}
}

// 顶层 properties 的 type 校验（B）：类型不符报错，不往下递归；integer 不收小数。
func TestF2_CheckArgsTopLevelTypes(t *testing.T) {
	sc := json.RawMessage(`{"type":"object","properties":{"s":{"type":"string"},"n":{"type":"number"},"i":{"type":"integer"},"b":{"type":"boolean"},"o":{"type":"object","properties":{"x":{"type":"string"}}},"a":{"type":"array"},"z":{"type":"null"},"u":{"type":["string","null"]}}}`)
	good := `{"s":"x","n":1.5,"i":2,"b":true,"o":{"x":1},"a":[],"z":null,"u":null}`
	if msg := checkArgs(sc, json.RawMessage(good)); msg != "" {
		t.Fatalf("合法参数被拒：%s", msg)
	}
	for args, field := range map[string]string{
		`{"s":1}`: "s", `{"n":"1"}`: "n", `{"i":1.5}`: "i", `{"b":"true"}`: "b",
		`{"o":[]}`: "o", `{"a":{}}`: "a", `{"z":0}`: "z", `{"u":3}`: "u",
	} {
		msg := checkArgs(sc, json.RawMessage(args))
		if !strings.Contains(msg, "字段 "+field) {
			t.Fatalf("%s 应报字段 %s 类型不符：%q", args, field, msg)
		}
	}
}

// 档位放宽：会话创建时被 deny（不在冻结清单），现在可见 → 新会话生效。
func TestF2_LoosenedLevelHint(t *testing.T) {
	h := New(Options{})
	_ = h.Register(&Tool{Spec: spec("read"), Call: okCall(new(int))})
	_ = h.Register(&Tool{Spec: spec("bash"), Call: okCall(new(int))})
	ts := h.SpecsFor(nil) // 当前档位 bash 可见
	ts.SetFrozen([]session.ToolSpec{spec("read")})
	hints := map[string]string{"1": "工具 bash 不存在，可用工具：read", "2": "工具 ghost 不存在，可用工具：read"}
	got := h.RewriteHints(ts, []session.ToolCall{call("1", "bash", `{}`), call("2", "ghost", `{}`)}, hints, map[string]bool{"1": true, "2": true})
	if !strings.Contains(got["1"], "新会话生效") {
		t.Fatalf("放宽后可见应提示新会话生效：%q", got["1"])
	}
	if got["2"] != hints["2"] {
		t.Fatalf("真不存在的保持原文案：%q", got["2"])
	}
}

// 档位仍判 deny：不提示新会话生效。
func TestF2_StillDeniedKeepsHint(t *testing.T) {
	h := New(Options{})
	_ = h.Register(&Tool{Spec: spec("bash"), Call: okCall(new(int))})
	_ = h.SetVisible(func(string) bool { return false })
	ts := h.SpecsFor(nil)
	ts.SetFrozen(nil)
	hints := map[string]string{"1": "工具 bash 不存在"}
	got := h.RewriteHints(ts, []session.ToolCall{call("1", "bash", `{}`)}, hints, map[string]bool{"1": true})
	if got["1"] != hints["1"] {
		t.Fatalf("仍 deny 不应改写：%q", got["1"])
	}
}
