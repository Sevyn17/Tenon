package appapi

import (
	"context"
	"strings"
	"testing"

	"tenon/runtime/store"
	"tenon/runtime/store/memstore"
)

// RenderTrace 按时间列出事件，种类换成中文标签，会话汇总在头部（从 host.ExplainTrace 移来）。
func TestRenderTrace(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	for i, ev := range []store.TraceEvent{
		{Trace: "tr", Session: "s1", Kind: "envelope", Detail: "human:u1 -> session:s1", At: 1},
		{Trace: "tr", Session: "s1", Kind: "tool", Detail: "echo ok", At: 2},
		{Trace: "tr", Session: "", Kind: "guard_denied", Detail: "hop", At: 3},
	} {
		if err := db.RecordTrace(ctx, ev); err != nil {
			t.Fatal(i, err)
		}
	}
	out, err := RenderTrace(ctx, db, "tr")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"trace tr", "会话：s1", "信封", "工具", "echo", "拒绝", "hop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q：\n%s", want, out)
		}
	}
	if out, _ := RenderTrace(ctx, db, "none"); !strings.Contains(out, "没有记录") {
		t.Fatalf("没有事件时应说明：%s", out)
	}
}
