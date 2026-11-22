package apphost

// T-6 / T-7（Mortise docs/67）：WorkspaceManager 透传 DefaultAgent、StoreAgents、Agents：
// 产品内置定义经内存来源进运行时，新建顶层会话绑定缺省定义；切换工作区后仍然生效。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agentdef"
)

func TestWorkspaceManagerDefaultAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	defs := agentdef.Mem()
	if err := defs.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: "main"},
		agentdef.Files{agentdef.ManifestName: []byte("format = \"tenon/v1\"\nid = \"main\"\ndescription = \"主智能体\"\n")}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	wm, err := OpenWorkspaceManager(WorkspaceOptions{Flags: Flags{Workdir: root, MaxSteps: -1},
		RecentPath: filepath.Join(home, "recent.json"), DefaultAgent: "main", StoreAgents: true,
		Agents: []agentdef.Source{defs}})
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()
	check := func(id string) {
		t.Helper()
		api := wm.Current().Built.API
		if _, err := api.Call(context.Background(), "session.create", json.RawMessage(`{"session":"`+id+`"}`)); err != nil {
			t.Fatalf("session.create: %v", err)
		}
		l, err := wm.Current().Runtime.App.Internals().DB.Load(context.Background(), id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if l.Meta.AgentID != "main" || l.Meta.AgentHash == "" {
			t.Fatalf("会话 %s 定义 = %q / %q，期望 main", id, l.Meta.AgentID, l.Meta.AgentHash)
		}
	}
	check("s1")
	// 切换工作区：新 App 沿用同一组选项。
	if _, err := wm.Open(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	check("s2")
	if wm.Current().Runtime.App.StoreAgents() == nil {
		t.Fatal("StoreAgents 应接上存储定义来源")
	}
}
