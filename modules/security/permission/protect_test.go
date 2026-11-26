package permission

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Mortise docs/63 W-6：受保护目录先于一切规则拒绝，full 档位加 allow 规则也覆盖不了。
// 路径用 TempDir 构造，Windows 上是带盘符的真实形态。
func TestProtectedDirs(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mortise")
	root := t.TempDir()
	p := Policy{Mode: Full, Root: root, Protected: []string{home},
		Rules: []Rule{{Tool: "*", Action: Allow}}}
	cx := func(name string) *toolhub.Cx {
		return &toolhub.Cx{Tool: &toolhub.Tool{Spec: session.ToolSpec{Name: name}}}
	}
	escape, err := filepath.Rel(root, filepath.Join(home, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	path := func(s string) string { b, _ := json.Marshal(map[string]string{"path": s}); return string(b) }
	cmd := func(s string) string { b, _ := json.Marshal(map[string]string{"command": s}); return string(b) }
	cases := []struct {
		name, tool, args string
		deny             bool
	}{
		{"读 home 下文件", "read_file", path(filepath.Join(home, "sessions.db")), true},
		{"home 本身", "list_dir", path(home), true},
		{"相对路径跳出到 home", "read_file", path(escape), true},
		{"前缀相同的兄弟目录不算", "read_file", path(filepath.Join(home+"-old", "x")), false},
		{"工作区文件", "read_file", path("src/a.go"), false},
		{"命令直接读", "shell", cmd("cat " + filepath.Join(home, "config.toml")), true},
		{"复合命令任一段", "shell", cmd("ls && type " + filepath.Join(home, "keys")), true},
		{"启动器内层", "shell", cmd("bash -c 'cp " + filepath.ToSlash(filepath.Join(home, "db")) + " /tmp'"), true},
		{"普通命令", "shell", cmd("go test ./..."), false},
	}
	for _, c := range cases {
		d := p.Decide(cx(c.tool), json.RawMessage(c.args))
		if (d.Action == Deny) != c.deny {
			t.Errorf("%s：得到 %s（%s）", c.name, d.Action, d.Reason)
		}
	}
}

// Windows 写法：反斜杠、大小写都归一。
func TestProtectedDirsWindowsForm(t *testing.T) {
	old := foldCase
	foldCase = true
	defer func() { foldCase = old }()
	p := Policy{Mode: Full, Protected: []string{`C:\Users\U\.mortise`}}
	cx := &toolhub.Cx{Tool: &toolhub.Tool{Spec: session.ToolSpec{Name: "shell"}}}
	d := p.Decide(cx, json.RawMessage(`{"command":"type c:/users/u/.MORTISE/keys.json"}`))
	if d.Action != Deny {
		t.Fatalf("Windows 写法应被拒：%s（%s）", d.Action, d.Reason)
	}
}
