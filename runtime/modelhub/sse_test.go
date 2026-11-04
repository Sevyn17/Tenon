package modelhub

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// PostSSE 的失败归一（审查 P1-60：三个适配器共用）：非 200 走 HTTPFailure 并可修正，取消归 Aborted，
// 200 返回响应体并带上请求头。
func TestPostSSE(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-api-key")
		switch r.URL.Path {
		case "/v1/ok":
			io.WriteString(w, "data: {}\n\n")
		case "/v1/busy":
			w.WriteHeader(529)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	req := func(p string) SSERequest {
		return SSERequest{BaseURL: srv.URL + "/v1/", Path: p, Header: map[string]string{"x-api-key": "k", "empty": ""}, Body: map[string]any{"a": 1}}
	}
	body, f := PostSSE(context.Background(), req("/ok"), nil)
	if f != nil || gotAuth != "k" {
		t.Fatalf("200 应返回响应体：%v auth=%q", f, gotAuth)
	}
	b, _ := io.ReadAll(body)
	body.Close()
	if string(b) != "data: {}\n\n" {
		t.Fatalf("响应体：%q", b)
	}
	if _, f := PostSSE(context.Background(), req("/nope"), nil); f == nil || f.Code != CodeAuth {
		t.Fatalf("401 应归 Auth：%+v", f)
	}
	fixed := false
	if _, f := PostSSE(context.Background(), req("/busy"), func(s int, f *Failure) { fixed = s == 529; f.Code = CodeServer }); f == nil || f.Code != CodeServer || !fixed {
		t.Fatalf("fix 应能按状态码修正：%+v", f)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, f := PostSSE(ctx, req("/ok"), nil); f == nil || f.Code != CodeAborted {
		t.Fatalf("取消应归 Aborted：%+v", f)
	}
	if _, f := PostSSE(context.Background(), SSERequest{BaseURL: srv.URL, Body: func() {}}, nil); f == nil || f.Code != CodeInvalidRequest {
		t.Fatalf("序列化失败应归 InvalidRequest：%+v", f)
	}
}
