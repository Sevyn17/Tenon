package modelhub

import (
	"context"
	"testing"
	"time"
)

const oneModel = `
[provider.a]
protocol = "fake"
key = "env:A"
[model."a/m1"]
context_window = 1000
[alias.default]
plan = [{ model = "a/m1", retries = 3 }]
`

// 客户端改进 M8：每次退避等待之前发一个 Retry，attempt 从 2 递增；连续失败 N 次 → N−1 个 Retry，最后是 Failed。
func TestRetryNoticeBeforeEachWait(t *testing.T) {
	ad := &fakeAdapter{script: func(int, Endpoint) []Event { return []Event{fail(CodeTransport)} }}
	h, _ := testHub(t, ad, oneModel)
	var waits []time.Duration // 只在调度协程里写，通道关闭后才读
	h.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	var notes []RetryNotice
	var failed *Failure
	for ev := range h.Call(context.Background(), req("r")) {
		switch {
		case ev.Retry != nil:
			notes = append(notes, *ev.Retry)
		case ev.Failed != nil:
			failed = ev.Failed
		}
	}
	const n = 4 // retries = 3 → 共 4 次尝试
	if len(ad.calls) != n || failed == nil || failed.Code != CodeTransport {
		t.Fatalf("应尝试 %d 次后失败：calls=%d failed=%+v", n, len(ad.calls), failed)
	}
	if len(notes) != n-1 || len(waits) != n-1 {
		t.Fatalf("应有 %d 个 Retry、%d 次等待：%+v %v", n-1, n-1, notes, waits)
	}
	for i, r := range notes {
		if r.Attempt != i+2 || r.Max != n || r.Wait != waits[i] || r.Reason == "" {
			t.Fatalf("第 %d 个 Retry 不符：%+v", i, r)
		}
	}
}
