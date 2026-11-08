package main

import (
	"context"
	"strings"
	"testing"

	"tenon/tenontest"
)

func TestRun(t *testing.T) {
	model := tenontest.Model(
		tenontest.Reply("").CallTool("order_query", `{"id":"A1"}`),
		tenontest.Reply("A1 已发货。"),
		tenontest.Reply("").CallTool("order_refund", `{"id":"A1"}`),
		tenontest.Reply("已经给你退了。"))
	var asked []string
	approve := func(tool, _ string) bool { asked = append(asked, tool); return true }
	var out strings.Builder
	if err := run(context.Background(), &out, approve, model); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	t.Log(got)
	if !strings.Contains(got, "[结果 已退款：A1]") || !strings.Contains(got, "已经给你退了。") {
		t.Fatalf("输出：%s", got)
	}
	if len(asked) != 1 || asked[0] != "order_refund" {
		t.Fatalf("只有退款要确认：%v", asked)
	}
}
