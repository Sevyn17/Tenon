package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 4xx / 5xx 按工具错误返回，带状态码与正文开头（2026-11-04 审查 P2 工具体验：原先按成功返回，
// 模型把 404 页面当成正文）。
func TestWebFetchErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("page not found"))
	}))
	defer srv.Close()
	w := &Web{AllowPrivate: true}
	w.defaults()
	_, err := w.fetch(context.Background(), srv.URL+"/x", "")
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "page not found") {
		t.Fatalf("404 应报错并带状态码与正文：%v", err)
	}
}

// 同主机 http → https 升级的重定向放行；其余跨源仍拒绝。
func TestRedirectAllowed(t *testing.T) {
	u := func(s string) *url.URL { p, _ := url.Parse(s); return p }
	for _, c := range []struct {
		from, to string
		ok       bool
	}{
		{"http://a.com/x", "http://a.com/y", true},
		{"http://a.com/x", "https://a.com/y", true},
		{"http://a.com:80/x", "https://a.com/y", true},
		{"http://a.com/x", "https://a.com:443/y", true},
		{"https://a.com/x", "http://a.com/y", false}, // 降级不放
		{"http://a.com/x", "https://b.com/y", false},
		{"http://a.com/x", "https://a.com:8443/y", false},
		{"http://a.com:8080/x", "http://a.com:9090/y", false},
	} {
		if got := redirectAllowed(u(c.from), u(c.to)); got != c.ok {
			t.Errorf("%s → %s = %v，期望 %v", c.from, c.to, got, c.ok)
		}
	}
}
