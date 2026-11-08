package tenon_test

// godoc 目录页的最短例子（计划/08 3.6）：用 tenontest 的脚本模型，不联网，go test 会执行并核对输出。

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/tenontest"
)

// 装配、启动、问一句。真实应用把 tenontest.Model 换成 WithOpenAI / WithAnthropic / WithModel。
func ExampleNew() {
	ctx := context.Background()
	app, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()),
		tenontest.Model(tenontest.Reply("你好，有什么可以帮你？")))
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer app.Close(ctx)

	r, err := app.Ask(ctx, "", "你好", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(r.Text)
	// Output: 你好，有什么可以帮你？
}

type weatherArgs struct {
	City string `json:"city" desc:"城市名"`
}

// 参数结构体生成 schema；返回值编成 JSON 交给模型。
func ExampleFunc() {
	ctx := context.Background()
	weather := tenon.Func("weather", "查天气", func(_ context.Context, a weatherArgs) (any, error) {
		return map[string]string{"city": a.City, "sky": "晴"}, nil
	})
	app, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()), tenon.WithTools(weather),
		tenontest.Model(
			tenontest.Reply("").CallTool("weather", `{"city":"杭州"}`),
			tenontest.Reply("杭州今天晴。")))
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer app.Close(ctx)

	s, _ := app.NewSession(ctx, "", nil)
	for ev := range s.Stream(ctx, "杭州天气？") {
		switch ev.Kind {
		case tenon.EventToolCall:
			fmt.Println("调用", ev.Tool, ev.Args)
		case tenon.EventDone:
			fmt.Println(ev.Reply.Text)
		}
	}
	// Output:
	// 调用 weather {"city":"杭州"}
	// 杭州今天晴。
}

// 同一个会话多轮。
func ExampleSession_Send() {
	ctx := context.Background()
	app, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()),
		tenontest.Model(tenontest.Reply("记住了：A1。"), tenontest.Reply("你刚才说的是 A1。")))
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer app.Close(ctx)

	s, _ := app.NewSession(ctx, "", nil)
	for _, q := range []string{"订单号是 A1", "我刚才说的订单号？"} {
		r, err := s.Send(ctx, q)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(r.Text)
	}
	// Output:
	// 记住了：A1。
	// 你刚才说的是 A1。
}

// 用 Info 声明配置，WithConfig 给值，Setup 里取解码好的结构体并登记工具。
type stampConfig struct {
	Prefix string `cfg:"prefix" default:"#"`
}

type stamp struct{}

func (stamp) Name() string              { return "stamp" }
func (stamp) Info() tenon.ComponentInfo { return tenon.ComponentInfo{Config: &stampConfig{}} }
func (stamp) Start(*tenon.Scope) error  { return nil }
func (stamp) Setup(cx *tenon.Registrar) error {
	c := cx.Config().(*stampConfig)
	t := tenon.Func("stamp", "给编号加前缀", func(_ context.Context, a struct {
		ID string `json:"id"`
	}) (any, error) {
		return c.Prefix + a.ID, nil
	})
	tenon.RegisterAt(cx, tenon.PointTools, "stamp", tenon.ToolsOf(60, t))
	return nil
}

func ExampleComponent() {
	ctx := context.Background()
	app, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()),
		tenon.With(stamp{}), tenon.WithConfig("stamp", map[string]any{"prefix": "NO."}),
		tenontest.Model(tenontest.Reply("").CallTool("stamp", `{"id":"7"}`), tenontest.Reply("好了")))
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer app.Close(ctx)

	s, _ := app.NewSession(ctx, "", nil)
	for ev := range s.Stream(ctx, "给 7 加前缀") {
		if ev.Kind == tenon.EventToolResult {
			fmt.Println(ev.Text)
		}
	}
	// Output: NO.7
}

// 错误用 errors.Is 判断。
func ExampleApp_Session() {
	ctx := context.Background()
	app, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()), tenontest.Model())
	if err != nil {
		log.Fatal(err)
	}
	if _, err := app.Session(ctx, "nope"); errors.Is(err, tenon.ErrNotStarted) {
		fmt.Println("还没 Start")
	}
	if err := app.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer app.Close(ctx)
	if _, err := app.Session(ctx, "nope"); errors.Is(err, tenon.ErrNotFound) {
		fmt.Println("没有这个会话")
	}
	// Output:
	// 还没 Start
	// 没有这个会话
}
