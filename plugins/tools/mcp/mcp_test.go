package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

// 假服务：处理 initialize / tools/list / tools/call。
func handle(m message) (any, *RPCError) {
	switch m.Method {
	case "initialize":
		return map[string]any{"protocolVersion": ProtocolVersion, "serverInfo": map[string]any{"name": "fake"}, "capabilities": map[string]any{}}, nil
	case "tools/list":
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.Cursor == "" {
			return map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "回显", "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": "p2"}, nil
		}
		return map[string]any{"tools": []any{
			map[string]any{"name": "fail", "description": "总是报错"},
			map[string]any{"name": "env", "description": "读环境"},
			map[string]any{"name": "slow", "description": "很慢"},
		}}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch p.Name {
		case "echo":
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo:" + string(p.Arguments)}, map[string]any{"type": "image", "mimeType": "image/png", "data": "AA=="}}}, nil
		case "fail":
			return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "坏了"}}}, nil
		case "env":
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": "secret=" + os.Getenv("FAKE_SECRET") + ";keep=" + os.Getenv("FAKE_KEEP") + ";extra=" + os.Getenv("FAKE_EXTRA")}}}, nil
		case "slow":
			time.Sleep(3 * time.Second)
			return map[string]any{"content": []any{}}, nil
		}
		return nil, &RPCError{Code: -32602, Message: "unknown tool"}
	}
	return nil, &RPCError{Code: -32601, Message: "method not found"}
}

func reply(m message) []byte {
	if m.ID == nil {
		return nil
	}
	res, e := handle(m)
	out := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	if e != nil {
		out["error"] = e
	} else {
		out["result"] = res
	}
	b, _ := json.Marshal(out)
	return b
}

func TestMain(m *testing.M) {
	if os.Getenv("FAKE_MCP") == "1" { // 作为 stdio 服务运行
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var msg message
			if json.Unmarshal(sc.Bytes(), &msg) != nil {
				continue
			}
			if msg.Method == "tools/call" && strings.Contains(string(msg.Params), `"exit"`) {
				os.Exit(3)
			}
			if b := reply(msg); b != nil {
				go func(b []byte) { os.Stdout.Write(append(b, '\n')) }(b) // 并发写入，测乱序响应
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func stdioServer(t *testing.T) Server {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Server{Name: "fake", Command: exe, Env: map[string]string{"FAKE_MCP": "1", "FAKE_EXTRA": "x"}, Timeout: 2}
}

func callTool(t *testing.T, ts []*toolhub.Tool, name, args string) toolhub.Output {
	t.Helper()
	for _, tl := range ts {
		if tl.Spec.Name == name {
			out, err := tl.Call(context.Background(), &toolhub.Cx{}, json.RawMessage(args))
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("没有工具 %s", name)
	return toolhub.Output{}
}

func text(o toolhub.Output) string {
	var sb strings.Builder
	for _, p := range o.Content {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

func checkSource(t *testing.T, s *Source) {
	t.Helper()
	if len(s.Warnings) > 0 {
		t.Fatal(s.Warnings)
	}
	if len(s.Tools) != 4 { // 分页拼接
		t.Fatalf("工具数 %d", len(s.Tools))
	}
	for _, tl := range s.Tools {
		if !tl.Deferred || tl.Risk != toolhub.Write || !strings.HasPrefix(tl.Spec.Name, "mcp__fake__") || !strings.Contains(tl.Spec.Description, "MCP 服务 fake") {
			t.Fatalf("工具属性不对：%+v", tl.Spec)
		}
	}
	o := callTool(t, s.Tools, "mcp__fake__echo", `{"a":1}`)
	if o.Status != session.ToolOK || !strings.Contains(text(o), `echo:{"a":1}`) || !strings.Contains(text(o), "未显示") {
		t.Fatal(text(o))
	}
	if o := callTool(t, s.Tools, "mcp__fake__fail", `{}`); o.Status != session.ToolError || !strings.Contains(text(o), "坏了") {
		t.Fatal(text(o))
	}
}

func TestStdio(t *testing.T) {
	t.Setenv("FAKE_SECRET", "hunter22")
	t.Setenv("FAKE_KEEP", "ok")
	s := Open(context.Background(), Config{Server: []Server{stdioServer(t)}})
	defer s.Close()
	checkSource(t, s)
	// 环境去密钥，保留普通变量，合并配置 env
	if got := text(callTool(t, s.Tools, "mcp__fake__env", `{}`)); got != "secret=;keep=ok;extra=x" {
		t.Fatal(got)
	}
	// 超时
	if o := callTool(t, s.Tools, "mcp__fake__slow", `{}`); o.Status != session.ToolError || !strings.Contains(text(o), "超时") {
		t.Fatal(text(o))
	}
	// 进程死掉 → 下次调用重连
	_ = callTool(t, s.Tools, "mcp__fake__echo", `{"exit":1}`)
	if o := callTool(t, s.Tools, "mcp__fake__echo", `{"b":2}`); o.Status != session.ToolOK {
		t.Fatal("重连失败：" + text(o))
	}
}

func TestCancel(t *testing.T) {
	s := Open(context.Background(), Config{Server: []Server{stdioServer(t)}})
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	var slow *toolhub.Tool
	for _, tl := range s.Tools {
		if tl.Spec.Name == "mcp__fake__slow" {
			slow = tl
		}
	}
	start := time.Now()
	out, _ := slow.Call(ctx, &toolhub.Cx{}, json.RawMessage(`{}`))
	if out.Status != session.ToolTurnCancelled || time.Since(start) > time.Second {
		t.Fatalf("%v %v", out.Status, time.Since(start))
	}
}

func TestHTTP(t *testing.T) {
	sse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m message
		if json.NewDecoder(r.Body).Decode(&m) != nil {
			w.WriteHeader(400)
			return
		}
		if m.Method != "initialize" && (r.Header.Get("Mcp-Session-Id") != "sid-1" || r.Header.Get("MCP-Protocol-Version") != ProtocolVersion) {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Mcp-Session-Id", "sid-1")
		b := reply(m)
		if b == nil {
			w.WriteHeader(202)
			return
		}
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	defer srv.Close()
	for _, mode := range []bool{false, true} {
		sse = mode
		s := Open(context.Background(), Config{Server: []Server{{Name: "fake", URL: srv.URL}}})
		checkSource(t, s)
		s.Close()
	}
}

func TestPublicName(t *testing.T) {
	if n := PublicName("gh", "create_issue"); n != "mcp__gh__create_issue" {
		t.Fatal(n)
	}
	a, b := PublicName("gh", "a.b"), PublicName("gh", "a/b")
	if a == b || !strings.HasPrefix(a, "mcp__gh__a_b_") {
		t.Fatal(a, b)
	}
	long := PublicName("gh", strings.Repeat("x", 100))
	if len(long) > 64 {
		t.Fatal(len(long))
	}
	h := toolhub.New(toolhub.Options{})
	for _, n := range []string{a, b, long} {
		if err := h.Register(&toolhub.Tool{Spec: session.ToolSpec{Name: n}, Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
			return toolhub.Output{}, nil
		}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	p := t.TempDir() + "/mcp.toml"
	os.WriteFile(p, []byte("[[server]]\nname=\"a\"\ncommand=\"x\"\nurl=\"y\"\n"), 0o644)
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("command 与 url 同时配置应报错")
	}
	os.WriteFile(p, []byte("[[server]]\nname=\"a b\"\ncommand=\"x\"\n"), 0o644)
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("非法服务名应报错")
	}
	if c, err := LoadConfig(t.TempDir() + "/none.toml"); err != nil || len(c.Server) != 0 {
		t.Fatal(err)
	}
}
