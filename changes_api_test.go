package tenon_test

// 客户端改进 M9：changes.* 与 session.rewind {restore_files} 用真实的 checkpoint 引擎端到端跑
// （脚本模型调 write_file，经 App.API() 调协议命令）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/tenontest"
)

func wf(path, content string) string {
	b, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return string(b)
}

type cpRig struct {
	t   *testing.T
	dir string
	app *tenontest.App
}

func newCPRig(t *testing.T, steps ...tenontest.Step) *cpRig {
	dir := t.TempDir()
	app := tenontest.New(t, tenon.WithWorkdir(dir),
		tenon.WithAgent(tenon.Agent{Name: "w", Tools: []string{"write_file"}}),
		tenon.WithConfig("permission", map[string]any{"force_mode": "full"}),
		tenontest.Model(steps...))
	return &cpRig{t: t, dir: dir, app: app}
}

func (r *cpRig) call(cmd string, args any) (any, *appapi.Error) {
	r.t.Helper()
	b, _ := json.Marshal(args)
	out, err := r.app.API().Call(context.Background(), cmd, b)
	if err == nil {
		return out, nil
	}
	var e *appapi.Error
	if !errors.As(err, &e) {
		r.t.Fatalf("%s：错误不是 *Error：%v", cmd, err)
	}
	return nil, e
}

func (r *cpRig) file(rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(r.dir, rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (r *cpRig) logLen(sid string) int {
	r.t.Helper()
	out, e := r.call("session.get", map[string]any{"session": sid})
	if e != nil {
		r.t.Fatal(e)
	}
	return out.(wire.SessionSnapshot).Log.Len
}

// 两个回合各写一次 a.txt（CRLF、无结尾换行），返回会话 id。
func (r *cpRig) twoWrites(ctx context.Context) string {
	r.t.Helper()
	s, err := r.app.NewSession(ctx, "w", nil)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, text := range []string{"一", "二"} {
		if _, err := s.Send(ctx, text); err != nil {
			r.t.Fatal(err)
		}
	}
	return s.ID()
}

const (
	cpV1 = "\ufeffline1\r\nline2\r\nno-eol"
	cpV2 = "\ufeffline1\r\nCHANGED\r\nno-eol"
)

func twoWriteSteps() []tenontest.Step {
	return []tenontest.Step{
		tenontest.Reply("写一").CallTool("write_file", wf("a.txt", cpV1)), tenontest.Reply("好一"),
		tenontest.Reply("写二").CallTool("write_file", wf("a.txt", cpV2)), tenontest.Reply("好二"),
	}
}

// 正常路径：能力、list、diff、revert（dry_run 与实写）。
func TestChangesCommands(t *testing.T) {
	ctx := context.Background()
	r := newCPRig(t, twoWriteSteps()...)
	h, _ := r.call("hello", map[string]any{})
	if !slices.Contains(h.(wire.HelloResult).Capabilities, "changes") {
		t.Fatalf("应报 changes：%v", h.(wire.HelloResult).Capabilities)
	}
	sid := r.twoWrites(ctx)
	if got, _ := r.file("a.txt"); got != cpV2 {
		t.Fatalf("写入：%q", got)
	}
	out, e := r.call("changes.list", map[string]any{"session": sid})
	if e != nil {
		t.Fatal(e)
	}
	fs := out.(wire.ChangesResult).Files
	if len(fs) != 1 || fs[0].Path != "a.txt" || fs[0].Status != "added" || !fs[0].Revertible ||
		!slices.Equal(fs[0].Turns, []uint32{1, 2}) || fs[0].Added != 3 || fs[0].Conflict != "" {
		t.Fatalf("list：%+v", fs)
	}
	turn := uint32(2)
	out, e = r.call("changes.list", map[string]any{"session": sid, "scope": "turn", "turn": turn})
	if fs := out.(wire.ChangesResult).Files; e != nil || len(fs) != 1 || fs[0].Status != "modified" || fs[0].Added != 1 || fs[0].Removed != 1 {
		t.Fatalf("list turn 2：%+v %v", out, e)
	}
	out, e = r.call("changes.diff", map[string]any{"session": sid, "path": "a.txt", "scope": "turn", "turn": turn})
	d := out.(wire.ChangesDiffResult)
	if e != nil || !strings.Contains(d.Diff, "-line2\r\n") || !strings.Contains(d.Diff, "+CHANGED\r\n") || !strings.HasPrefix(d.Diff, "--- a/a.txt\n+++ b/a.txt\n") {
		t.Fatalf("diff：%+v %v", d, e)
	}
	// 大小写不同的路径也能找到（只在不区分大小写的平台上），反斜杠按正斜杠处理
	if _, e := r.call("changes.diff", map[string]any{"session": sid, "path": ".\\a.txt"}); e != nil {
		t.Fatalf("diff 路径规范化：%v", e)
	}
	// dry_run 不写
	out, e = r.call("changes.revert", map[string]any{"session": sid, "scope": "turn", "turn": turn, "dry_run": true})
	if e != nil || !slices.Equal(out.(wire.ChangesRevertResult).Reverted, []string{"a.txt"}) {
		t.Fatalf("dry_run：%+v %v", out, e)
	}
	if got, _ := r.file("a.txt"); got != cpV2 {
		t.Fatalf("dry_run 不应写：%q", got)
	}
	out, e = r.call("changes.revert", map[string]any{"session": sid, "scope": "turn", "turn": turn})
	if e != nil || !slices.Equal(out.(wire.ChangesRevertResult).Reverted, []string{"a.txt"}) {
		t.Fatalf("revert：%+v %v", out, e)
	}
	if got, _ := r.file("a.txt"); got != cpV1 {
		t.Fatalf("还原后应逐字节等于第一回合写的内容：%q", got)
	}
	// 整个会话还原：新建的文件删掉
	if _, e := r.call("changes.revert", map[string]any{"session": sid}); e != nil {
		t.Fatal(e)
	}
	if _, ok := r.file("a.txt"); ok {
		t.Fatal("会话范围还原后 a.txt 应不存在")
	}
}

// 安全：changes.diff 只按本会话记录取内容；别的会话改过的路径取不到，未知参数（哈希）不起作用。
func TestChangesDiffOtherSession(t *testing.T) {
	ctx := context.Background()
	r := newCPRig(t,
		tenontest.Reply("A").CallTool("write_file", wf("a.txt", "secret-a")), tenontest.Reply("完A"),
		tenontest.Reply("B").CallTool("write_file", wf("b.txt", "secret-b")), tenontest.Reply("完B"))
	sa, _ := r.app.NewSession(ctx, "w", nil)
	if _, err := sa.Send(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	sb, _ := r.app.NewSession(ctx, "w", nil)
	if _, err := sb.Send(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	// A 看不到 B 的文件
	_, e := r.call("changes.diff", map[string]any{"session": sa.ID(), "path": "b.txt"})
	if e == nil || e.Code != wire.CodeNotFound {
		t.Fatalf("别的会话的路径应 not_found：%+v", e)
	}
	// 塞一个 B 的内容哈希也没用：参数里没有这个字段，按路径查仍是 not_found
	_, e = r.call("changes.diff", map[string]any{"session": sa.ID(), "path": "b.txt",
		"hash": "whatever", "after": "whatever"})
	if e == nil || e.Code != wire.CodeNotFound {
		t.Fatalf("带哈希也应 not_found：%+v", e)
	}
	out, _ := r.call("changes.list", map[string]any{"session": sa.ID()})
	for _, f := range out.(wire.ChangesResult).Files {
		if f.Path == "b.txt" {
			t.Fatalf("A 的列表里不应有 B 的文件：%+v", f)
		}
	}
	out, e = r.call("changes.diff", map[string]any{"session": sa.ID(), "path": "a.txt"})
	if e != nil || strings.Contains(out.(wire.ChangesDiffResult).Diff, "secret-b") || !strings.Contains(out.(wire.ChangesDiffResult).Diff, "+secret-a") {
		t.Fatalf("A 的 diff：%+v %v", out, e)
	}
	// A 不能还原 B 的文件：路径只是查找键，本会话没有 → 什么都不写
	out, e = r.call("changes.revert", map[string]any{"session": sa.ID(), "paths": []string{"b.txt"}})
	if e != nil || len(out.(wire.ChangesRevertResult).Reverted) != 0 {
		t.Fatalf("revert 别的会话的文件：%+v %v", out, e)
	}
	if got, _ := r.file("b.txt"); got != "secret-b" {
		t.Fatalf("b.txt 不应被动：%q", got)
	}
}

// rewind {restore_files}：回到第二回合前 → 文件逐字节等于第一回合写的内容；再回到第一回合前 → 文件删除。
func TestRewindRestoreFiles(t *testing.T) {
	ctx := context.Background()
	r := newCPRig(t, twoWriteSteps()...)
	sid := r.twoWrites(ctx)
	out, e := r.call("session.rewind", map[string]any{"session": sid, "to_turn": 2, "restore_files": true})
	if e != nil || !slices.Equal(out.(wire.RewindResult).Files, []string{"a.txt"}) {
		t.Fatalf("rewind 2：%+v %v", out, e)
	}
	if got, _ := r.file("a.txt"); got != cpV1 {
		t.Fatalf("应逐字节复原为第一回合内容：%q", got)
	}
	n := r.logLen(sid)
	if out.(wire.RewindResult).To != n {
		t.Fatalf("to=%d len=%d", out.(wire.RewindResult).To, n)
	}
	// 截掉的第二回合在列表里不再算（会话范围只剩第一回合）
	lo, _ := r.call("changes.list", map[string]any{"session": sid})
	if fs := lo.(wire.ChangesResult).Files; len(fs) != 1 || !slices.Equal(fs[0].Turns, []uint32{1}) || fs[0].Conflict != "" {
		t.Fatalf("rewind 后列表：%+v", fs)
	}
	if _, e := r.call("session.rewind", map[string]any{"session": sid, "to_turn": 1, "restore_files": true}); e != nil {
		t.Fatal(e)
	}
	if _, ok := r.file("a.txt"); ok {
		t.Fatal("回到第一回合前，新建的 a.txt 应被删除")
	}
}

// 外部改过：rewind {restore_files} 与 changes.revert 都返回冲突，日志和文件一个字节都不变；force 才写。
func TestRewindRestoreConflictTouchesNothing(t *testing.T) {
	ctx := context.Background()
	r := newCPRig(t, twoWriteSteps()...)
	sid := r.twoWrites(ctx)
	ext := "用户在编辑器里改的\n"
	if err := os.WriteFile(filepath.Join(r.dir, "a.txt"), []byte(ext), 0o644); err != nil {
		t.Fatal(err)
	}
	n := r.logLen(sid)
	_, e := r.call("session.rewind", map[string]any{"session": sid, "to_turn": 2, "restore_files": true})
	if e == nil || e.Code != wire.CodeConflict || e.Details["reason"] != wire.ConflictFiles {
		t.Fatalf("应为 conflict(files)：%+v", e)
	}
	cs, _ := e.Details["conflicts"].([]wire.ChangeConflict)
	if len(cs) != 1 || cs[0].Path != "a.txt" || cs[0].Reason != "modified" || !cs[0].Forceable {
		t.Fatalf("details.conflicts：%+v", e.Details)
	}
	if got, _ := r.file("a.txt"); got != ext || r.logLen(sid) != n {
		t.Fatalf("冲突时文件 %q、日志 %d→%d 不应变", got, n, r.logLen(sid))
	}
	_, e = r.call("changes.revert", map[string]any{"session": sid})
	if e == nil || e.Code != wire.CodeConflict {
		t.Fatalf("changes.revert 应 conflict：%+v", e)
	}
	if got, _ := r.file("a.txt"); got != ext {
		t.Fatalf("revert 冲突时一字节不写：%q", got)
	}
	lo, _ := r.call("changes.list", map[string]any{"session": sid})
	if fs := lo.(wire.ChangesResult).Files; len(fs) != 1 || fs[0].Conflict != "modified" {
		t.Fatalf("list 应预检出冲突：%+v", fs)
	}
	// force：照写
	if _, e := r.call("session.rewind", map[string]any{"session": sid, "to_turn": 2, "restore_files": true, "force": true}); e != nil {
		t.Fatal(e)
	}
	if got, _ := r.file("a.txt"); got != cpV1 || r.logLen(sid) >= n {
		t.Fatalf("force 后：%q len=%d", got, r.logLen(sid))
	}
}

// 不带新字段：rewind 只截日志，不碰文件，结果里没有 files。
func TestRewindWithoutRestoreKeepsFiles(t *testing.T) {
	ctx := context.Background()
	r := newCPRig(t, twoWriteSteps()...)
	sid := r.twoWrites(ctx)
	out, e := r.call("session.rewind", map[string]any{"session": sid, "to_turn": 2})
	if e != nil || out.(wire.RewindResult).Files != nil {
		t.Fatalf("rewind：%+v %v", out, e)
	}
	b, _ := json.Marshal(out)
	if string(b) != fmt.Sprintf(`{"to":%d}`, out.(wire.RewindResult).To) {
		t.Fatalf("线上形态应与原来一致：%s", b)
	}
	if got, _ := r.file("a.txt"); got != cpV2 {
		t.Fatalf("不带 restore_files 不应碰文件：%q", got)
	}
}
