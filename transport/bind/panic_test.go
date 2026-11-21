package bind

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/host"
)

func TestEventEmitPanicClosesConnection(t *testing.T) {
	app := newTestApp(t)
	svc := NewService(app, func(name string, _ any) {
		if name == EventName {
			panic("测试：前端事件回调异常")
		}
	})
	svc.StartEvents()
	defer svc.StopEvents()
	svc.mu.Lock()
	c, done := svc.conn, svc.done
	svc.mu.Unlock()
	app.Notice("", "触发事件")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("事件回调 panic后事件泵没有退出")
	}
	if _, err := c.Next(context.Background()); !errors.Is(err, host.ErrClosed) {
		t.Fatalf("事件回调 panic后应关闭连接：%v", err)
	}
}
