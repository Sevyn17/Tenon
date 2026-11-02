package session

import (
	"errors"
	"testing"
)

// T2a-1（09 9.4 ②）：运行中到达的用户消息立即写进历史，进入下一次模型请求，不另开回合。
func TestMidInputAppendsDuringTurn(t *testing.T) {
	c := New(&Config{Model: "m", Policy: DefaultPolicy()})
	if _, _, err := c.Input(Content{NewTextPart("开始")}, map[string]any{"source": "user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MidInput(Content{NewTextPart("补充")}, map[string]any{"source": "user"}); err != nil {
		t.Fatalf("回合进行中应能插入: %v", err)
	}
	req, err := BuildRequest(c.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(req.Messages); n != 2 {
		t.Fatalf("应有两段用户输入，得 %d 条消息", n)
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" || len(last.Content) != 1 || last.Content[0].Text != "补充" {
		t.Fatalf("插入的消息应在末尾：%+v", last)
	}
	// 同一回合：不应产生新的 TurnInfo。
	if got := c.hdr.Turn.Turn; got != 1 {
		t.Fatalf("插入不应开新回合，turn=%d", got)
	}
}

func TestMidInputRequiresRunningTurn(t *testing.T) {
	c := New(&Config{Model: "m", Policy: DefaultPolicy()})
	if _, err := c.MidInput(Content{NewTextPart("补")}, nil); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("空闲时应返回 ErrNotWaiting，得 %v", err)
	}
}
