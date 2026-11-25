package sessions

// 并发上限（Mortise docs/76 §5.1 D12、已定 Q1、Q4）：一个会话同时在跑的派出会话数（同步子会话、异步子会话、
// 独立会话合计）不超过 max。计数与登记在同一把锁里（按派出方 id 分片）：先登记名额再建会话，建会话失败调 release 回滚。
// 只计运行中与等待审批的：异步派出的会话回到空闲（Handle.Wait 返回）即释放名额，空闲不计。
// 计数在进程内：宿主重启后从零算（重启前派出、仍在跑的会话不占名额）。

import (
	"fmt"
	"sync"

	"github.com/Sevyn17/tenon/runtime/host"
)

type concurrency struct {
	mu sync.Mutex
	n  map[string]int // 派出方 → 占用名额数
}

// acquire 为派出方 creator 占一个名额；已满返回给模型看的错误。返回的 release 幂等。
func (c *concurrency) acquire(creator string, max int) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	if c.n[creator] >= max {
		return nil, fmt.Errorf("已达并发上限（%d）：本会话派出的会话还有 %d 个在运行。可用 session_list 查看进度，等某个完成后再派", max, c.n[creator])
	}
	c.n[creator]++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.n[creator]--; c.n[creator] <= 0 {
				delete(c.n, creator)
			}
			c.mu.Unlock()
		})
	}, nil
}

// inUse 派出方当前占用的名额（测试用）。
func (c *concurrency) inUse(creator string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[creator]
}

// track 异步派出的会话：等它回到空闲（或退出）后释放名额。取不到会话时立即释放。
func track(h *host.Host, id string, release func()) {
	hd, ok := h.Get(id)
	if !ok {
		release()
		return
	}
	go func() {
		defer release()
		_ = hd.Wait()
	}()
}
