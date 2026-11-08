package tenon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// WithKernel 的宿主参数接到 Host 上：SDK 与应用走同一份接线（assemble.Wire，2026-11-04 审查 P1-51：
// 原先 SDK 不设并发上限 / 所属树策略 / 保留期，也没有入口设）。
func TestWithKernelWiresHost(t *testing.T) {
	a, err := New(Minimal(), WithStore(memstore.New()), WithWorkdir(t.TempDir()),
		WithModels(testHub(), "m"),
		WithKernel(map[string]any{"concurrency": 3, "tree_policy": "restart", "tree_restarts": 2}),
		WithSleepAfter(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer a.Close(ctx)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	h := a.host
	if h.Limit != 3 || h.TreePolicy != "restart" || h.TreeRestarts != 2 || h.SleepAfter != time.Minute {
		t.Fatalf("宿主参数没接上：limit=%d policy=%q restarts=%d sleep=%s", h.Limit, h.TreePolicy, h.TreeRestarts, h.SleepAfter)
	}
	if h.New == nil || h.Child == nil {
		t.Fatal("Start 之后 Host.New / Child 应已由 Wire 设置")
	}
}

func TestWithKernelRejectsUnknownKey(t *testing.T) {
	_, err := New(Minimal(), WithStore(memstore.New()), WithWorkdir(t.TempDir()), WithModels(testHub(), "m"),
		WithKernel(map[string]any{"no_such_key": 1}))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("未知 [kernel] 键应报 ErrInvalid：%v", err)
	}
}

// testHub 空模型目录的 hub：这里的用例不调模型。
func testHub() *modelhub.Hub {
	return modelhub.New(modelhub.NewCatalog(), func(string) (string, error) { return "", nil }, nil)
}
