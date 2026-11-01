package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

func callFileTool(t *testing.T, tl *toolhub.Tool, args string) toolhub.Output {
	t.Helper()
	out, err := tl.Call(context.Background(), &toolhub.Cx{}, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func outText(o toolhub.Output) string {
	s := ""
	for _, p := range o.Content {
		s += p.Text
	}
	return s
}

func TestWriteFileAndListDir(t *testing.T) {
	root := t.TempDir()
	w := WriteFile(root)
	if w.Risk != toolhub.Write {
		t.Fatal("write_file 应为 Write")
	}
	if o := callFileTool(t, w, `{"path":"a/b.txt","content":"hello"}`); o.Status != session.ToolOK {
		t.Fatalf("写入失败: %s", outText(o))
	}
	b, err := os.ReadFile(filepath.Join(root, "a", "b.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("内容 = %q %v", b, err)
	}
	if o := callFileTool(t, w, `{"path":"../x.txt","content":"x"}`); o.Status != session.ToolError {
		t.Fatal("越界应失败")
	}
	if o := callFileTool(t, w, `{"path":"a","content":"x"}`); o.Status != session.ToolError {
		t.Fatal("写目录应失败")
	}
	l := ListDir(root)
	if got := outText(callFileTool(t, l, `{}`)); got != "a/" {
		t.Fatalf("list_dir = %q", got)
	}
	if got := outText(callFileTool(t, l, `{"path":"a"}`)); got != "b.txt" {
		t.Fatalf("list_dir a = %q", got)
	}
	if o := callFileTool(t, l, `{"path":".."}`); o.Status != session.ToolError || !strings.Contains(outText(o), "超出") {
		t.Fatalf("越界应失败: %s", outText(o))
	}
}
