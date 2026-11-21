package permission

// docs/51 第 6 节：SaveMode / RemoveMode / SetDefaultMode 写用户级文件（临时 home），注册表与缺省档位随之变化。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func editSessions(t *testing.T) (*Sessions, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	s, err := open(t.TempDir(), "", nil, "", "", func() store.Store { return nil })
	if err != nil {
		t.Fatal(err)
	}
	s.UserHome = home
	s.SetItems([]toolhub.Item{
		{ID: "t.cmd", Title: "命令", Type: toolhub.ItemMatch, Class: toolhub.EffectExec, Owner: "test",
			Subject: func(string, json.RawMessage) ([]string, error) { return nil, nil }},
		{ID: "t.sw", Title: "开关", Type: toolhub.ItemSwitch, Class: toolhub.EffectNetwork, Owner: "test"},
	})
	return s, home
}

func editKind(err error) string {
	var ee *EditError
	if errors.As(err, &ee) {
		return ee.Kind
	}
	return ""
}

func TestSaveRemoveSetDefault(t *testing.T) {
	s, home := editSessions(t)
	e := ModeEdit{ID: "mine", Name: "我的", Base: "workspace", Items: map[string]ItemFile{
		"t.cmd": {Default: "ask", Allow: []string{"git *"}, Deny: []string{"rm -rf:*"}},
		"t.sw":  {Default: "deny"},
	}}
	rev, err := s.SaveMode(e, "", true)
	if err != nil || rev == "" {
		t.Fatalf("save：%q %v", rev, err)
	}
	p := filepath.Join(home, "modes", "mine.toml")
	if FileRevision(p) != rev || !s.UserEditable("mine") || s.ModeRevision("mine") != rev {
		t.Fatalf("版本 / 可编辑：%q %v", FileRevision(p), s.UserEditable("mine"))
	}
	d, ok := s.Registry().Get("mine")
	if !ok || d.Title != "我的" || d.Root != Workspace {
		t.Fatalf("注册表：%+v", d)
	}
	if st, _ := d.ItemOf("t.cmd"); st.Default != toolhub.ActionAsk || len(st.Allow) != 1 || st.Deny[0] != "rm -rf:*" {
		t.Fatalf("t.cmd：%+v", st)
	}
	// create 再来一次 → exists；revision 不对 → stale；带对的 revision 改写
	if _, err := s.SaveMode(e, "", true); editKind(err) != EditExists {
		t.Fatalf("重名应 exists：%v", err)
	}
	if _, err := s.SaveMode(e, "deadbeef", false); editKind(err) != EditStale {
		t.Fatalf("旧版本应 stale：%v", err)
	}
	e.Items["t.sw"] = ItemFile{Default: "allow"}
	rev2, err := s.SaveMode(e, rev, false)
	if err != nil || rev2 == rev {
		t.Fatalf("改写：%v", err)
	}
	// 内置、坏字段 → invalid
	for _, bad := range []ModeEdit{
		{ID: "workspace"}, {ID: "a b"}, {ID: "x", Base: "nope"}, {ID: "x", Base: "x"},
		{ID: "x", Items: map[string]ItemFile{"t.sw": {Default: "maybe"}}},
		{ID: "x", Items: map[string]ItemFile{"t.sw": {Default: "ask", Allow: []string{"a"}}}},
		{ID: "x", Items: map[string]ItemFile{"t.cmd": {Allow: []string{" "}}}},
	} {
		if _, err := s.SaveMode(bad, "", false); editKind(err) != EditInvalid {
			t.Errorf("%+v 应 invalid：%v", bad, err)
		}
	}
	// setDefault：写 permissions.toml，保留别的内容；新会话用它
	cp := filepath.Join(home, "permissions.toml")
	if err := os.WriteFile(cp, []byte("# 注释\nmode = \"ask\"\ndeny = [\"shell(rm -rf:*)\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDefaultMode("nope"); editKind(err) != EditNotFound {
		t.Fatalf("不存在应 not_found：%v", err)
	}
	if pinned, err := s.SetDefaultMode("mine"); err != nil || pinned != "" {
		t.Fatalf("setDefault：%q %v", pinned, err)
	}
	b, _ := os.ReadFile(cp)
	if !strings.Contains(string(b), "mode = \"mine\"") || !strings.Contains(string(b), "# 注释") || !strings.Contains(string(b), "deny") {
		t.Fatalf("permissions.toml：%s", b)
	}
	if s.DefaultMode() != "mine" || s.Mode("new-session") != "mine" {
		t.Fatalf("新会话应用 mine：%s %s", s.DefaultMode(), s.Mode("new-session"))
	}
	// remove：内置 invalid、不存在 not_found、旧版本 stale；删缺省档位退回 workspace
	if _, err := s.RemoveMode("full", ""); editKind(err) != EditInvalid {
		t.Fatalf("删内置应 invalid：%v", err)
	}
	if _, err := s.RemoveMode("ghost", ""); editKind(err) != EditNotFound {
		t.Fatalf("删不存在应 not_found：%v", err)
	}
	if _, err := s.RemoveMode("mine", rev); editKind(err) != EditStale {
		t.Fatalf("旧版本删应 stale：%v", err)
	}
	fb, err := s.RemoveMode("mine", rev2)
	if err != nil || !fb {
		t.Fatalf("remove：%v %v", fb, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("文件应删掉：%v", err)
	}
	if _, ok := s.Registry().Get("mine"); ok || s.DefaultMode() != Default {
		t.Fatalf("删后：缺省 %s", s.DefaultMode())
	}
	if b, _ := os.ReadFile(cp); !strings.Contains(string(b), "mode = \"workspace\"") {
		t.Fatalf("permissions.toml 应退回 workspace：%s", b)
	}
}

// 被别的档位当 base 的档位删不掉，文件保留；Pinned 时 setDefault 只写文件。
func TestRemoveBaseAndPinned(t *testing.T) {
	s, home := editSessions(t)
	if _, err := s.SaveMode(ModeEdit{ID: "a", Base: "ask"}, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveMode(ModeEdit{ID: "b", Base: "a"}, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveMode("a", ""); editKind(err) != EditInvalid {
		t.Fatalf("被继承的档位删除应 invalid：%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "modes", "a.toml")); err != nil {
		t.Fatalf("删除失败时文件应还原：%v", err)
	}
	s.Pinned = "启动参数强制了档位 full"
	if pinned, err := s.SetDefaultMode("a"); err != nil || pinned == "" || s.DefaultMode() == "a" {
		t.Fatalf("pinned：%q %v %s", pinned, err, s.DefaultMode())
	}
	if b, _ := os.ReadFile(filepath.Join(home, "permissions.toml")); !strings.Contains(string(b), "mode = \"a\"") {
		t.Fatalf("pinned 时仍应写文件：%s", b)
	}
	s.UserHome = ""
	if _, err := s.SaveMode(ModeEdit{ID: "c"}, "", true); editKind(err) != EditUnsupported {
		t.Fatalf("没有用户目录应 unsupported：%v", err)
	}
}
