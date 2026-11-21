package apphost

// docs/51 第 6 节：mode.show 的权限项字段、mode.save / remove / setDefault 走真实的 OpenWorkspaceManager 与 permission 插件
// （临时 home，不碰 ~/.tenon）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/appid"
)

func openModeEnv(t *testing.T) (*WorkspaceManager, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(appid.Get().EnvName("HOME"), home)
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	wm, err := OpenWorkspaceManager(WorkspaceOptions{Flags: Flags{Workdir: t.TempDir(), MaxSteps: -1},
		RecentPath: filepath.Join(home, "recent.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wm.Close)
	return wm, home
}

func modeItem(r wire.ModeShowResult, id string) (wire.ModeItem, bool) {
	for _, it := range r.Items {
		if it.Item == id {
			return it, true
		}
	}
	return wire.ModeItem{}, false
}

func TestModeEditFlow(t *testing.T) {
	wm, home := openModeEnv(t)
	h := trustCall[wire.HelloResult](t, wm, "hello", `{}`)
	if !slices.Contains(h.Capabilities, appapi.CapModeEdit) {
		t.Fatalf("应报 %s：%v", appapi.CapModeEdit, h.Capabilities)
	}
	// 建一个会话，权限项注册表与「项 → 工具」才登记
	trustCall[wire.SessionSnapshot](t, wm, "session.create", `{"session":"s1"}`)

	// mode.show：字段齐全；NoAuth 工具不出现在任何项的 tools 里
	sh := trustCall[wire.ModeShowResult](t, wm, "mode.show", `{"mode":"workspace"}`)
	if !sh.Builtin || sh.Editable || sh.Name == "" || len(sh.Items) == 0 {
		t.Fatalf("内置档位：%+v", sh)
	}
	ex, ok := modeItem(sh, "shell.exec")
	if !ok || ex.Type != "match" || ex.Class != "exec" || ex.Bounded || ex.Owner == "" || !slices.Contains(ex.Tools, "shell") ||
		(ex.Value != "allow" && ex.Value != "ask" && ex.Value != "deny") {
		t.Fatalf("shell.exec：%+v", ex)
	}
	fr, ok := modeItem(sh, "fs.read")
	if !ok || fr.Class != "read" || fr.Type == "" || len(fr.Tools) == 0 {
		t.Fatalf("fs.read：%+v", fr)
	}
	if sb, ok := modeItem(sh, "sandbox.level"); !ok || sb.Type != "level" || len(sb.Levels) == 0 {
		t.Fatalf("sandbox.level：%+v", sb)
	}
	for _, it := range sh.Items {
		for _, n := range it.Tools {
			if n == "ask_user" || n == "todo_write" {
				t.Fatalf("NoAuth 工具 %s 不该出现在 %s", n, it.Item)
			}
		}
	}

	// mode.save：内置 id → invalid；新建
	if _, pe := mcpCall(t, wm, "mode.save", `{"mode":{"id":"workspace"}}`); pe == nil || pe.Code != wire.CodeInvalid {
		t.Fatalf("内置应 invalid：%v", pe)
	}
	draft := `{"mode":{"id":"mine","name":"我的","base":"workspace","items":{"shell.exec":{"default":"ask","allow":["git *"],"deny":["rm -rf:*"]}}},"create":true}`
	sv := trustCall[wire.ModeSaveResult](t, wm, "mode.save", draft)
	if sv.Revision == "" || sv.Mode.ID != "mine" || sv.Mode.Builtin {
		t.Fatalf("save：%+v", sv)
	}
	if _, err := os.Stat(filepath.Join(home, "modes", "mine.toml")); err != nil {
		t.Fatal(err)
	}
	if _, pe := mcpCall(t, wm, "mode.save", draft); pe == nil || pe.Code != wire.CodeConflict || pe.Details["reason"] != wire.ConflictExists {
		t.Fatalf("重名 create 应 conflict/exists：%v", pe)
	}
	mine := trustCall[wire.ModeShowResult](t, wm, "mode.show", `{"mode":"mine"}`)
	ex, _ = modeItem(mine, "shell.exec")
	if !mine.Editable || mine.Revision != sv.Revision || mine.Base != "workspace" || !ex.Overridden || ex.Value != "ask" ||
		!slices.Contains(ex.Allow, "git *") || !slices.Contains(ex.Deny, "rm -rf:*") {
		t.Fatalf("mine：%+v %+v", mine, ex)
	}
	// revision 冲突：带旧版本改写 → stale，details.revision 是当前版本
	upd := `{"mode":{"id":"mine","base":"workspace","items":{"shell.exec":{"default":"deny"}}},"revision":"0000"}`
	if _, pe := mcpCall(t, wm, "mode.save", upd); pe == nil || pe.Code != wire.CodeConflict || pe.Details["reason"] != wire.ConflictStale ||
		pe.Details["revision"] != sv.Revision {
		t.Fatalf("旧版本应 conflict/stale：%v", pe)
	}
	upd = strings.Replace(upd, `"0000"`, `"`+sv.Revision+`"`, 1)
	sv2 := trustCall[wire.ModeSaveResult](t, wm, "mode.save", upd)
	if sv2.Revision == sv.Revision {
		t.Fatal("改写后版本应变")
	}

	// mode.setDefault：不存在 → not_found；设成 mine 后 mode.list 的 default 与新会话都跟着变
	if _, pe := mcpCall(t, wm, "mode.setDefault", `{"id":"nope"}`); pe == nil || pe.Code != wire.CodeNotFound {
		t.Fatalf("不存在应 not_found：%v", pe)
	}
	sd := trustCall[wire.ModeSetDefaultResult](t, wm, "mode.setDefault", `{"id":"mine"}`)
	if sd.Default != "mine" || sd.Pinned != "" {
		t.Fatalf("setDefault：%+v", sd)
	}
	if l := trustCall[wire.ModeListResult](t, wm, "mode.list", `{}`); l.Default != "mine" {
		t.Fatalf("mode.list default：%q", l.Default)
	}
	b, _ := os.ReadFile(filepath.Join(home, "permissions.toml"))
	if !strings.Contains(string(b), `mode = "mine"`) {
		t.Fatalf("permissions.toml：%s", b)
	}
	trustCall[wire.SessionSnapshot](t, wm, "session.create", `{"session":"s2"}`)
	if g := trustCall[wire.ModeSetResult](t, wm, "mode.get", `{"session":"s2"}`); g.State.Mode != "mine" {
		t.Fatalf("新会话应按新缺省档位：%+v", g.State)
	}

	// mode.remove：内置 → invalid，不存在 → not_found；删的是缺省档位 → 退回 workspace 并说明
	if _, pe := mcpCall(t, wm, "mode.remove", `{"id":"full"}`); pe == nil || pe.Code != wire.CodeInvalid {
		t.Fatalf("内置应 invalid：%v", pe)
	}
	if _, pe := mcpCall(t, wm, "mode.remove", `{"id":"ghost"}`); pe == nil || pe.Code != wire.CodeNotFound {
		t.Fatalf("不存在应 not_found：%v", pe)
	}
	if _, pe := mcpCall(t, wm, "mode.remove", `{"id":"mine","revision":"0000"}`); pe == nil || pe.Code != wire.CodeConflict {
		t.Fatalf("旧版本删应 conflict：%v", pe)
	}
	rm := trustCall[wire.ModeRemoveResult](t, wm, "mode.remove", `{"id":"mine","revision":"`+sv2.Revision+`"}`)
	if !rm.FellBack || rm.Default != "workspace" || rm.Note == "" {
		t.Fatalf("remove：%+v", rm)
	}
	if l := trustCall[wire.ModeListResult](t, wm, "mode.list", `{}`); l.Default != "workspace" ||
		slices.ContainsFunc(l.Modes, func(m wire.ModeInfo) bool { return m.ID == "mine" }) {
		t.Fatalf("删后 mode.list：%+v", l)
	}
	trustCall[wire.SessionSnapshot](t, wm, "session.create", `{"session":"s3"}`)
	if g := trustCall[wire.ModeSetResult](t, wm, "mode.get", `{"session":"s3"}`); g.State.Mode != "workspace" {
		t.Fatalf("删后新会话应回 workspace：%+v", g.State)
	}
}

// viewer 不能写档位（forbidden），也不留下文件；mode.show 仍可读。
func TestModeEditViewerForbidden(t *testing.T) {
	wm, home := openModeEnv(t)
	v := wm.Current().Built.API.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	if _, err := v.Call(context.Background(), "mode.show", []byte(`{"mode":"ask"}`)); err != nil {
		t.Fatalf("viewer mode.show：%v", err)
	}
	for cmd, args := range map[string]string{
		"mode.save":       `{"mode":{"id":"v1","base":"ask"}}`,
		"mode.remove":     `{"id":"v1"}`,
		"mode.setDefault": `{"id":"ask"}`,
	} {
		_, err := v.Call(context.Background(), cmd, []byte(args))
		var pe *appapi.Error
		if !errors.As(err, &pe) || pe.Code != wire.CodeForbidden {
			t.Fatalf("viewer 不能 %s：%v", cmd, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "modes", "v1.toml")); !os.IsNotExist(err) {
		t.Fatalf("viewer 不应写出档位：%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "permissions.toml")); !os.IsNotExist(err) {
		t.Fatalf("viewer 不应写 permissions.toml：%v", err)
	}
}
