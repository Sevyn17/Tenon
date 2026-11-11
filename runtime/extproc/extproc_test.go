package extproc

import "testing"

func TestHandshakeAllowsSubset(t *testing.T) {
	h := Handshake{Version: Version, Capabilities: []string{CapTools, CapMiddleware, CapEvents, CapMatcher, CapResource, CapRenderer},
		Middleware: []string{"tool_call", "deliver"}, Tools: []string{"t1"}, Matchers: []string{"m1"}, Resources: []string{"r1"}}
	if err := h.Validate(); err != nil {
		t.Fatalf("允许的能力应通过：%v", err)
	}
}

func TestHandshakeRejectsOutOfScope(t *testing.T) {
	// 越界能力：guard / provide / model_call / context 一律拒。
	for _, bad := range []string{"guard", "provide", "model_call", "context"} {
		h := Handshake{Version: Version, Capabilities: []string{CapTools, bad}}
		if err := h.Validate(); err == nil {
			t.Fatalf("能力 %q 应被拒", bad)
		}
	}
	// 中间件点位越界（只开 tool_call / deliver）。
	h := Handshake{Version: Version, Capabilities: []string{CapMiddleware}, Middleware: []string{"model_call"}}
	if err := h.Validate(); err == nil {
		t.Fatal("中间件点位 model_call 应被拒")
	}
	// 版本不一致。
	if err := (Handshake{Version: "2", Capabilities: []string{CapTools}}).Validate(); err == nil {
		t.Fatal("版本不一致应被拒")
	}
	// 没报告能力。
	if err := (Handshake{Version: Version}).Validate(); err == nil {
		t.Fatal("没能力应被拒")
	}
}

func TestEncodeDecodeRequest(t *testing.T) {
	b, err := Encode(Request{Method: "tools/call", Params: []byte(`{"name":"t1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	r, err := DecodeRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "tools/call" {
		t.Fatalf("method = %q", r.Method)
	}
	if _, err := DecodeRequest([]byte(`{"jsonrpc":"1.0","method":"x"}`)); err == nil {
		t.Fatal("非 2.0 应被拒")
	}
	if _, err := DecodeRequest([]byte(`{"jsonrpc":"2.0"}`)); err == nil {
		t.Fatal("缺 method 应被拒")
	}
}
