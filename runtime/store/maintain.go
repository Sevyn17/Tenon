package store

// 存储清理循环（2026-11-04 审查 P2 归属：原先是 host.Host 的方法，但它只调存储的清理接口、不碰会话，
// 放在宿主里让 Host 多了一个 GC 字段；现在由装配层（assemble.Wire）启动）。

import (
	"context"
	"time"
)

// RetainDefault 观测与记账行的缺省保留期（约束清单「存储」）。
const RetainDefault = 7 * 24 * time.Hour

// BlobGC 可回收的工件存储：删没有引用、且超过宽限期未被触碰的工件，返回回收数。
type BlobGC interface {
	GC(ctx context.Context, grace time.Duration) (int, error)
}

// MaintainOptions Maintain 的参数。
type MaintainOptions struct {
	// Store 实现了 PruneStore 时清理过期的观测与记账行；没实现就跳过。
	Store any
	// GC 可空：每轮调用的额外清理（如回收没有引用的工件），返回回收条数。
	GC func(ctx context.Context) (int, error)
	// Every 清理间隔；<= 0 按 1 小时。Keep 保留期；<= 0 按 RetainDefault。
	Every, Keep time.Duration
	// Notify 可空：清理结果事件（store.pruned / store.prune_failed / store.gc / store.gc_failed）。
	Notify func(kind string, data map[string]any)
}

// Maintain 先清一次，之后每隔 Every 清一次，直到 ctx 取消。既不能 Prune 也没有 GC 时直接返回。
func Maintain(ctx context.Context, o MaintainOptions) {
	p, ok := o.Store.(PruneStore)
	if !ok && o.GC == nil {
		return
	}
	every, keep := o.Every, o.Keep
	if every <= 0 {
		every = time.Hour
	}
	if keep <= 0 {
		keep = RetainDefault
	}
	notify := o.Notify
	if notify == nil {
		notify = func(string, map[string]any) {}
	}
	run := func() {
		if ok {
			if n, err := p.Prune(ctx, time.Now().Add(-keep)); err != nil {
				notify("store.prune_failed", map[string]any{"error": err.Error()})
			} else if n > 0 {
				notify("store.pruned", map[string]any{"rows": n})
			}
		}
		if o.GC != nil {
			if n, err := o.GC(ctx); err != nil {
				notify("store.gc_failed", map[string]any{"error": err.Error()})
			} else if n > 0 {
				notify("store.gc", map[string]any{"blobs": n})
			}
		}
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
