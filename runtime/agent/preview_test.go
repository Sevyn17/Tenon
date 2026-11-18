package agent

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// replyModel 回「答：<最后一条用户消息>」；最后一条是工具结果时也照样回文本（本测试不调工具）。
type replyModel struct{}

func (replyModel) Stream(_ context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	s := "答：" + contentText(req.Messages[len(req.Messages)-1].Content)
	return session.AssistantItem{Content: session.Content{session.NewTextPart(s)}}, session.StopReason{V: "EndTurn"}, nil
}

func previewOf(t *testing.T, db store.Store) string {
	t.Helper()
	l, err := db.Load(context.Background(), "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	return l.Preview
}

// 客户端改进 M7 协议T9：检查点填会话行预览。
func TestCheckpointFillsPreview(t *testing.T) {
	ctx := context.Background()

	t.Run("工具调用后取最后一条有正文的 assistant", func(t *testing.T) {
		db := tempStore(t)
		a := newAgent(t, db, agentOpts{})
		if _, err := a.Send(ctx, "ping"); err != nil {
			t.Fatal(err)
		}
		if p := previewOf(t, db); p != "工具说：pong" {
			t.Fatalf("预览 %q", p)
		}
	})

	t.Run("回退后重算", func(t *testing.T) {
		db := tempStore(t)
		a := newAgent(t, db, agentOpts{model: replyModel{}})
		if _, err := a.Send(ctx, "第一"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Send(ctx, "第二"); err != nil {
			t.Fatal(err)
		}
		if p := previewOf(t, db); p != "答：第二" {
			t.Fatalf("两回合后预览 %q", p)
		}
		if err := a.Core.Rewind(2); err != nil { // 去掉第二回合（user、assistant 各一条）
			t.Fatal(err)
		}
		if err := a.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if p := previewOf(t, db); p != "答：第一" {
			t.Fatalf("回退后预览 %q", p)
		}
	})

	t.Run("只有 user", func(t *testing.T) {
		db := tempStore(t)
		a := newAgent(t, db, agentOpts{model: fakeModel{fail: true}})
		_, _ = a.Send(ctx, "  没有   回复 ") // 模型失败：日志里只有用户条目
		if p := previewOf(t, db); p != "没有 回复" {
			t.Fatalf("预览 %q", p)
		}
	})
}
