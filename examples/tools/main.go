// tools：自定义工具、会改东西的工具要确认、流式输出（docs/00 - 上手指南/01、02）。
//
//	TENON_BASE_URL=https://api.deepseek.com/v1 TENON_MODEL=deepseek-chat TENON_API_KEY=… go run ./examples/tools
//
// 退款工具标了 Dangerous：确认事件出现时在终端问 y/n。main_test.go 用脚本模型跑同一个 run。
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// OrderArgs order_query 的参数。
type OrderArgs struct {
	ID string `json:"id" desc:"订单号"`
}

// RefundArgs refund 的参数。
type RefundArgs struct {
	ID     string `json:"id" desc:"订单号"`
	Reason string `json:"reason,omitempty" desc:"退款原因"`
}

// shop 一个内存里的小店，工具读写它。
type shop struct {
	mu       sync.Mutex
	refunded map[string]bool
}

func (s *shop) tools() []tenon.Tool {
	query := tenon.Func("order_query", "按订单号查订单状态", func(_ context.Context, a OrderArgs) (any, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		status := "已发货"
		if s.refunded[a.ID] {
			status = "已退款"
		}
		return map[string]string{"id": a.ID, "status": status}, nil
	})
	refund := tenon.Func("order_refund", "给订单退款", func(_ context.Context, a RefundArgs) (any, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.refunded[a.ID] = true
		return "已退款：" + a.ID, nil
	}).Dangerous()
	return []tenon.Tool{query, refund}
}

func main() {
	model := tenon.WithOpenAI(os.Getenv("TENON_BASE_URL"), os.Getenv("TENON_MODEL"), tenon.Env("TENON_API_KEY"))
	in := bufio.NewReader(os.Stdin)
	ask := func(tool, args string) bool {
		fmt.Printf("\n允许调用 %s %s？[y/N] ", tool, args)
		line, _ := in.ReadString('\n')
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
	}
	if err := run(context.Background(), os.Stdout, ask, model); err != nil {
		log.Fatal(err)
	}
}

// run 两轮：先查订单，再退款（退款要确认，approve 回答）。
func run(ctx context.Context, w io.Writer, approve func(tool, args string) bool, model tenon.Option) error {
	s := &shop{refunded: map[string]bool{}}
	app, err := tenon.New(tenon.Default(), tenon.WithStore(memstore.MemoryStore()), tenon.WithTools(s.tools()...),
		tenon.WithAgent(tenon.Agent{Name: "support", Prompt: "你是售后客服。查订单、退款都要用工具。",
			Tools: []string{"order_query", "order_refund"}}),
		model)
	if err != nil {
		return err
	}
	defer app.Close(ctx)
	if err := app.Start(ctx); err != nil {
		return err
	}
	sess, err := app.NewSession(ctx, "support", nil)
	if err != nil {
		return err
	}
	for _, q := range []string{"A1 到哪了？", "A1 不要了，帮我退款。"} {
		fmt.Fprintf(w, "问：%s\n答：", q)
		for ev := range sess.Stream(ctx, q) {
			switch ev.Kind {
			case tenon.EventText:
				fmt.Fprint(w, ev.Text)
			case tenon.EventToolCall:
				fmt.Fprintf(w, "[调用 %s %s]", ev.Tool, ev.Args)
			case tenon.EventConfirm:
				if err := ev.Approve(approve(ev.Tool, ev.Args)); err != nil {
					return err
				}
			case tenon.EventToolResult:
				fmt.Fprintf(w, "[结果 %s]", ev.Text)
			case tenon.EventError:
				return ev.Err
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}
