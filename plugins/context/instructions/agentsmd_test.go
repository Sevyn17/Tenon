package instructions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ybase-kernel/core/callctx"
	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

func put(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstructionsBaselineAndDiscover(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(home, "AGENTS.md"), "全局规则")
	put(t, filepath.Join(root, "AGENTS.md"), "根规则")
	put(t, filepath.Join(root, "CLAUDE.md"), "不应读取")
	put(t, filepath.Join(root, "app", "AGENTS.md"), "根规则") // 重复内容折叠
	put(t, filepath.Join(root, "app", "AGENTS.local.md"), "本地规则")
	put(t, filepath.Join(root, "app", "sub", "AGENTS.md"), "子目录规则")
	put(t, filepath.Join(root, "app", "sub", "x.go"), "package x")

	in := &Instructions{Workdir: filepath.Join(root, "app"), Home: home}
	text, err := in.Baseline()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"全局规则", "根规则", "本地规则"} {
		if !strings.Contains(text, want) {
			t.Fatalf("基线缺 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "不应读取") || strings.Contains(text, "子目录规则") {
		t.Fatalf("基线多读:\n%s", text)
	}
	if strings.Count(text, "根规则") != 1 {
		t.Fatalf("重复内容未折叠:\n%s", text)
	}
	if strings.Index(text, "全局规则") > strings.Index(text, "本地规则") {
		t.Fatal("顺序应从宽泛到具体")
	}
	if d := in.Discover("sub/x.go"); !strings.Contains(d, "子目录规则") {
		t.Fatalf("嵌套发现失败: %q", d)
	}
	if d := in.Discover("sub/x.go"); d != "" {
		t.Fatalf("同一文件只装载一次: %q", d)
	}
	if d := in.Discover("../outside.txt"); d != "" {
		t.Fatalf("工作目录外不应发现: %q", d)
	}
}

func TestInstructionsBudget(t *testing.T) {
	root := t.TempDir()
	put(t, filepath.Join(root, "AGENTS.md"), strings.Repeat("规", 1000))
	in := &Instructions{Workdir: root, MaxBytes: 200, MaxFile: 100}
	text, err := in.Baseline()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "已截断") {
		t.Fatalf("应截断: %s", text)
	}
	if !json.Valid([]byte(`"` + strings.ReplaceAll(text, "\n", "") + `"`)) {
		t.Fatal("截断破坏了 UTF-8")
	}
}

func TestInstructionsLayer(t *testing.T) {
	root := t.TempDir()
	put(t, filepath.Join(root, "pkg", "AGENTS.md"), "包规则")
	put(t, filepath.Join(root, "pkg", "a.txt"), "hi")
	in := &Instructions{Workdir: root}
	if _, err := in.Baseline(); err != nil {
		t.Fatal(err)
	}
	h := toolhub.New(toolhub.Options{})
	if err := h.Register(fakeRead(root)); err != nil {
		t.Fatal(err)
	}
	h.AddLayer(in.Layer())
	run := h.Execute(context.Background(), kctx("s"), []session.ToolCall{
		{ID: "1", Name: "read_file", Args: json.RawMessage(`{"path":"pkg/a.txt"}`)},
		{ID: "2", Name: "read_file", Args: json.RawMessage(`{"path":"pkg/a.txt"}`)},
	}, nil)
	var outs []string
	for {
		r, ok := run.Next()
		if !ok {
			break
		}
		var b strings.Builder
		for _, p := range r.Output.Content {
			b.WriteString(p.Text)
		}
		outs = append(outs, b.String())
	}
	if len(outs) != 2 {
		t.Fatalf("结果数 %d", len(outs))
	}
	if n := strings.Count(outs[0]+outs[1], "包规则"); n != 1 {
		t.Fatalf("包规则应只出现一次，实际 %d: %q", n, outs)
	}
}

// fakeRead 测试用 read_file（插件之间不互相 import，不用 fs 插件的实现）。
func fakeRead(root string) *toolhub.Tool {
	return toolhub.Simple("read_file", "读文件", `{"type":"object","properties":{"path":{"type":"string"}}}`, func(_ context.Context, raw json.RawMessage) (string, error) {
		var a struct{ Path string }
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", err
		}
		b, err := os.ReadFile(filepath.Join(root, a.Path))
		return string(b), err
	})
}

// kctx 测试用调用上下文（框架设计 04 §4.4）。
func kctx(id string) *callctx.Ctx { return callctx.New(callctx.Options{SessionID: id}) }
