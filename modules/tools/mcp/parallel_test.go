package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 各服务并行连接（2026-11-04 审查 P2 工具体验：原先串行，一个慢服务拖住全部启动）；结果仍按配置顺序。
// 判定看两个 initialize 有没有同时在处理，不看墙钟耗时：负载高时 400ms 的睡眠会被拉长，按耗时断言会偶发失败。
func TestOpenConnectsInParallel(t *testing.T) {
	var inFlight, peak atomic.Int32
	both := make(chan struct{})
	var once sync.Once
	slow := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var m message
			if json.NewDecoder(r.Body).Decode(&m) != nil {
				w.WriteHeader(400)
				return
			}
			if m.Method == "initialize" {
				n := inFlight.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				if n >= 2 {
					once.Do(func() { close(both) })
				}
				select { // 等另一个也进来（串行时等满 3s 后放行，断言再报失败）
				case <-both:
				case <-time.After(3 * time.Second):
				}
				inFlight.Add(-1)
			}
			w.Header().Set("Mcp-Session-Id", "sid-1")
			b := reply(m)
			if b == nil {
				w.WriteHeader(202)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		}))
	}
	a, b := slow(), slow()
	defer a.Close()
	defer b.Close()
	s := Open(context.Background(), Config{Server: []Server{{Name: "a", URL: a.URL}, {Name: "b", URL: b.URL}}})
	defer s.Close()
	if p := peak.Load(); p < 2 {
		t.Fatalf("两个服务应并行连接，同时握手的最多只有 %d 个（串行）", p)
	}
	if len(s.Clients) != 2 || len(s.Warnings) != 0 {
		t.Fatalf("clients=%d warnings=%v", len(s.Clients), s.Warnings)
	}
	if len(s.Tools) == 0 || !strings.HasPrefix(s.Tools[0].Spec.Name, "mcp__a__") ||
		!strings.HasPrefix(s.Tools[len(s.Tools)-1].Spec.Name, "mcp__b__") {
		t.Fatalf("工具应按配置顺序：首 %s 末 %s", s.Tools[0].Spec.Name, s.Tools[len(s.Tools)-1].Spec.Name)
	}
}
