package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ybase/runtime/session"
	"ybase/runtime/toolhub"
)

func callAs(t *testing.T, tl *toolhub.Tool, sid, args string) toolhub.Output {
	t.Helper()
	out, err := tl.Call(context.Background(), &toolhub.Cx{SessionID: sid}, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 读后写（2026-11-04 审查 P1-43）：覆盖 / 编辑已存在的文件前，本会话必须读过它，且之后没被别人改过。
// 新建文件不受限；自己刚写的结果算读过；另一个会话读过不算。
func TestReadBeforeWrite(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	if err := os.WriteFile(p, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &Tracker{}
	rd, ed, wr := ReadFile(root, tr), Edit(root, tr), WriteFile(root, tr)

	// 没读过：拒绝覆盖与编辑。
	if o := callAs(t, wr, "s1", `{"path":"a.txt","content":"x"}`); o.Status != session.ToolError || !strings.Contains(outText(o), "还没读过") {
		t.Fatalf("没读过就覆盖应被拒：%q", outText(o))
	}
	if o := callAs(t, ed, "s1", `{"path":"a.txt","edits":[{"old_text":"one","new_text":"two"}]}`); o.Status != session.ToolError {
		t.Fatalf("没读过就编辑应被拒：%q", outText(o))
	}
	// 新建文件不受限。
	if o := callAs(t, wr, "s1", `{"path":"new.txt","content":"n"}`); o.Status != session.ToolOK {
		t.Fatalf("新建文件不应受限：%q", outText(o))
	}
	// 另一个会话读过不算。
	callAs(t, rd, "s2", `{"path":"a.txt"}`)
	if o := callAs(t, ed, "s1", `{"path":"a.txt","edits":[{"old_text":"one","new_text":"two"}]}`); o.Status != session.ToolError {
		t.Fatal("别的会话读过不应放行")
	}
	// 读过：可以编辑，之后连续编辑不必重读。
	callAs(t, rd, "s1", `{"path":"a.txt"}`)
	if o := callAs(t, ed, "s1", `{"path":"a.txt","edits":[{"old_text":"one","new_text":"two"}]}`); o.Status != session.ToolOK {
		t.Fatalf("读过后编辑应成功：%q", outText(o))
	}
	if o := callAs(t, ed, "s1", `{"path":"a.txt","edits":[{"old_text":"two","new_text":"three"}]}`); o.Status != session.ToolOK {
		t.Fatalf("自己写过的文件连续编辑应成功：%q", outText(o))
	}
	// 读过之后被外部改过：拒绝，提示重读。
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(p, []byte("user edit, longer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if o := callAs(t, wr, "s1", `{"path":"a.txt","content":"x"}`); o.Status != session.ToolError || !strings.Contains(outText(o), "被改过") {
		t.Fatalf("外部改过后应拒绝：%q", outText(o))
	}
	callAs(t, rd, "s1", `{"path":"a.txt"}`)
	if o := callAs(t, wr, "s1", `{"path":"a.txt","content":"x"}`); o.Status != session.ToolOK {
		t.Fatalf("重读后应能写：%q", outText(o))
	}
}

// 组件装配的 fs 工具共用一份记录：plugin 里 read_file 读过的文件，edit 才放行。
func TestPluginToolsShareTracker(t *testing.T) {
	tr := &Tracker{}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	callAs(t, ReadFile(root, tr), "s", `{"path":"b.txt"}`)
	if o := callAs(t, Edit(root, tr), "s", `{"path":"b.txt","edits":[{"old_text":"b","new_text":"c"}]}`); o.Status != session.ToolOK {
		t.Fatalf("同一记录下读后编辑应成功：%q", outText(o))
	}
}
