// component：一个应用自己的组件（docs/上手/03）。只 import tenon，演示：
//
//   - Info 声明配置（WithConfig 给值，Setup 里 cx.Config() 取）和提供的服务；
//
//   - 另一个组件按服务键取用它，在 PointTools 登记工具、在 PointSystemBlock 加系统提示；
//
//   - GuardToolCall 守卫拦下黑名单里的订单；PointAfterTurn 记每轮结束。
//
//     TENON_BASE_URL=https://api.deepseek.com/v1 TENON_MODEL=deepseek-chat TENON_API_KEY=… go run ./examples/component
//
// main_test.go 用脚本模型跑同一个 run。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"sync/atomic"

	"tenon"
)

// Catalog 订单目录服务：catalog 组件提供，support 组件使用。
type Catalog interface {
	Status(id string) (string, bool)
}

var catalogKey = tenon.NewKey[Catalog]("catalog")

type mapCatalog map[string]string

func (m mapCatalog) Status(id string) (string, bool) { s, ok := m[id]; return s, ok }

// catalogConfig catalog 组件的配置：cfg 取键名，default 写缺省值。
type catalogConfig struct {
	Orders  map[string]string `cfg:"orders"`
	Blocked []string          `cfg:"blocked"`
}

// catalogComponent 按配置提供订单目录服务。
type catalogComponent struct{}

func (catalogComponent) Name() string { return "catalog" }
func (catalogComponent) Info() tenon.ComponentInfo {
	return tenon.ComponentInfo{Config: &catalogConfig{}, Provides: []string{catalogKey.Name()}}
}
func (catalogComponent) Setup(cx *tenon.Registrar) error {
	c := cx.Config().(*catalogConfig)
	if err := tenon.Provide[Catalog](cx, catalogKey, mapCatalog(c.Orders)); err != nil {
		return err
	}
	// 守卫：黑名单里的订单不让查。被拒时失败结果交给模型，这一轮继续。
	cx.Guard(tenon.GuardRule{Name: "blocked-orders", On: tenon.GuardToolCall,
		Check: func(r tenon.GuardRequest) (tenon.Verdict, string) {
			var a struct{ ID string }
			if r.Tool == "order_query" && json.Unmarshal(r.Args, &a) == nil && slices.Contains(c.Blocked, a.ID) {
				return tenon.Deny, "订单 " + a.ID + " 已冻结"
			}
			return tenon.Abstain, ""
		}})
	return nil
}
func (catalogComponent) Start(*tenon.Scope) error { return nil }

// supportComponent 用目录服务做工具，加一段系统提示，并数回合。
type supportComponent struct{ turns *atomic.Int32 }

func (supportComponent) Name() string { return "support-kit" }
func (supportComponent) Info() tenon.ComponentInfo {
	return tenon.ComponentInfo{Requires: []string{catalogKey.Need()}}
}
func (s supportComponent) Setup(cx *tenon.Registrar) error {
	cat, err := tenon.Get(cx, catalogKey)
	if err != nil {
		return err
	}
	query := tenon.Func("order_query", "按订单号查订单状态", func(_ context.Context, a struct {
		ID string `json:"id" desc:"订单号"`
	}) (any, error) {
		st, ok := cat.Status(a.ID)
		if !ok {
			return nil, fmt.Errorf("没有订单 %s", a.ID)
		}
		return st, nil
	})
	tenon.RegisterAt(cx, tenon.PointTools, "orders", tenon.ToolsOf(60, query))
	tenon.RegisterAt(cx, tenon.PointSystemBlock, "support-rules", tenon.BlockEntry{Order: 10,
		Func: func(*tenon.SessionCx) (*tenon.SystemBlock, error) {
			return &tenon.SystemBlock{Text: "回答订单问题前先用 order_query 查。"}, nil
		}})
	tenon.RegisterAt(cx, tenon.PointAfterTurn, "count-turns", tenon.AfterTurnFunc(func(string, tenon.EndReason) {
		s.turns.Add(1)
	}))
	return nil
}
func (supportComponent) Start(sc *tenon.Scope) error {
	sc.OnClose(func(context.Context) error { return nil }) // 关闭时的清理写在这里
	return nil
}

func main() {
	model := tenon.WithOpenAI(os.Getenv("TENON_BASE_URL"), os.Getenv("TENON_MODEL"), tenon.Env("TENON_API_KEY"))
	if err := run(context.Background(), os.Stdout, model); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, w io.Writer, model tenon.Option) error {
	turns := &atomic.Int32{}
	app, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()),
		tenon.With(catalogComponent{}, supportComponent{turns: turns}),
		tenon.WithConfig("catalog", map[string]any{
			"orders":  map[string]any{"A1": "已发货", "A2": "待付款"},
			"blocked": []any{"A2"},
		}),
		model)
	if err != nil {
		return err
	}
	defer app.Close(ctx)
	if err := app.Start(ctx); err != nil {
		return err
	}
	s, err := app.NewSession(ctx, "", nil)
	if err != nil {
		return err
	}
	for _, q := range []string{"A1 到哪了？", "A2 呢？"} {
		fmt.Fprintf(w, "问：%s\n", q)
		for ev := range s.Stream(ctx, q) {
			switch ev.Kind {
			case tenon.EventToolResult:
				mark := "结果"
				if ev.Failed {
					mark = "失败"
				}
				fmt.Fprintf(w, "  [%s %s]\n", mark, ev.Text)
			case tenon.EventDone:
				fmt.Fprintf(w, "答：%s\n", ev.Reply.Text)
			case tenon.EventError:
				return ev.Err
			}
		}
	}
	fmt.Fprintf(w, "共 %d 轮\n", turns.Load())
	return nil
}
