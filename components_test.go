package tenon_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/tenontest"
)

// 应用组件只 import tenon：声明配置、提供与使用服务、在类型化点位登记工具、登守卫规则。

type greeter interface{ Greet(name string) string }

type greetSvc struct{ word string }

func (g greetSvc) Greet(name string) string { return g.word + "，" + name }

var greeterKey = tenon.NewKey[greeter]("greeter")

type greetConfig struct {
	Word string `cfg:"word" default:"你好"`
}

// provider 按配置提供 greeter 服务。
type provider struct{}

func (provider) Name() string { return "greet-provider" }
func (provider) Info() tenon.ComponentInfo {
	return tenon.ComponentInfo{Config: &greetConfig{}, Provides: []string{greeterKey.Name()}}
}
func (provider) Setup(cx *tenon.Registrar) error {
	c := cx.Config().(*greetConfig)
	return tenon.Provide[greeter](cx, greeterKey, greetSvc{word: c.Word})
}
func (provider) Start(*tenon.Scope) error { return nil }

// user 用 greeter 做一个工具，并拦下 forbidden 工具。
type user struct{}

func (user) Name() string { return "greet-user" }
func (user) Info() tenon.ComponentInfo {
	return tenon.ComponentInfo{Requires: []string{greeterKey.Need()}}
}
func (user) Setup(cx *tenon.Registrar) error {
	g, err := tenon.Get(cx, greeterKey)
	if err != nil {
		return err
	}
	type args struct {
		Name string `json:"name"`
	}
	greet := tenon.Func("greet", "打招呼", func(_ context.Context, a args) (any, error) { return g.Greet(a.Name), nil })
	forbidden := tenon.Func("forbidden", "不让用", func(context.Context, args) (any, error) { return "不该到这", nil })
	tenon.RegisterAt(cx, tenon.PointTools, "greet-user", tenon.ToolsOf(60, greet, forbidden))
	component.WrapGuard(cx, component.ToolCall, "no-forbidden",
		func(ctx context.Context, req component.ToolCallReq) (component.Verdict, string) {
			if req.Tool == "forbidden" {
				return component.Deny, "测试里禁用"
			}
			return component.Abstain, ""
		})
	return nil
}
func (user) Start(*tenon.Scope) error { return nil }

func TestAppComponentInfo(t *testing.T) {
	app := tenontest.New(t, tenon.With(provider{}, user{}), tenon.WithConfig("greet-provider", map[string]any{"word": "早上好"}),
		tenontest.Model(
			tenontest.Reply("").CallTool("greet", `{"name":"小王"}`).CallTool("forbidden", `{"name":"x"}`),
			tenontest.Reply("完")))
	r, err := app.Ask(context.Background(), "", "打个招呼", nil)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := app.History(context.Background(), r.SessionID)
	var results []string // 按调用顺序：greet、forbidden
	for _, e := range h {
		if tr := e.Item.ToolResult; tr != nil {
			var text strings.Builder
			for _, p := range tr.Output {
				text.WriteString(p.Text)
			}
			results = append(results, tr.Status.State+":"+text.String())
		}
	}
	if len(results) != 2 || results[0] != "ok:早上好，小王" {
		t.Fatalf("greet 结果（应按 WithConfig 的 word）：%q", results)
	}
	if strings.HasPrefix(results[1], "ok") || strings.Contains(results[1], "不该到这") {
		t.Fatalf("forbidden 应被守卫拦下：%q", results)
	}
}

// 未知配置键在 New 里报错（按 Info().Config 校验）。
func TestAppComponentUnknownConfig(t *testing.T) {
	_, err := tenon.New(tenon.Minimal(), tenon.With(provider{}), tenon.WithConfig("greet-provider", map[string]any{"nope": 1}),
		tenontest.Model())
	if err == nil {
		t.Fatal("未知键应报错")
	}
}
