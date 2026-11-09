package tenon_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/tenontest"
)

type orderArgs struct {
	ID    string   `json:"id" desc:"订单号"`
	Carry *bool    `json:"carry"`
	Tags  []string `json:"tags,omitempty" enum:"a,b"`
}

func orderTool(calls *[]string) tenon.Tool {
	return tenon.Func("order_query", "按订单号查订单状态", func(ctx context.Context, a orderArgs) (any, error) {
		ci, _ := tenon.CallInfoFrom(ctx)
		*calls = append(*calls, a.ID+"@"+ci.Session)
		return map[string]string{"id": a.ID, "status": "已发货"}, nil
	})
}

// schema 按结构体字段生成：json 名、desc、enum、必填（非指针且没有 omitempty）。
func TestSchemaOf(t *testing.T) {
	var s map[string]any
	if err := json.Unmarshal([]byte(tenon.SchemaOf[orderArgs]()), &s); err != nil {
		t.Fatal(err)
	}
	props := s["properties"].(map[string]any)
	if props["id"].(map[string]any)["description"] != "订单号" || props["carry"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("字段：%v", props)
	}
	if e := props["tags"].(map[string]any)["enum"].([]any); len(e) != 2 {
		t.Fatalf("enum：%v", props["tags"])
	}
	if req := s["required"].([]any); len(req) != 1 || req[0] != "id" {
		t.Fatalf("必填：%v", s["required"])
	}
}

// 不给任何定义、不给名字：用内置缺省智能体，它能用 WithTools 的工具，拿不到 shell、写文件这类工具。
func TestDefaultAgentWithTools(t *testing.T) {
	var calls []string
	app := tenontest.New(t, tenon.WithTools(orderTool(&calls)),
		tenontest.Model(tenontest.Reply("查一下").CallTool("order_query", `{"id":"A1"}`), tenontest.Reply("A1 已发货")))
	r, err := app.Ask(context.Background(), "", "A1 到哪了", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "A1 已发货" || len(calls) != 1 || calls[0] != "A1@"+r.SessionID {
		t.Fatalf("回答 %q，调用 %v", r.Text, calls)
	}
	h, _ := app.History(context.Background(), r.SessionID)
	var tools []string
	for _, e := range h {
		if e.Item.Kind == "assistant" && e.Item.Assistant != nil {
			for _, c := range e.Item.Assistant.ToolCalls {
				tools = append(tools, c.Name)
			}
		}
	}
	if !slices.Contains(tools, "order_query") {
		t.Fatalf("工具调用：%v", tools)
	}
}

func TestDefaultAgentExcludesWriteTools(t *testing.T) {
	app := tenontest.New(t, tenontest.Model(tenontest.Reply("写").CallTool("write_file", `{"path":"a.txt","content":"x"}`), tenontest.Reply("完")))
	r, err := app.Ask(context.Background(), "", "写个文件", nil)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := app.History(context.Background(), r.SessionID)
	for _, e := range h {
		if e.Item.ToolResult != nil && e.Item.ToolResult.Status.State == "ok" {
			t.Fatalf("缺省智能体不该能写文件：%+v", e.Item.ToolResult)
		}
	}
}

// 代码里定义智能体；不给名字时：一个定义用它，多个报错列出名字，WithDefaultAgent 指定。
func TestWithAgentResolution(t *testing.T) {
	one := tenontest.New(t, tenon.WithAgent(tenon.Agent{Name: "support", Prompt: "你是客服"}), tenontest.Model(tenontest.Reply("你好")))
	s, err := one.NewSession(context.Background(), "", nil)
	if err != nil || !strings.HasPrefix(s.ID(), "ag-") {
		t.Fatalf("一个定义：%v %v", s, err)
	}
	two := tenontest.New(t, tenon.WithAgent(tenon.Agent{Name: "a"}, tenon.Agent{Name: "b"}), tenontest.Model())
	if _, err := two.NewSession(context.Background(), "", nil); !errors.Is(err, tenon.ErrInvalid) || !strings.Contains(err.Error(), "a、b") {
		t.Fatalf("两个定义不给名字应报错并列出名字：%v", err)
	}
	pick := tenontest.New(t, tenon.WithAgent(tenon.Agent{Name: "a"}, tenon.Agent{Name: "b"}), tenon.WithDefaultAgent("b"),
		tenontest.Model(tenontest.Reply("b 在")))
	r, err := pick.Ask(context.Background(), "", "在吗", nil)
	if err != nil || r.Text != "b 在" {
		t.Fatalf("WithDefaultAgent：%v %v", r, err)
	}
}

// 多轮：同一个会话接着聊；按 id 找回会话再聊。
func TestSessionMultiTurn(t *testing.T) {
	app := tenontest.New(t, tenontest.Model(tenontest.Reply("一"), tenontest.Reply("二"), tenontest.Reply("三")))
	ctx := context.Background()
	s, err := app.NewSession(ctx, "", tenon.Meta{"user": "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.Send(ctx, "第一句"); err != nil || r.Text != "一" || r.SessionID != s.ID() {
		t.Fatalf("第一轮：%v %v", r, err)
	}
	if r, err := s.Send(ctx, "第二句"); err != nil || r.Text != "二" {
		t.Fatalf("第二轮：%v %v", r, err)
	}
	back, err := app.Session(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if r, err := back.Send(ctx, "第三句"); err != nil || r.Text != "三" {
		t.Fatalf("找回后第三轮：%v %v", r, err)
	}
	h, _ := s.History(ctx)
	users := 0
	for _, e := range h {
		if e.Item.Kind == "user" {
			users++
		}
	}
	if users != 3 {
		t.Fatalf("同一会话应有 3 条用户消息，得 %d", users)
	}
	if _, err := app.Session(ctx, "nope"); !errors.Is(err, tenon.ErrNotFound) {
		t.Fatalf("找不到的会话：%v", err)
	}
}

// 流式：正文、工具调用、工具结果、结束依次到达。
func TestSessionStream(t *testing.T) {
	var calls []string
	app := tenontest.New(t, tenon.WithTools(orderTool(&calls)),
		tenontest.Model(tenontest.Reply("我查查").CallTool("order_query", `{"id":"A2"}`), tenontest.Reply("A2 已发货")))
	ctx := context.Background()
	s, _ := app.NewSession(ctx, "", nil)
	var kinds []tenon.EventKind
	var text strings.Builder
	var done *tenon.Reply
	for ev := range s.Stream(ctx, "A2 呢") {
		kinds = append(kinds, ev.Kind)
		switch ev.Kind {
		case tenon.EventText:
			text.WriteString(ev.Text)
		case tenon.EventDone:
			done = ev.Reply
		case tenon.EventError:
			t.Fatal(ev.Err)
		}
	}
	for _, want := range []tenon.EventKind{tenon.EventText, tenon.EventToolCall, tenon.EventToolResult, tenon.EventDone} {
		if !slices.Contains(kinds, want) {
			t.Fatalf("缺 %s：%v", want, kinds)
		}
	}
	if done == nil || done.Text != "A2 已发货" || !strings.Contains(text.String(), "A2 已发货") || kinds[len(kinds)-1] != tenon.EventDone {
		t.Fatalf("结束：%v 正文 %q 事件 %v", done, text.String(), kinds)
	}
}

// 确认：Send 时没人能回答就立即拒绝（不等 24 小时）；Stream 把确认交给调用方。
func TestConfirmWithoutUI(t *testing.T) {
	ask := tenon.WithConfig("permission", map[string]any{"force_mode": "ask"})
	ops := tenon.WithAgent(tenon.Agent{Name: "ops", Tools: []string{"shell"}}) // 缺省智能体不带 shell
	app := tenontest.New(t, ask, ops, tenontest.Model(
		tenontest.Reply("跑").CallTool("shell", `{"command":"echo hi"}`), tenontest.Reply("没跑成"),
		tenontest.Reply("再跑").CallTool("shell", `{"command":"echo hi"}`), tenontest.Reply("跑完了")))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // 机器负载高时（两个仓库同时跑 CI）20 s 不够
	defer cancel()
	s, _ := app.NewSession(ctx, "", nil)
	if _, err := s.Send(ctx, "跑一下"); err != nil {
		t.Fatalf("Send 应立即拒绝确认并结束这一轮：%v", err)
	}
	approved := false
	for ev := range s.Stream(ctx, "再跑一下") {
		if ev.Kind == tenon.EventConfirm {
			if err := ev.Approve(true); err != nil {
				t.Fatal(err)
			}
			approved = true
		}
		if ev.Kind == tenon.EventError {
			t.Fatal(ev.Err)
		}
	}
	if !approved {
		t.Fatal("Stream 应交出 confirm 事件")
	}
}

// WithOpenAI：类型化配端点；api_key 只收引用。
func TestWithOpenAI(t *testing.T) {
	app, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()), tenon.WithWorkdir(t.TempDir()),
		tenon.WithOpenAI("https://api.deepseek.com/v1", "deepseek-chat", tenon.Env("DEEPSEEK_API_KEY")))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	c := app.API().NewConn(context.Background(), appapi.LocalPrincipal)
	defer c.Close()
	ms := protoCall[wire.ModelsListResult](t, c, "models.list", map[string]any{})
	if ms.Default != "cli/deepseek-chat" {
		t.Fatalf("缺省模型：%q", ms.Default)
	}
	if _, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()),
		tenon.WithOpenAI("https://x/v1", "m", tenon.KeyRef("sk-明文"))); err == nil {
		t.Fatal("明文密钥应被拒绝")
	}
}
