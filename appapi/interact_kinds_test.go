package appapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/host"
)

// openKind 在已打开的会话上发起一次交互（后台等回答），返回交互 id 与回答通道。
func openKind(t *testing.T, app *App, c *Conn, sess string, r host.Request) (wire.Interaction, <-chan host.Answer, <-chan error) {
	t.Helper()
	ans, errc := make(chan host.Answer, 1), make(chan error, 1)
	go func() {
		a, err := app.Host().Interact(context.Background(), sess, r)
		ans <- a
		errc <- err
	}()
	got := waitKind(t, c, wire.EvInteractionOpen)
	return got[len(got)-1].Data.(wire.Interaction), ans, errc
}

func respond(c *Conn, id, resp string) (wire.RespondResult, *Error) {
	out, err := c.Call(context.Background(), "interaction.respond", json.RawMessage(`{"interaction":"`+id+`","response":`+resp+`}`))
	if err != nil {
		return wire.RespondResult{}, AsError(err)
	}
	return out.(wire.RespondResult), nil
}

// choice / form / confirm / 自定义 kind：载荷上线、blocking 与 schema 透传、回答按 schema 校验、值交回等待方。
func TestInteractionKinds(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"k"}`); e != nil {
		t.Fatal(e)
	}
	sendWait(t, app, "k", "hi", "c0") // 打开会话（交互要发在打开着的会话上）
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "k"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)

	// choice：多选，最多 2 个
	it, ans, errc := openKind(t, app, c, "k", host.Request{Kind: "choice", Title: "选方案",
		Body: map[string]any{"text": "哪个？", "multiple": true, "max": 2,
			"options": []map[string]string{{"id": "a", "label": "A"}, {"id": "b", "label": "B"}, {"id": "c", "label": "C"}}}})
	if it.Kind != wire.InteractionChoice || it.Choice == nil || len(it.Choice.Options) != 3 || !it.Choice.Multiple || it.Choice.Max != 2 {
		t.Fatalf("choice 载荷：%+v", it)
	}
	for _, bad := range []string{`{"value":["x"]}`, `{"value":["a","b","c"]}`, `{"value":"a"}`, `{}`} {
		if _, e := respond(c, it.ID, bad); e == nil || (e.Code != wire.CodeInvalid && e.Code != wire.CodeBadRequest) {
			t.Fatalf("%s 应被拒：%+v", bad, e)
		}
	}
	r, e := respond(c, it.ID, `{"value":["a","c"]}`)
	if e != nil || r.Outcome != wire.OutcomeAnswered {
		t.Fatalf("choice 回答：%+v %+v", r, e)
	}
	if a := <-ans; string(a.Value) != `["a","c"]` || <-errc != nil {
		t.Fatalf("等待方拿到：%s", a.Value)
	}

	// form：schema 透传，必填与类型校验；blocking=false
	no := false
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","minLength":1},"age":{"type":"integer","minimum":0}},"required":["name"]}`)
	it, ans, errc = openKind(t, app, c, "k", host.Request{Kind: "form", Title: "填信息", Body: map[string]any{"submit": "提交"}, Schema: schema, Blocking: &no})
	if it.Form == nil || it.Form.Submit != "提交" || len(it.Schema) == 0 || it.Blocking == nil || *it.Blocking {
		t.Fatalf("form 载荷：%+v", it)
	}
	if _, e := respond(c, it.ID, `{"value":{"age":3}}`); e == nil || e.Code != wire.CodeInvalid {
		t.Fatalf("缺必填应 invalid：%+v", e)
	}
	if _, e := respond(c, it.ID, `{"value":{"name":"x","age":"3"}}`); e == nil || e.Code != wire.CodeInvalid {
		t.Fatalf("类型不符应 invalid：%+v", e)
	}
	if _, e := respond(c, it.ID, `{"value":{"name":"小王","age":30}}`); e != nil {
		t.Fatal(e)
	}
	if a := <-ans; string(a.Value) != `{"name":"小王","age":30}` || <-errc != nil {
		t.Fatalf("form 值：%s", a.Value)
	}

	// confirm：approve
	it, ans, errc = openKind(t, app, c, "k", host.Request{Kind: "confirm", Title: "继续？", Body: map[string]any{"text": "会删掉缓存", "danger": true}})
	if it.Confirm == nil || !it.Confirm.Danger {
		t.Fatalf("confirm 载荷：%+v", it)
	}
	if r, e := respond(c, it.ID, `{"approve":false}`); e != nil || r.Outcome != wire.OutcomeDenied {
		t.Fatalf("confirm：%+v %+v", r, e)
	}
	if a := <-ans; a.Approve || <-errc != nil {
		t.Fatal("confirm 应为拒绝")
	}

	// 插件自定义 kind：通用卡的自由文本当作字符串值
	it, ans, errc = openKind(t, app, c, "k", host.Request{Kind: "review", Title: "审一下", Body: map[string]any{"diff": "..."}})
	if _, e := respond(c, it.ID, `{"text":"看过了"}`); e != nil {
		t.Fatal(e)
	}
	if a := <-ans; string(a.Value) != `"看过了"` || <-errc != nil {
		t.Fatalf("自定义 kind：%s", a.Value)
	}

	// 截止：过期为 expired
	_, _, errc = openKind(t, app, c, "k", host.Request{Kind: "confirm", Title: "很快过期", Deadline: time.Now().Add(150 * time.Millisecond)})
	if err := <-errc; err == nil {
		t.Fatal("过期应返回错误")
	}
	got := waitKind(t, c, wire.EvInteractionDone)
	if d := got[len(got)-1].Data.(wire.InteractionResolved); d.Outcome != wire.OutcomeExpired {
		t.Fatalf("过期结果：%+v", d)
	}
	if _, err := app.Host().Interact(context.Background(), "k", host.Request{Kind: "approval"}); err == nil {
		t.Fatal("Interact 不接受 approval")
	}
}

func TestValidateValue(t *testing.T) {
	s := json.RawMessage(`{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string","enum":["x","y"]},"maxItems":1},"n":{"type":"number","maximum":1}}}`)
	for v, ok := range map[string]bool{`{}`: true, `{"tags":["x"]}`: true, `{"tags":["z"]}`: false, `{"tags":["x","y"]}`: false, `{"n":1.5}`: false, `{"n":0.5}`: true, `[]`: false} {
		if err := validateValue(s, json.RawMessage(v)); (err == nil) != ok {
			t.Errorf("%s：%v", v, err)
		}
	}
	if validateValue(nil, json.RawMessage(`"x"`)) != nil || validateValue(nil, json.RawMessage(`{`)) == nil {
		t.Fatal("无 schema")
	}
}

// blocking=false 的交互出现在快照与 interactions.list 里，载荷带 blocking:false（界面据此内联显示）。
func TestNonBlockingInSnapshot(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"nb"}`); e != nil {
		t.Fatal(e)
	}
	sendWait(t, app, "nb", "hi", "c0")
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "nb"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	no := false
	it, _, errc := openKind(t, app, c, "nb", host.Request{Kind: "form", Title: "f", Blocking: &no,
		Schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)})
	snap, _ := call2(t, app, "session.get", `{"session":"nb"}`)
	got := snap.(wire.SessionSnapshot).Interactions
	if len(got) != 1 || got[0].Blocking == nil || *got[0].Blocking || len(got[0].Schema) == 0 {
		t.Fatalf("快照：%+v", got)
	}
	if _, e := respond(c, it.ID, `{"value":{"a":"x"}}`); e != nil {
		t.Fatal(e)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}
