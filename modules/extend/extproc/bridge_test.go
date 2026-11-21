package extproc

// 回桥端到端：用一个子进程（本测试二进制自身，带环境变量标记）当扩展插件，
// 握手报告一个工具，框架调它、它经 JSON-RPC 返回结果；越界握手被拒。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/extproc"
	"github.com/Sevyn17/tenon/runtime/session"
)

// TestHelperPluginProcess 当子进程时的服务端：读 stdin 的 JSON-RPC，回握手 / 工具调用。
func TestHelperPluginProcess(t *testing.T) {
	if os.Getenv("TENON_EXTPROC_HELPER") != "1" {
		t.Skip("只在作为子进程时运行")
	}
	sc := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		line := sc.Bytes()
		var req extproc.Request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		switch req.Method {
		case "handshake":
			hs := extproc.Handshake{Version: extproc.Version,
				Capabilities: []string{extproc.CapTools, extproc.CapMiddleware, extproc.CapEvents,
					extproc.CapMatcher, extproc.CapResource},
				Tools:         []string{"echo_tool"},
				ToolSpecs:     []extproc.ToolSpec{{Name: "echo_tool", Description: "回显"}},
				Middleware:    []string{"tool_call", "deliver"},
				Events:        []string{"plugin.*"},
				Matchers:      []string{"kw"},
				MatcherSpecs:  []extproc.MatcherSpec{{Name: "kw", Description: "按 body 是否含关键字判断"}},
				Resources:     []string{"ticket"},
				ResourceSpecs: []extproc.ResourceSpec{{Name: "ticket", Description: "工单"}}}
			b, _ := json.Marshal(hs)
			_ = enc.Encode(extproc.Response{JSONRPC: "2.0", ID: req.ID, Result: b})
		case "tools/call":
			var p struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			}
			_ = json.Unmarshal(req.Params, &p)
			out, _ := json.Marshal(map[string]string{"text": "echo:" + string(p.Args)})
			_ = enc.Encode(extproc.Response{JSONRPC: "2.0", ID: req.ID, Result: out})
		case "middleware":
			var mc extproc.MiddlewareCall
			_ = json.Unmarshal(req.Params, &mc)
			res := extproc.MiddlewareResult{}
			if strings.Contains(mc.Tool, "bad") {
				res.Verdict, res.Reason = "deny", "工具名含 bad"
			}
			b, _ := json.Marshal(res)
			_ = enc.Encode(extproc.Response{JSONRPC: "2.0", ID: req.ID, Result: b})
		case "deliver":
			var p struct {
				SessionID  string          `json:"session_id"`
				Envelope   json.RawMessage `json:"envelope"`
				FromRemote bool            `json:"from_remote"`
			}
			_ = json.Unmarshal(req.Params, &p)
			// 约定：信封 body 含 "block" 就拦截。
			res := map[string]string{}
			if strings.Contains(string(p.Envelope), "block") {
				res["verdict"], res["reason"] = "deny", "body 含 block"
			}
			b, _ := json.Marshal(res)
			_ = enc.Encode(extproc.Response{JSONRPC: "2.0", ID: req.ID, Result: b})
		case "matcher":
			var mc extproc.MatchCall
			_ = json.Unmarshal(req.Params, &mc)
			// 约定：body 含 params["keyword"] 即命中，命中时 mode=wake。
			res := extproc.MatchResult{}
			if kw := mc.Params["keyword"]; kw != "" && strings.Contains(mc.Record.Body, kw) {
				res.Matched, res.Mode = true, "wake"
			}
			b, _ := json.Marshal(res)
			_ = enc.Encode(extproc.Response{JSONRPC: "2.0", ID: req.ID, Result: b})
		case "render":
			var rc extproc.RenderCall
			_ = json.Unmarshal(req.Params, &rc)
			b, _ := json.Marshal(extproc.RenderResult{Text: "[" + rc.Resource + "#" + itoa(rc.Record.Seq) + "] " + rc.Record.Body})
			_ = enc.Encode(extproc.Response{JSONRPC: "2.0", ID: req.ID, Result: b})
		default:
			_ = enc.Encode(extproc.Response{JSONRPC: "2.0", ID: req.ID,
				Error: &extproc.RPCError{Code: -32601, Message: "no such method"}})
		}
	}
}

// helperCmd 起本测试二进制当子进程。
func helperCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run", "TestHelperPluginProcess")
	cmd.Env = append(os.Environ(), "TENON_EXTPROC_HELPER=1")
	return cmd
}

func TestConnHandshakeAndCall(t *testing.T) {
	cmd := helperCmd(t)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	conn := extproc.NewConn(in, out)
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 握手。
	raw, err := conn.Call(ctx, "handshake", map[string]any{"version": extproc.Version})
	if err != nil {
		t.Fatalf("握手：%v", err)
	}
	var hs extproc.Handshake
	if err := json.Unmarshal(raw, &hs); err != nil {
		t.Fatal(err)
	}
	if err := hs.Validate(); err != nil {
		t.Fatalf("握手校验：%v", err)
	}
	if len(hs.ToolSpecs) != 1 || hs.ToolSpecs[0].Name != "echo_tool" {
		t.Fatalf("工具声明 = %+v", hs.ToolSpecs)
	}

	// 工具调用。
	res, err := conn.Call(ctx, "tools/call", map[string]any{"name": "echo_tool", "args": map[string]string{"x": "1"}})
	if err != nil {
		t.Fatalf("工具调用：%v", err)
	}
	var out2 struct{ Text string }
	if err := json.Unmarshal(res, &out2); err != nil {
		t.Fatal(err)
	}
	if out2.Text == "" || out2.Text[:5] != "echo:" {
		t.Fatalf("工具返回 = %q", out2.Text)
	}
	// 未知方法 → RPCError。
	if _, err := conn.Call(ctx, "nope", nil); err == nil {
		t.Fatal("未知方法应返回错误")
	}
}

// 握手报告越界能力（guard）时 Validate 拒绝——端到端里组件会因此拒绝启动。
func TestHandshakeGuardRejectedE2E(t *testing.T) {
	h := extproc.Handshake{Version: extproc.Version, Capabilities: []string{extproc.CapTools, "guard"}}
	if err := h.Validate(); err == nil {
		t.Fatal("guard 能力应被拒")
	}
	_ = fmt.Sprint
}

// 中间件：插件 deny 的工具调用被拦（fail_closed 语义），放行的照常执行。
func TestMiddlewareDeny(t *testing.T) {
	conn, cleanup := startHelper(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 先握手，让插件声明 middleware。
	if _, err := conn.Call(ctx, "handshake", map[string]any{"version": extproc.Version}); err != nil {
		t.Fatal(err)
	}
	// 直接走 middleware 方法：deny 带 bad 的。
	raw, err := conn.Call(ctx, "middleware", extproc.MiddlewareCall{Point: "tool_call", Tool: "bad_tool"})
	if err != nil {
		t.Fatal(err)
	}
	var res extproc.MiddlewareResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "deny" {
		t.Fatalf("bad_tool 应被 deny：%+v", res)
	}
	// 放行的工具返回空 verdict。
	raw2, err := conn.Call(ctx, "middleware", extproc.MiddlewareCall{Point: "tool_call", Tool: "ok_tool"})
	if err != nil {
		t.Fatal(err)
	}
	var res2 extproc.MiddlewareResult
	_ = json.Unmarshal(raw2, &res2)
	if res2.Verdict != "" {
		t.Fatalf("ok_tool 应放行：%+v", res2)
	}
}

// 握手报告的中间件与事件经 Validate 通过（能力已声明）。
func TestHandshakeMiddlewareAndEvents(t *testing.T) {
	h := extproc.Handshake{Version: extproc.Version,
		Capabilities: []string{extproc.CapMiddleware, extproc.CapEvents},
		Middleware:   []string{"tool_call", "deliver"}, Events: []string{"plugin.*"}}
	if err := h.Validate(); err != nil {
		t.Fatalf("合法握手应通过：%v", err)
	}
	// 报告中间件但没声明 middleware 能力 → 拒。
	bad := extproc.Handshake{Version: extproc.Version, Capabilities: []string{extproc.CapEvents}, Middleware: []string{"tool_call"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("缺 middleware 能力应被拒")
	}
}

// startHelper 起 helper 子进程并返回已连好的 Conn。
func startHelper(t *testing.T) (*extproc.Conn, func()) {
	t.Helper()
	cmd := helperCmd(t)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	conn := extproc.NewConn(in, out)
	cleanup := func() {
		conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return conn, cleanup
}

// TestPlugCrashRestartReconnects覆盖真实链路：进程崩溃→Supervisor重启→组件重握手→回桥恢复。
func TestPlugCrashRestartReconnects(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &plug{cfg: Config{
		Command:    []string{exe, "-test.run", "^TestHelperPluginProcess$"},
		Env:        []string{"TENON_EXTPROC_HELPER=1"},
		MaxCrashes: 2,
	}}
	if err := p.startAndHandshake(); err != nil {
		t.Fatalf("初次启动握手：%v", err)
	}
	defer func() { _ = p.sup.Stop() }()

	//在进程仍稳定时调整测试时序；重启的 startLocked会读取这些配置。
	p.sup.mu.Lock()
	oldCmd := p.sup.cmd
	p.sup.Backoff = 10 * time.Millisecond
	p.sup.StableFor = 50 * time.Millisecond
	p.sup.mu.Unlock()
	oldConn := p.getConn()
	if oldCmd == nil || oldCmd.Process == nil || oldConn == nil {
		t.Fatal("初次启动后缺少进程或连接")
	}

	//直接杀进程模拟崩溃；握手成功并替换连接后，p.conn才会不同于 oldConn。
	if err := oldCmd.Process.Kill(); err != nil {
		t.Fatalf("杀掉插件进程：%v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		current := p.getConn()
		p.sup.mu.Lock()
		newCmd := p.sup.cmd
		running := p.sup.running
		p.sup.mu.Unlock()
		if current != nil && current != oldConn && newCmd != oldCmd && running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("进程重启后没有重建并完成握手")
		}
		time.Sleep(20 * time.Millisecond)
	}

	//稳定运行后清零「连续崩溃」计数，不能把数小时前的崩溃永久累积。
	for {
		p.sup.mu.Lock()
		crashes := p.sup.crashes
		p.sup.mu.Unlock()
		if crashes == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("稳定运行后崩溃计数未清零：%d", crashes)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !p.sup.Available() {
		t.Fatal("成功重启并稳定运行后进程不应不可用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// tool_call中间件必须重新连到新进程，而不是因旧 Conn关闭而全部 Denied。
	calledNext := false
	resp, err := p.toolCallMw()(ctx, component.ToolCallReq{Tool: "echo_tool"},
		func(context.Context, component.ToolCallReq) (component.ToolCallResp, error) {
			calledNext = true
			return component.ToolCallResp{}, nil
		})
	if err != nil {
		t.Fatalf("重启后的 tool_call中间件：%v", err)
	}
	if resp.Denied || !calledNext {
		t.Fatalf("重启后的 tool_call应放行：resp=%+v calledNext=%v", resp, calledNext)
	}

	//实际调用一次外部工具，证明新 Conn不仅能握手，也能继续完成 JSON-RPC回桥。
	p.mu.RLock()
	specs := append([]extproc.ToolSpec(nil), p.tools...)
	if len(specs) != 1 {
		p.mu.RUnlock()
		t.Fatalf("重启后的工具声明 = %+v", specs)
	}
	spec := specs[0]
	p.mu.RUnlock()
	out, err := p.toolOf(spec).Call(ctx, nil, json.RawMessage(`{"after":"restart"}`))
	if err != nil {
		t.Fatalf("重启后的工具调用：%v", err)
	}
	if out.Status != session.ToolOK {
		t.Fatalf("重启后的工具调用失败：%+v", out)
	}
	want := `echo:{"after":"restart"}`
	if len(out.Content) != 1 || out.Content[0].Text != want {
		t.Fatalf("重启后的工具返回 = %+v，想要 %q", out.Content, want)
	}
}

// itoa 小整数转字符串（避免为测试再引 strconv 之外的东西）。
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// 匹配器与资源渲染经回桥转发给插件（T6b 剩余能力）。
func TestMatcherAndRenderBridge(t *testing.T) {
	conn, cleanup := startHelper(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 握手拿到 matcher / resource 声明。
	raw, err := conn.Call(ctx, "handshake", map[string]any{"version": extproc.Version})
	if err != nil {
		t.Fatal(err)
	}
	var hs extproc.Handshake
	if err := json.Unmarshal(raw, &hs); err != nil {
		t.Fatal(err)
	}
	if err := hs.Validate(); err != nil {
		t.Fatalf("握手校验：%v", err)
	}
	if len(hs.MatcherSpecs) != 1 || hs.MatcherSpecs[0].Name != "kw" {
		t.Fatalf("匹配器声明 = %+v", hs.MatcherSpecs)
	}
	if len(hs.ResourceSpecs) != 1 || hs.ResourceSpecs[0].Name != "ticket" {
		t.Fatalf("资源类型声明 = %+v", hs.ResourceSpecs)
	}

	// matcher：body 含关键字 → 命中且 mode=wake。
	mraw, err := conn.Call(ctx, "matcher", extproc.MatchCall{
		Matcher: "kw",
		Record:  extproc.MatchRecord{Resource: "ticket", Seq: 7, Body: "紧急：服务器宕机"},
		Params:  map[string]string{"keyword": "紧急"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var mr extproc.MatchResult
	if err := json.Unmarshal(mraw, &mr); err != nil {
		t.Fatal(err)
	}
	if !mr.Matched || mr.Mode != "wake" {
		t.Fatalf("含关键字应命中且 mode=wake：%+v", mr)
	}
	// 不含关键字 → 不命中。
	mraw2, _ := conn.Call(ctx, "matcher", extproc.MatchCall{
		Matcher: "kw", Record: extproc.MatchRecord{Body: "普通工单"},
		Params: map[string]string{"keyword": "紧急"},
	})
	var mr2 extproc.MatchResult
	_ = json.Unmarshal(mraw2, &mr2)
	if mr2.Matched {
		t.Fatalf("不含关键字不该命中：%+v", mr2)
	}

	// render：按资源名与序号渲染。
	rraw, err := conn.Call(ctx, "render", extproc.RenderCall{
		Resource: "ticket", Record: extproc.MatchRecord{Seq: 7, Body: "工单内容"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var rr extproc.RenderResult
	if err := json.Unmarshal(rraw, &rr); err != nil {
		t.Fatal(err)
	}
	if rr.Text != "[ticket#7] 工单内容" {
		t.Fatalf("渲染 = %q", rr.Text)
	}
}

// 握手报告的 MatcherSpecs / ResourceSpecs 与能力声明不一致时拒绝。
func TestHandshakeSpecCapMismatch(t *testing.T) {
	// 有 MatcherSpecs 但没声明 matcher 能力。
	h := extproc.Handshake{Version: extproc.Version, Capabilities: []string{extproc.CapTools},
		MatcherSpecs: []extproc.MatcherSpec{{Name: "m"}}}
	if err := h.Validate(); err == nil {
		t.Fatal("有匹配器声明但没声明能力应被拒")
	}
	// 有 ResourceSpecs 但没声明 resource 能力。
	h2 := extproc.Handshake{Version: extproc.Version, Capabilities: []string{extproc.CapTools},
		ResourceSpecs: []extproc.ResourceSpec{{Name: "r"}}}
	if err := h2.Validate(); err == nil {
		t.Fatal("有资源类型声明但没声明能力应被拒")
	}
	// 声明里缺名字。
	h3 := extproc.Handshake{Version: extproc.Version, Capabilities: []string{extproc.CapMatcher},
		MatcherSpecs: []extproc.MatcherSpec{{}}}
	if err := h3.Validate(); err == nil {
		t.Fatal("匹配器声明缺名字应被拒")
	}
}

// Deliver 中间件经回桥转发：插件 deny 则拦截，放行则继续。
func TestDeliverMiddlewareBridge(t *testing.T) {
	conn, cleanup := startHelper(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := conn.Call(ctx, "handshake", map[string]any{"version": extproc.Version}); err != nil {
		t.Fatal(err)
	}
	// body 含 block → deny。
	raw, err := conn.Call(ctx, "deliver", map[string]any{
		"session_id": "s1", "envelope": map[string]string{"body": "please block me"}, "from_remote": false})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "deny" {
		t.Fatalf("含 block 应被 deny：%+v", res)
	}
	// 不含 block → 放行（verdict 空）。
	raw2, err := conn.Call(ctx, "deliver", map[string]any{
		"session_id": "s1", "envelope": map[string]string{"body": "normal"}, "from_remote": false})
	if err != nil {
		t.Fatal(err)
	}
	var res2 struct {
		Verdict string `json:"verdict"`
	}
	_ = json.Unmarshal(raw2, &res2)
	if res2.Verdict != "" {
		t.Fatalf("普通信封应放行：%+v", res2)
	}
}
