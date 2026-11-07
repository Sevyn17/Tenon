package reqlog

import (
	"encoding/json"
	"errors"
	"testing"

	"tenon/runtime/session"
)

func txt(s string) session.Content { return session.Content{session.NewTextPart(s)} }

func fixture(t *testing.T) (*session.Core, session.Context) {
	t.Helper()
	c := session.New(&session.Config{
		Model:  "m",
		System: []session.SystemBlock{{Text: "sys", Source: "agent"}, {Text: "agents.md 内容", Source: "plugin:agents-md"}},
		Tools: []session.ToolSpec{
			{Name: "read", Schema: json.RawMessage(`{"type":"object"}`)},
			{Name: "shell", Schema: json.RawMessage(`{"type":"object"}`)},
		},
		Policy: session.DefaultPolicy(),
	})
	for _, s := range []string{"a", "b", "c"} {
		if err := c.Record(txt(s), nil); err != nil {
			t.Fatal(err)
		}
	}
	return c, c.Context()
}

func TestBuildRebuildRoundTrip(t *testing.T) {
	_, ctx := fixture(t)
	base, _ := session.BuildRequest(ctx)
	// 注入已全部进内核历史（12.4 C4）：最终请求就是基础请求，没有「后缀」这一段。
	r, err := Build("req-1", ctx, nil, base, base)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.PrefixBase != 0 || r.PrefixEnd != 3 {
		t.Fatalf("记录不符: %+v", r)
	}
	got, err := Rebuild(r, ctx)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if !sameJSON(got, base) {
		t.Fatalf("重组不等于最终请求")
	}
	if r.Composition.Parts["system:plugin:agents-md"] == 0 || r.Composition.Parts["tools"] == 0 {
		t.Fatalf("组成统计缺类别: %+v", r.Composition)
	}
}

func TestRebuildWithCompactionView(t *testing.T) {
	_, ctx := fixture(t)
	view := &CompactionView{Summary: txt("摘要"), Upto: 2}
	base, _ := session.BuildWithCompaction(ctx, view.Summary, view.Upto)
	r, err := Build("req-2", ctx, view, base, base)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Rebuild(r, ctx)
	if err != nil || !sameJSON(got, base) {
		t.Fatalf("压缩视图重组失败: %v", err)
	}
	if r.Composition.Parts["compaction_summary"] == 0 {
		t.Fatalf("应统计压缩摘要: %+v", r.Composition)
	}
}

func TestShapeViolations(t *testing.T) {
	_, ctx := fixture(t)
	base, _ := session.BuildRequest(ctx)

	mod := base
	mod.Messages = append([]session.Message(nil), base.Messages...)
	mod.Messages[1] = session.Message{Role: "user", Content: txt("篡改")}
	if _, err := Build("x", ctx, nil, base, mod); !errors.Is(err, ErrShape) {
		t.Fatalf("改写前缀应拒绝: %v", err)
	}

	cut := base
	cut.Messages = base.Messages[:1]
	if _, err := Build("x", ctx, nil, base, cut); !errors.Is(err, ErrShape) {
		t.Fatalf("删减消息应拒绝: %v", err)
	}

	wide := base
	wide.Tools = append(append([]session.ToolSpec(nil), base.Tools...), session.ToolSpec{Name: "evil"})
	if _, err := Build("x", ctx, nil, base, wide); !errors.Is(err, ErrShape) {
		t.Fatalf("放宽工具应拒绝: %v", err)
	}
}

func TestRebuildMismatchAfterHistoryChange(t *testing.T) {
	c, ctx := fixture(t)
	base, _ := session.BuildRequest(ctx)
	r, err := Build("req-3", ctx, nil, base, base)
	if err != nil {
		t.Fatal(err)
	}
	r.Shell.Model = "other" // 外壳被改：哈希必须不符
	if _, err := Rebuild(r, c.Context()); !errors.Is(err, ErrMismatch) {
		t.Fatalf("应报哈希不符: %v", err)
	}
	r2 := *r
	r2.PrefixEnd = 99
	if _, err := Rebuild(&r2, c.Context()); !errors.Is(err, ErrMismatch) {
		t.Fatalf("区间越界应报不符: %v", err)
	}
}
