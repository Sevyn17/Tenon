package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/tenontest"
)

func TestRun(t *testing.T) {
	model := tenontest.Model(
		tenontest.Reply("").CallTool("order_query", `{"id":"A1"}`),
		tenontest.Reply("A1 已发货。"),
		tenontest.Reply("").CallTool("order_query", `{"id":"A2"}`),
		tenontest.Reply("A2 查不了。"))
	var out strings.Builder
	if err := run(context.Background(), &out, model); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	t.Log(got)
	for _, want := range []string{"[结果 已发货]", "[失败 ", "已冻结", "共 2 轮"} {
		if !strings.Contains(got, want) {
			t.Fatalf("输出缺 %q：\n%s", want, got)
		}
	}
}
