package permission

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 确认原因的结构化字段（Mortise t3-shots #1）：按权限项判定时带档位 id、内置标记、权限项 id 与判定结果；
// 由智能体上限判定时另带上限档位；Reason 文本照旧作兜底。
func TestDecisionDetail(t *testing.T) {
	a := Authorizer(func(string) Policy { return Policy{Mode: Ask} })
	c := eff("write_file", toolhub.Write, toolhub.EffectWrite)
	d, err := a.Authorize(context.Background(), c, json.RawMessage(`{"path":"b.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != toolhub.DecisionConfirm || d.Detail == nil || d.Reason == "" {
		t.Fatalf("确认应带结构化原因与文本兜底：%+v", d)
	}
	x := d.Detail
	if x.Mode != string(Ask) || !x.ModeBuiltin || x.Item != "t.write_file" || x.Action != string(AskUser) {
		t.Fatalf("结构化原因字段不对：%+v", x)
	}
	// 智能体上限更严：detail 标出上限档位
	p := Policy{Mode: Full, Cap: &Policy{Mode: Ask}}
	dd := p.Decide(c, json.RawMessage(`{"path":"b.txt"}`))
	if dd.Detail == nil || dd.Detail.Cap != string(Ask) {
		t.Fatalf("上限判定应带 cap：%+v", dd)
	}
	// 规则命中没有结构化原因，界面用 Reason
	r := Policy{Mode: Ask, Rules: []Rule{{Tool: "write_file", Action: AskUser}}}
	if rd := r.Decide(c, json.RawMessage(`{"path":"b.txt"}`)); rd.Detail != nil {
		t.Fatalf("规则命中不应带结构化原因：%+v", rd)
	}
}
