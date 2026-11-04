package compactbasic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ybase/runtime/session"
)

func txt(s string) session.Content { return session.Content{session.NewTextPart(s)} }

// turns 造 n 个回合，每回合用户与助手各一条长文本。
func turns(t *testing.T, n int) *session.Core {
	t.Helper()
	c := session.New(&session.Config{Model: "m", System: []session.SystemBlock{{Text: "sys"}}, Policy: session.DefaultPolicy()})
	long := strings.Repeat("内容", 200)
	for i := 0; i < n; i++ {
		need, err := c.Input(txt("问"+long), map[string]any{session.MetaSource: "user"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ModelDone(need.Model.RequestID, need.ID, session.AssistantItem{Content: txt("答" + long)}, session.StopReason{V: "EndTurn"}); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func fake(out string, calls *int) Summarizer {
	return func(ctx context.Context, req session.Request) (string, error) {
		*calls++
		last := req.Messages[len(req.Messages)-1]
		if last.Content[0].Text != Instruction {
			return "", errors.New("最后一条应是摘要指令")
		}
		return out, nil
	}
}

func TestThreshold(t *testing.T) {
	c := &Compactor{Window: 128000, MaxOutput: 8192}
	// min(W×0.8=102400, W−O−B=103424)
	if got := c.Threshold(); got != 102400 {
		t.Fatalf("threshold=%d", got)
	}
	if (&Compactor{}).Threshold() != 0 {
		t.Fatal("窗口未知应为 0")
	}
}

func TestCutPointKeepsTail(t *testing.T) {
	core := turns(t, 6)
	per := entryTokens(core.Context().Items[0])
	u := CutPoint(core, per*3)
	if u <= 0 || !core.CanCompact(u) {
		t.Fatalf("upto=%d", u)
	}
	if end := len(core.Context().Items); end-u < 3 {
		t.Fatalf("保留不足: upto=%d end=%d", u, end)
	}
	if CutPoint(core, 1<<30) != -1 {
		t.Fatal("保留量超过全部应无切点")
	}
}

func TestPlanThresholdAndCompact(t *testing.T) {
	core := turns(t, 6)
	n := 0
	c := &Compactor{Auto: true, Window: 100000, MaxOutput: 1000, Summarize: fake("摘要", &n)}
	if v, _, err := c.Plan(context.Background(), "s", core, false); err != nil || v != nil || n != 0 {
		t.Fatalf("低于阈值不应压缩: %v %v %d", v, err, n)
	}
	c.Window, c.Headroom, c.KeepRatio = 4000, 1, 0.3
	v, meta, err := c.Plan(context.Background(), "s", core, false)
	if err != nil || v == nil || n != 1 {
		t.Fatalf("应压缩: %v %v %d", v, err, n)
	}
	if !strings.HasPrefix(v.Summary[0].Text, "<compacted-summary>") || meta[session.MetaSource] != "compact-basic" {
		t.Fatalf("摘要或 meta 不符: %+v %v", v, meta)
	}
	if err := core.Compact(v.Summary, v.Upto, meta); err != nil {
		t.Fatal(err)
	}
	req, err := session.BuildRequest(core.Context())
	if err != nil || req.Messages[0].Content[0].Text != v.Summary[0].Text {
		t.Fatalf("压缩后首条应为摘要: %v", err)
	}
	// 再次压缩：下界在上次压缩条目之后，旧摘要被并入新摘要的输入。
	if CutPoint(core, 1) != -1 {
		t.Fatal("压缩条目之后无新内容时应无切点")
	}
	need, _ := core.Input(txt("新问题"), map[string]any{session.MetaSource: "user"})
	core.ModelDone(need.Model.RequestID, need.ID, session.AssistantItem{Content: txt("新回答")}, session.StopReason{V: "EndTurn"})
	if u := CutPoint(core, 1); u <= v.Upto {
		t.Fatalf("二次切点应在压缩条目之后: %d <= %d", u, v.Upto)
	}
}

func TestBeforeCompactDenyAndNoShrink(t *testing.T) {
	core := turns(t, 4)
	n := 0
	c := &Compactor{Summarize: fake("x", &n), Before: []BeforeCompact{func(context.Context, string, int) error { return errors.New("不行") }}}
	if _, _, err := c.Plan(context.Background(), "s", core, true); !errors.Is(err, ErrDenied) || n != 0 {
		t.Fatalf("应被拒绝且不花钱: %v %d", err, n)
	}
	c.Before = nil
	c.Summarize = fake(strings.Repeat("长", 100000), &n)
	if _, _, err := c.Plan(context.Background(), "s", core, true); !errors.Is(err, ErrNoShrink) {
		t.Fatalf("应拒绝不变短的摘要: %v", err)
	}
	c.Summarize = fake("  ", &n)
	if _, _, err := c.Plan(context.Background(), "s", core, true); !errors.Is(err, ErrEmpty) {
		t.Fatalf("应拒绝空摘要: %v", err)
	}
}
