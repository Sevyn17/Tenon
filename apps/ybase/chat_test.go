package main

import (
	"os"
	"path/filepath"
	"testing"

	"ybase/modules/collab/subagent"
	"ybase/modules/collab/workgroup"
)

// CLI 聊天的会话构造器要和桌面端 / api-server 同一份装配：打开 subagent / workgroup 后，
// 子会话按内置定义模板构造（旧实现自建 Builder，漏了定义来源，每次派生都报「未配置定义来源」）。
func TestChatBuilderBuildsChildDefinitions(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("YBASE_HOME", home)
	t.Setenv("YBASE_BASE_URL", "")
	t.Setenv("YBASE_MODEL", "")
	extra := filepath.Join(home, "extra.toml")
	if err := os.WriteFile(extra, []byte("[[plugin]]\nname = \"subagent\"\nenabled = true\n\n[[plugin]]\nname = \"workgroup\"\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := opts{workdir: root, sess: "default", profile: extra, yes: true, maxSteps: -1}
	err := withRuntime(o, func(a *app) error {
		built, err := buildChat(o, a)
		if err != nil {
			return err
		}
		for _, def := range []string{subagent.Definition, workgroup.MemberDefinition} {
			if _, _, err := built.Builder.Build("main/sub-1", def, true); err != nil {
				t.Errorf("构造子会话定义 %s: %v", def, err)
			}
		}
		if a.h.Guards == nil && len(a.rt.Asm.GuardRules()) > 0 {
			t.Error("组件守卫规则应接到宿主")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
