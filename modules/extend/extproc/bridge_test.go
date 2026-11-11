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
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/extproc"
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
				Capabilities: []string{extproc.CapTools},
				Tools:        []string{"echo_tool"},
				ToolSpecs:    []extproc.ToolSpec{{Name: "echo_tool", Description: "回显"}}}
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
