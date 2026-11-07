package modelhub

import (
	"context"
	"testing"

	"tenon/runtime/session"
)

// 没有模型层时不做结构校验（审查 P1-39：原先每次调模型都把整段历史序列化两遍）；有层时照常校验。
func TestShapeCheckSkippedWithoutLayers(t *testing.T) {
	n := 0
	old := checkShape
	checkShape = func(a, b session.Request) error { n++; return CheckShape(a, b) }
	defer func() { checkShape = old }()
	ad := &fakeAdapter{script: func(int, Endpoint) []Event { return []Event{done("ok")} }}
	h, _ := testHub(t, ad, twoModels)
	if _, _, _, f, _ := collect(h.Call(context.Background(), req("n1"))); f != nil {
		t.Fatal(f)
	}
	if n != 0 {
		t.Fatalf("无模型层时不应做结构校验，调了 %d 次", n)
	}
	h.AddLayer(Layer{Name: "pass", Call: func(ctx context.Context, r *Request, next func(context.Context, *Request) <-chan Event) <-chan Event {
		return next(ctx, r)
	}})
	if _, _, _, f, _ := collect(h.Call(context.Background(), req("n2"))); f != nil {
		t.Fatal(f)
	}
	if n != 1 {
		t.Fatalf("有模型层时应校验一次，调了 %d 次", n)
	}
}
