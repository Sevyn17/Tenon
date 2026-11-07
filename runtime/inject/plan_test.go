package inject

import (
	"strings"
	"testing"

	"tenon/runtime/session"
)

// apply 把 Plan 的输出当作已写入历史的条目追加上去（模拟 Core.Inject）。
func apply(hist []*session.Entry, ws []Write) []*session.Entry {
	for _, w := range ws {
		hist = append(hist, &session.Entry{Item: session.Item{Kind: w.Kind}, Meta: w.Meta})
	}
	return hist
}

func TestPlanSendsOnceAndClears(t *testing.T) {
	items := []Item{
		{Key: "agents_md", Kind: KindSnapshot, Body: "规则"},
		{Key: "todo", Body: "1. 写测试"},
	}
	ws := Plan(items, nil, false)
	if len(ws) != 2 || ws[0].Kind != session.KindSnapshot || ws[1].Kind != session.KindStatus {
		t.Fatalf("首次应写快照 + 状态两条：%+v", ws)
	}
	hist := apply(nil, ws)

	// 不变：一条都不写。快照的 key 不能被当成"已清除"（P0-10）。
	if ws := Plan(items, hist, false); len(ws) != 0 {
		t.Fatalf("内容未变不应再写：%+v", ws)
	}

	// todo 撤销：只写一条已清除，快照不动。
	ws = Plan(items[:1], hist, false)
	if len(ws) != 1 || ws[0].Kind != session.KindStatus || !strings.Contains(ws[0].Text, `key="todo" cleared="true"`) {
		t.Fatalf("撤销应写一条已清除：%+v", ws)
	}
	if strings.Contains(ws[0].Text, "agents_md") {
		t.Fatalf("快照 key 不应出现在撤销里：%s", ws[0].Text)
	}
	hist = apply(hist, ws)
	if ws := Plan(items[:1], hist, false); len(ws) != 0 {
		t.Fatalf("已清除的 key 不应再次清除：%+v", ws)
	}

	// 压缩后重置：快照与状态都重发。
	if ws := Plan(items, hist, true); len(ws) != 2 {
		t.Fatalf("reset 应重发两条：%+v", ws)
	}
}

func TestPlanTruncatedNotResent(t *testing.T) {
	items := []Item{{Key: "big", Body: strings.Repeat("长", 2000), Budget: 50}}
	ws := Plan(items, nil, false)
	if len(ws) != 1 || !strings.Contains(ws[0].Text, "已截断") {
		t.Fatalf("超预算片段应截断后发出：%+v", ws)
	}
	if ws := Plan(items, apply(nil, ws), false); len(ws) != 0 {
		t.Fatalf("截断片段内容没变不应重发（P0-11）：%+v", ws)
	}
}

func TestPlanDroppedRetried(t *testing.T) {
	big := strings.Repeat("x", DefaultTotalBudget*4)
	items := []Item{
		{Key: "hi", Priority: 1, Body: big, Budget: DefaultTotalBudget},
		{Key: "lo", Body: "小"},
	}
	ws := Plan(items, nil, false)
	if len(ws) != 1 || ws[0].Meta[MetaDropped] == nil {
		t.Fatalf("超总预算应丢低优先级并记 dropped：%+v", ws)
	}
	// lo 被丢弃时没记哈希：下次只剩它待发。
	ws = Plan(items, apply(nil, ws), false)
	if len(ws) != 1 || !strings.Contains(ws[0].Text, `key="lo"`) || strings.Contains(ws[0].Text, `key="hi"`) {
		t.Fatalf("被丢的 key 下次应重试：%+v", ws)
	}
}
