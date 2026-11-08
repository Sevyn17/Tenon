package modelhub

import (
	"context"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

// 推理强度只发给声明了推理能力的模型：降级到普通模型时去掉，不改调用方的请求。
func TestReasoningOnlyForCapableModels(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	ad := &fakeAdapter{}
	ad.script = func(_ int, ep Endpoint) []Event {
		if ep.Model == "think" {
			return []Event{fail(CodeServer)}
		}
		return []Event{done("好")}
	}
	cat := `
[provider.a]
protocol = "fake"
key = "env:A"
[provider.b]
protocol = "fake"
key = "env:B"
[model."a/think"]
[model."a/think".caps]
reasoning = true
[model."b/plain"]
[alias.default]
plan = [{ model = "a/think", retries = 0 }, { model = "b/plain", retries = 0 }]
`
	ad.onReq = func(r *Request, ep Endpoint) {
		v := ""
		if r.Req.Params.Reasoning != nil {
			v = *r.Req.Params.Reasoning
		}
		mu.Lock()
		seen = append(seen, ep.Model+"="+v)
		mu.Unlock()
	}
	h, _ := testHub(t, ad, cat)
	high := "high"
	r := &Request{Req: session.Request{Model: "default", Params: session.Params{Reasoning: &high}}, RequestID: "n"}
	_, _, d, f, _ := collect(h.Call(context.Background(), r))
	if d == nil || f != nil {
		t.Fatalf("应降级成功：%v %v", d, f)
	}
	if len(seen) < 2 || seen[0] != "think=high" || seen[len(seen)-1] != "plain=" {
		t.Fatalf("推理强度下发不符：%v", seen)
	}
	if r.Req.Params.Reasoning == nil || *r.Req.Params.Reasoning != "high" {
		t.Fatal("不应改调用方的请求")
	}
}
