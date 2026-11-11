package events_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/tenontest"
)

// 直接让 router 定义在一次 Ask 里调 event_dispatch，看工具是否真的执行。
func TestDispatchToolViaAgent(t *testing.T) {
	app := tenontest.New(t,
		tenon.WithConfig("permission", map[string]any{"force_mode": "full"}),
		tenon.WithAgent(tenon.Agent{Name: "router", Prompt: "分发"}),
		tenontest.Model(
			tenontest.Reply("分发").CallTool("event_dispatch", `{"to":"agent:worker","body":"X"}`),
			tenontest.Reply("done"),
			tenontest.Reply("worker ok"),
		))
	_, err := app.Ask(context.Background(), "router", "分发事件", nil)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	app.AssertToolCalled("event_dispatch")
}

// 端到端：请求投给 agent:router，router 回合里 event_dispatch 到 agent:worker，worker 会话建出并含正文。
// （外部事件一律不可信：allow 工具会被降级为确认，无界面时拒绝——这是权限边界，见 T5b 方案不做项；
// 这里用可信触发证明 Router→Worker 的分发链路本身。）
func TestRouterForwardsToWorker(t *testing.T) {
	app := tenontest.New(t,
		tenon.WithConfig("permission", map[string]any{"force_mode": "full"}),
		tenon.WithAgent(
			tenon.Agent{Name: "router", Prompt: "分发"},
			tenon.Agent{Name: "worker", Prompt: "处理"},
		),
		tenontest.Model(
			tenontest.Reply("分发").CallTool("event_dispatch", `{"to":"agent:worker","body":"订单 7 已支付"}`),
			tenontest.Reply("done"),
			tenontest.Reply("worker ok"),
		))
	ctx := context.Background()
	if err := app.Internals().Host.Deliver(ctx, mail.Envelope{
		From:   mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:     mail.Address{Kind: mail.AddrAgent, ID: "router"},
		Kind:   mail.KindRequest,
		Mode:   mail.ModeWake,
		Origin: mail.OriginHuman,
		Body:   "订单 7 已支付",
	}); err != nil {
		t.Fatalf("投请求：%v", err)
	}
	db := app.Internals().DB
	var workerOK bool
	for i := 0; i < 600; i++ {
		infos, _ := db.ListSessions(ctx, 0, 100, true)
		for _, si := range infos {
			l, err := db.Load(ctx, si.ID, nil)
			if err != nil || l.Meta.AgentID != "worker" {
				continue
			}
			h, _ := app.History(ctx, si.ID)
			for _, e := range h {
				if e.Item.User != nil {
					for _, p := range e.Item.User.Content {
						if strings.Contains(p.Text, "订单 7 已支付") {
							workerOK = true
						}
					}
				}
			}
		}
		if workerOK {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !workerOK {
		t.Fatal("worker 会话应含 router 分发的事件正文")
	}
}
