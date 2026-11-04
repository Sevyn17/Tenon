package modelhub

import (
	"context"
	"testing"

	"ybase/runtime/session"
)

// 目录里没有的模型名报 CodeUnknownModel，不归为 CodeNoAdapter（2026-11-04 审查 P2 错误一致性：
// 原先写错模型名和缺协议适配器是同一个码，上层分不清是配置写错还是少装了组件）。
func TestUnknownModelCode(t *testing.T) {
	ad := &fakeAdapter{script: func(int, Endpoint) []Event { return []Event{done("x")} }}
	h, _ := testHub(t, ad, twoModels)
	_, _, _, f, _ := collect(h.Call(context.Background(), &Request{Req: session.Request{Model: "no-such-model"}, RequestID: "r"}))
	if f == nil || f.Code != CodeUnknownModel {
		t.Fatalf("未知模型应报 %s：%+v", CodeUnknownModel, f)
	}
}
