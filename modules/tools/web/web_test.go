package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/r":
			http.Redirect(w, r, "/page", http.StatusFound)
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><head><title>T</title><style>.x{}</style><script>evil()</script></head><body>
<h1>标题</h1><p>你好 <a href="https://example.com">链接</a></p>
<div hidden>隐藏内容</div><div style="display:none">也隐藏</div>
<ul><li>一</li><li>二</li></ul></body></html>`)
		case "/bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte{0, 1, 2})
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, strings.Repeat("a", 500))
		}
	}))
	defer srv.Close()
	w := &Web{AllowPrivate: true, MaxChars: 100}
	w.defaults()
	out, err := w.fetch(context.Background(), srv.URL+"/r", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"不可信", "# 标题", "[链接](https://example.com)", "一", "二", "状态: 200"} {
		if !strings.Contains(out, want) {
			t.Errorf("缺 %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"evil", "隐藏内容", "也隐藏", ".x{}"} {
		if strings.Contains(out, bad) {
			t.Errorf("不应含 %q:\n%s", bad, out)
		}
	}
	if raw, _ := w.fetch(context.Background(), srv.URL+"/page", "html"); !strings.Contains(raw, "<h1>") {
		t.Error("html 格式应保留原文")
	}
	if _, err := w.fetch(context.Background(), srv.URL+"/bin", ""); err == nil {
		t.Error("二进制应拒绝")
	}
	if big, _ := w.fetch(context.Background(), srv.URL+"/big", "text"); !strings.Contains(big, "已截断") {
		t.Error("超长应截断")
	}
}

func TestWebFetchRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	w := &Web{}
	w.defaults()
	if _, err := w.fetch(context.Background(), srv.URL, ""); !errors.Is(err, errPrivate) {
		t.Errorf("回环地址应拒绝，得到 %v", err)
	}
	for _, u := range []string{"file:///etc/passwd", "http://u:p@example.com/", "ftp://x", "http://" + strings.Repeat("a", 3000)} {
		if _, err := w.fetch(context.Background(), u, ""); err == nil {
			t.Errorf("%s 应拒绝", u)
		}
	}
	// 跨源重定向
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL, http.StatusFound)
	}))
	defer other.Close()
	wp := &Web{AllowPrivate: true}
	wp.defaults()
	if _, err := wp.fetch(context.Background(), other.URL, ""); err == nil || !strings.Contains(err.Error(), "跨源") {
		t.Errorf("跨源重定向应拒绝，得到 %v", err)
	}
}

type fakeSearch map[string][]SearchResult

func (f fakeSearch) Search(_ context.Context, q string) ([]SearchResult, error) {
	if r, ok := f[q]; ok {
		return r, nil
	}
	return nil, errors.New("boom")
}

func TestWebSearch(t *testing.T) {
	w := &Web{Search: fakeSearch{
		"a": {{Title: "A", URL: "https://a", Snippet: "sa"}, {Title: "B", URL: "https://b"}},
		"b": {{Title: "B2", URL: "https://b"}, {URL: "https://c"}},
	}}
	w.defaults()
	out, err := w.search(context.Background(), []string{"a", "b", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "https://b") != 1 || !strings.Contains(out, "## [A](https://a)\nsa") ||
		!strings.Contains(out, "[https://c](https://c)") || !strings.Contains(out, "部分查询失败") {
		t.Errorf("结果不对:\n%s", out)
	}
	if _, err := w.search(context.Background(), []string{"x"}); err == nil {
		t.Error("全部失败应报错")
	}
	if _, err := (&Web{}).search(context.Background(), []string{"a"}); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Error("无提供方应报结构化错误")
	}
	if _, err := w.search(context.Background(), []string{"1", "2", "3", "4", "5"}); err == nil {
		t.Error("超过查询上限应拒绝")
	}
}

func TestDeepSeekSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" || r.Header.Get("x-api-key") != "k" {
			http.Error(w, "bad", 401)
			return
		}
		fmt.Fprint(w, `{"content":[
{"type":"server_tool_use","id":"s1","name":"web_search"},
{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://go.dev","title":"Go"},{"type":"web_search_result","url":"https://go.dev","title":"dup"}]},
{"type":"text","text":"x","citations":[{"url":"https://go.dev","cited_text":"Go is fun"}]}]}`)
	}))
	defer srv.Close()
	d := &DeepSeekSearch{BaseURL: srv.URL, Key: func() string { return "k" }}
	res, err := d.Search(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Snippet != "Go is fun" || res[0].Title != "Go" {
		t.Errorf("%+v", res)
	}
	if _, err := parseAnthropicSearch([]byte(`{"content":[{"type":"text","text":"no search"}]}`)); err == nil {
		t.Error("无结果块应报错")
	}
	if _, err := parseAnthropicSearch([]byte(`{"content":[{"type":"web_search_tool_result","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}]}`)); err == nil {
		t.Error("错误块应报错")
	}
	bad := &DeepSeekSearch{BaseURL: srv.URL, Key: func() string { return "z" }}
	if _, err := bad.Search(context.Background(), "go"); err == nil {
		t.Error("401 应报错")
	}
}

// 搜索只认 DEEPSEEK_API_KEY：通用的 TENON_API_KEY 可能是别家模型的 key，不能发给 DeepSeek（审查 P0-16）。
func TestDeepSeekSearchIgnoresGenericKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("TENON_API_KEY", "sk-other-vendor")
	if p := NewDeepSeekSearch(); p != nil {
		t.Fatal("只有 TENON_API_KEY 时不应启用 DeepSeek 搜索")
	}
	t.Setenv("DEEPSEEK_API_KEY", "ds-key")
	p, ok := NewDeepSeekSearch().(*DeepSeekSearch)
	if !ok || p.Key() != "ds-key" {
		t.Fatalf("应使用 DEEPSEEK_API_KEY: %+v", p)
	}
}
