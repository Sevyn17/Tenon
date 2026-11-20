package tenon_test

// Plan 模式验收（客户端改进 M12，docs/50 第 5.2 节）：走 SDK 装配（缺省插件 + 协议界面），脚本模型记下每次请求。
// 进入 Plan 模式 → 工具列表没有写文件 / 命令 / 未声明副作用的工具 → 硬调写工具被拒 → exit_plan 弹计划卡 →
// 要求修改（意见回到模型、仍在 Plan）→ 再提交、批准（work_mode 回 execute、档位不变、立即开新回合、要求记 todo）。

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/workmode"
)

type planStep struct {
	text  string
	name  string
	args  string
	calls bool
}

// recModel 按脚本逐次回复，记下每次请求。
type recModel struct {
	mu    sync.Mutex
	steps []planStep
	reqs  []session.Request
}

func (m *recModel) Protocol() string { return "rec" }

func (m *recModel) Stream(_ context.Context, r *modelhub.Request, ep modelhub.Endpoint) <-chan modelhub.Event {
	ch := make(chan modelhub.Event, 4)
	if strings.HasPrefix(r.RequestID, "side-") { // 标题 / 追问这类旁路调用：不占脚本、不记请求
		ch <- modelhub.Event{Done: &modelhub.Done{Reply: session.AssistantItem{Model: ep.Model,
			Content: session.Content{session.NewTextPart("标题")}}, Stop: session.StopReason{V: "EndTurn"}}}
		close(ch)
		return ch
	}
	m.mu.Lock()
	n := len(m.reqs)
	m.reqs = append(m.reqs, r.Req)
	m.mu.Unlock()
	go func() {
		defer close(ch)
		if n >= len(m.steps) {
			ch <- modelhub.Event{Failed: &modelhub.Failure{Code: "script_exhausted", Message: fmt.Sprintf("第 %d 次调用超出脚本", n+1)}}
			return
		}
		s := m.steps[n]
		reply := session.AssistantItem{Model: ep.Model}
		stop := session.StopReason{V: "EndTurn"}
		if s.text != "" {
			reply.Content = session.Content{session.NewTextPart(s.text)}
		}
		if s.calls {
			reply.ToolCalls = []session.ToolCall{{Name: s.name, Args: json.RawMessage(s.args)}}
			stop = session.StopReason{V: "ToolUse"}
		}
		ch <- modelhub.Event{Done: &modelhub.Done{Reply: reply, Stop: stop}}
	}()
	return ch
}

func (m *recModel) req(i int) session.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reqs[i]
}

func (m *recModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reqs)
}

func recModels(m *recModel) tenon.Option {
	cat := modelhub.NewCatalog()
	if err := cat.Add(modelhub.LayerUser, "rec", modelhub.CatalogFile{
		Provider: map[string]modelhub.Provider{"rec": {Protocol: "rec"}},
		Model:    map[string]modelhub.Model{"rec": {Provider: "rec", ContextWindow: 100000, MaxOutput: 8192}},
	}); err != nil {
		panic(err)
	}
	hub := modelhub.New(cat, func(string) (string, error) { return "", nil }, nil)
	if err := hub.RegisterAdapter(m); err != nil {
		panic(err)
	}
	return tenon.WithModels(hub, "rec")
}

// allTools 缺省智能体只带只读工具；验收要看写工具被收窄，用一个不限工具的定义。
var allTools = tenon.WithAgent(tenon.Agent{Name: "all", Tools: []string{"*"}})

func toolNames(r session.Request) []string {
	out := []string{}
	for _, t := range r.Tools {
		out = append(out, t.Name)
	}
	return out
}

func reqText(r session.Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		for _, p := range m.Content {
			b.WriteString(p.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// waitPlanCard 等会话出现一张未回答的计划卡。
func waitPlanCard(t *testing.T, c *appapi.Conn, sid string, seen map[string]bool) wire.Interaction {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r := protoCall[wire.InteractionsResult](t, c, "interactions.list", map[string]any{})
		for _, it := range r.Items {
			if it.Session == sid && it.Kind == wire.InteractionPlan && !seen[it.ID] {
				seen[it.ID] = true
				return it
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等不到计划卡")
	return wire.Interaction{}
}

func TestPlanModeFlow(t *testing.T) {
	plan := `{"plan":"先改 a.txt，再补测试","steps":["改 a.txt","补测试"]}`
	m := &recModel{steps: []planStep{
		{name: "write_file", args: `{"path":"a.txt","content":"x"}`, calls: true}, // 0：硬调写工具
		{name: "exit_plan", args: plan, calls: true},                              // 1：提交计划 → 要求修改
		{name: "exit_plan", args: plan, calls: true},                              // 2：再提交 → 批准
		{text: "好的"}, // 3：本回合收尾
		{name: "todo_write", args: `{"todos":[{"content":"改 a.txt","status":"pending"},{"content":"补测试","status":"pending"}]}`, calls: true}, // 4：新回合
		{text: "完成"}, // 5
	}}
	undeclared := tenon.Raw("deploy", "没有声明权限项的写工具", `{"type":"object"}`,
		func(context.Context, json.RawMessage) (any, error) { return "ok", nil }).Writes()
	app, err := tenon.New(tenon.Default(), tenon.WithStore(memstore.New()), tenon.WithWorkdir(t.TempDir()),
		recModels(m), tenon.WithProtocolUI(), tenon.WithTools(undeclared), allTools)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	c := app.API().NewConn(context.Background(), appapi.LocalPrincipal)
	defer c.Close()

	h := protoCall[wire.HelloResult](t, c, "hello", map[string]any{"protocol": map[string]int{"min": 2, "max": 2}})
	if !slices.Contains(h.Capabilities, appapi.CapWorkMode) {
		t.Fatalf("hello 应带 work_mode：%v", h.Capabilities)
	}
	snap := protoCall[wire.SessionSnapshot](t, c, "session.create", map[string]any{})
	sid := snap.ID
	if snap.Settings.WorkMode != "" {
		t.Fatalf("新会话 work_mode 应为空（execute）：%+v", snap.Settings)
	}
	modeBefore := protoCall[json.RawMessage](t, c, "mode.get", map[string]any{"session": sid})

	if _, err := c.Call(context.Background(), "session.settings", json.RawMessage(`{"session":"`+sid+`","work_mode":"bogus"}`)); err == nil {
		t.Fatal("不认识的 work_mode 应报错")
	}
	st := protoCall[wire.SettingsResult](t, c, "session.settings", map[string]any{"session": sid, "work_mode": "plan"})
	if st.Settings.WorkMode != wire.WorkModePlan || !st.Committed {
		t.Fatalf("设成 plan：%+v", st)
	}
	if g := protoCall[wire.SessionSnapshot](t, c, "session.get", map[string]any{"session": sid}); g.Settings.WorkMode != wire.WorkModePlan {
		t.Fatalf("快照应带 work_mode=plan：%+v", g.Settings)
	}

	protoCall[wire.SendResult](t, c, "session.send", map[string]any{"session": sid, "text": "给个计划", "client_id": "c1"})
	seen := map[string]bool{}
	card := waitPlanCard(t, c, sid, seen)
	if card.Plan == nil || card.Plan.Plan != "先改 a.txt，再补测试" || len(card.Plan.Steps) != 2 {
		t.Fatalf("计划卡内容不对：%+v", card.Plan)
	}
	protoCall[wire.RespondResult](t, c, "interaction.respond", map[string]any{"interaction": card.ID,
		"response": map[string]any{"value": map[string]any{"choice": "revise", "feedback": "先补测试再改"}}})
	card = waitPlanCard(t, c, sid, seen)
	protoCall[wire.RespondResult](t, c, "interaction.respond", map[string]any{"interaction": card.ID,
		"response": map[string]any{"value": map[string]any{"choice": "approve"}}})

	deadline := time.Now().Add(10 * time.Second)
	for {
		g := protoCall[wire.SessionSnapshot](t, c, "session.get", map[string]any{"session": sid})
		if m.count() >= 6 && g.RunState == wire.RunIdle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等不到新回合跑完：调用 %d 次，%s", m.count(), g.RunState)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// ② 工具显露：Plan 模式没有写文件、命令、未声明副作用的工具；读工具与 exit_plan 在。
	r0 := toolNames(m.req(0))
	for _, bad := range []string{"write_file", "edit", "shell", "deploy"} {
		if slices.Contains(r0, bad) {
			t.Errorf("Plan 模式不应显露 %s：%v", bad, r0)
		}
	}
	for _, want := range []string{"read_file", "grep", "exit_plan"} {
		if !slices.Contains(r0, want) {
			t.Errorf("Plan 模式应显露 %s：%v", want, r0)
		}
	}
	// ① 上下文：带 Plan 模式指令。
	if !strings.Contains(reqText(m.req(0)), "当前处于 Plan 模式") {
		t.Error("Plan 模式请求应带指令")
	}
	// ③ 执行前把关：硬调写工具被拒。
	if txt := reqText(m.req(1)); !strings.Contains(txt, "Plan 模式下不能用会改动的工具") {
		t.Errorf("硬调 write_file 应被拒：%s", txt)
	}
	// 要求修改：意见回到模型，仍在 Plan 模式。
	if txt := reqText(m.req(2)); !strings.Contains(txt, "先补测试再改") || !slices.Contains(toolNames(m.req(2)), "exit_plan") {
		t.Errorf("修改意见应交给模型且仍在 Plan 模式：%v", toolNames(m.req(2)))
	}
	// 批准：下一步起回到 execute（写工具回来，exit_plan 消失），新回合带计划与 todo 要求。
	r4 := m.req(4)
	if n := toolNames(r4); !slices.Contains(n, "write_file") || slices.Contains(n, "exit_plan") {
		t.Errorf("批准后应回到 execute 的工具列表：%v", n)
	}
	last := r4.Messages[len(r4.Messages)-1]
	var kick string
	for _, p := range last.Content {
		kick += p.Text
	}
	if last.Role != "user" || !strings.Contains(kick, "计划已批准") || !strings.Contains(kick, "todo_write") || !strings.Contains(kick, "1. 改 a.txt") {
		t.Errorf("新回合应以计划开头并要求记 todo：%s %q", last.Role, kick)
	}
	g := protoCall[wire.SessionSnapshot](t, c, "session.get", map[string]any{"session": sid})
	if g.Settings.WorkMode != "" {
		t.Errorf("批准后 work_mode 应回到 execute：%+v", g.Settings)
	}
	if after := protoCall[json.RawMessage](t, c, "mode.get", map[string]any{"session": sid}); string(after) != string(modeBefore) {
		t.Errorf("权限档位不应改变：%s → %s", modeBefore, after)
	}
	// 新回合是单独的回合（不是插进批准所在的回合）：第 4 次请求之前日志里有第 3 步的收尾回复。
	if !strings.Contains(reqText(r4), "好的") {
		t.Error("新回合应在批准所在回合结束之后开始")
	}
	_ = workmode.Plan
}

// 没进过 Plan 模式的会话：exit_plan 不显露，也没有 Plan 模式指令（和原来一样）。
func TestPlanModeAbsentByDefault(t *testing.T) {
	m := &recModel{steps: []planStep{{text: "你好"}}}
	app, err := tenon.New(tenon.Default(), tenon.WithStore(memstore.New()), tenon.WithWorkdir(t.TempDir()),
		recModels(m), tenon.WithProtocolUI(), allTools)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	c := app.API().NewConn(context.Background(), appapi.LocalPrincipal)
	defer c.Close()
	snap := protoCall[wire.SessionSnapshot](t, c, "session.create", map[string]any{})
	protoCall[wire.SendResult](t, c, "session.send", map[string]any{"session": snap.ID, "text": "hi", "client_id": "c1"})
	deadline := time.Now().Add(10 * time.Second)
	for m.count() < 1 || protoCall[wire.SessionSnapshot](t, c, "session.get", map[string]any{"session": snap.ID}).RunState != wire.RunIdle {
		if time.Now().After(deadline) {
			t.Fatal("等不到回合结束")
		}
		time.Sleep(20 * time.Millisecond)
	}
	r := m.req(0)
	if n := toolNames(r); slices.Contains(n, "exit_plan") || !slices.Contains(n, "write_file") {
		t.Errorf("execute 会话的工具列表不对：%v", n)
	}
	if strings.Contains(reqText(r), "Plan 模式") {
		t.Error("execute 会话不应有 Plan 模式指令")
	}
}
