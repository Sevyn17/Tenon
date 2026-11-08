// chat：只 import tenon，带一个自定义工具，接真实模型，两轮对话加一次流式（计划/08 阶段 A 的验收示例）。
//
//	TENON_BASE_URL=https://api.deepseek.com/v1 TENON_MODEL=deepseek-chat TENON_API_KEY=… go run ./examples/chat
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"tenon"
)

type OrderArgs struct {
	ID string `json:"id" desc:"订单号"`
}

func main() {
	lookup := tenon.Func("order_query", "按订单号查订单状态", func(ctx context.Context, a OrderArgs) (any, error) {
		return map[string]string{"id": a.ID, "status": "已发货，预计明天送达"}, nil
	})
	app, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()), tenon.WithTools(lookup),
		tenon.WithAgent(tenon.Agent{Name: "support", Prompt: "你是售后客服，查订单要用工具。", Tools: []string{"order_query"}}),
		tenon.WithOpenAI(os.Getenv("TENON_BASE_URL"), os.Getenv("TENON_MODEL"), tenon.Env("TENON_API_KEY")))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer app.Close(ctx)

	s, _ := app.NewSession(ctx, "", nil) // 只有一个定义：不给名字就用它
	for _, q := range []string{"我的订单 A1 到哪了？", "那 A2 呢？"} {
		r, err := s.Send(ctx, q)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("问：%s\n答：%s\n\n", q, r.Text)
	}
	fmt.Print("问：两个订单一句话总结一下\n答：")
	for ev := range s.Stream(ctx, "两个订单一句话总结一下") {
		switch ev.Kind {
		case tenon.EventText:
			fmt.Print(ev.Text)
		case tenon.EventToolCall:
			fmt.Printf("[调用 %s %s]", ev.Tool, ev.Args)
		case tenon.EventError:
			log.Fatal(ev.Err)
		}
	}
	fmt.Println()
}
