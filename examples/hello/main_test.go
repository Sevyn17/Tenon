package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/tenontest"
)

func TestRun(t *testing.T) {
	var out strings.Builder
	if err := run(context.Background(), &out, "你好", tenontest.Model(tenontest.Reply("我是 Tenon 智能体。"))); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "我是 Tenon 智能体。\n" {
		t.Fatalf("输出：%q", got)
	}
}
