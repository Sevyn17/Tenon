package appapi

import (
	"fmt"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/store"
)

// 会话属于别的工作区：会触发执行、改运行配置、改文件的命令返回 invalid（other_workspace，带 workspace / workspace_id）；
// 只动元数据的命令照常放行（数据页跨工作区清除、导出不能被误伤）；没记工作区的旧会话按当前工作区处理。
func TestWorkspaceGuard(t *testing.T) {
	app := newApp(t)
	app.o.Host.Workspace = "wa"
	storeSession(t, app, store.SessionMeta{ID: "other", Title: "别处", Workspace: "wb"})
	storeSession(t, app, store.SessionMeta{ID: "legacy", Title: "旧数据"})

	blocked := []struct{ cmd, args string }{
		{"session.send", `{"session":"%s","text":"hi","client_id":"c1"}`},
		{"session.steer", `{"session":"%s","text":"hi","client_id":"c2"}`},
		{"session.rewind", `{"session":"%s","to_turn":1}`},
		{"session.compact", `{"session":"%s"}`},
		{"session.fork", `{"session":"%s"}`},
		{"session.settings", `{"session":"%s","max_steps":3}`},
		{"mode.set", `{"session":"%s","mode":"ask"}`},
		{"changes.revert", `{"session":"%s"}`},
	}
	for _, c := range blocked {
		_, e := call2(t, app, c.cmd, fmt.Sprintf(c.args, "other"))
		if e == nil || e.Code != wire.CodeInvalid || e.Details["reason"] != wire.InvalidOtherWorkspace {
			t.Fatalf("%s 跨工作区应被拒（invalid/other_workspace），得到 %+v", c.cmd, e)
		}
		if e.Details["workspace"] != "wb" || e.Details["workspace_id"] != wire.WorkspaceID("wb") {
			t.Fatalf("%s 错误应带工作区：%+v", c.cmd, e.Details)
		}
		// 旧会话（没记工作区）不按"别的工作区"拒：即使失败也不能是 other_workspace。
		if _, e := call2(t, app, c.cmd, fmt.Sprintf(c.args, "legacy")); e != nil && e.Details["reason"] == wire.InvalidOtherWorkspace {
			t.Fatalf("%s 旧会话不应按跨工作区拒：%+v", c.cmd, e)
		}
	}

	allowed := []struct{ cmd, args string }{
		{"session.get", `{"session":"other"}`},
		{"session.entries", `{"session":"other"}`},
		{"session.locate", `{"session":"other"}`},
		{"session.update", `{"session":"other","title":"改名"}`},
		{"session.cancel", `{"session":"other"}`},
		{"session.delete", `{"session":"other"}`},
	}
	for _, c := range allowed {
		if _, e := call2(t, app, c.cmd, c.args); e != nil && e.Details["reason"] == wire.InvalidOtherWorkspace {
			t.Fatalf("%s 应放行，得到 %+v", c.cmd, e)
		} else if e != nil {
			t.Fatalf("%s 失败：%+v", c.cmd, e)
		}
	}
}

// 宿主没有工作区（CLI 单目录）时不拦。
func TestWorkspaceGuardNoHostWorkspace(t *testing.T) {
	app := newApp(t)
	storeSession(t, app, store.SessionMeta{ID: "other", Workspace: "wb"})
	if err := app.inWorkspace(t.Context(), "other"); err != nil {
		t.Fatalf("宿主无工作区时不应拦：%v", err)
	}
}
