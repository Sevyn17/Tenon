package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 各服务并行连接（2026-11-04 审查 P2 工具体验：原先串行，一个慢服务拖住全部启动）；结果仍按配置顺序。
func TestOpenConnectsInParallel(t *testing.T) {
	slow := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var m message
			if json.NewDecoder(r.Body).Decode(&m) != nil {
				w.WriteHeader(400)
				return
			}
			if m.Method == "initialize" {
				time.Sleep(400 * time.Millisecond)
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
	start := time.Now()
	s := Open(context.Background(), Config{Server: []Server{{Name: "a", URL: a.URL}, {Name: "b", URL: b.URL}}})
	defer s.Close()
	if d := time.Since(start); d >= 800*time.Millisecond {
		t.Fatalf("两个各 400ms 的服务应并行连接，用了 %s", d)
	}
	if len(s.Clients) != 2 || len(s.Warnings) != 0 {
		t.Fatalf("clients=%d warnings=%v", len(s.Clients), s.Warnings)
	}
	if len(s.Tools) == 0 || !strings.HasPrefix(s.Tools[0].Spec.Name, "mcp__a__") ||
		!strings.HasPrefix(s.Tools[len(s.Tools)-1].Spec.Name, "mcp__b__") {
		t.Fatalf("工具应按配置顺序：首 %s 末 %s", s.Tools[0].Spec.Name, s.Tools[len(s.Tools)-1].Spec.Name)
	}
}
