package apphost

// B1（08 8.3）：真实装配（permission 插件 + 内置工具）下，定义的 tools 白名单与 [use.permission]
// 上限档位都在运行时生效：白名单外的工具不可用；上限档位比会话档位严时按上限判定。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"tenon/runtime/agentdef"
	"tenon/runtime/session"
	"tenon/runtime/store"
)

func TestDefinitionToolsAndUsePermissionEnforced(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TENON_HOME", t.TempDir())
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	rt, err := Start(StartOptions{Flags: Flags{Workdir: root, Yes: true, MaxSteps: -1}, // -yes：会话档位 full
		App: AppOptions{MaxSteps: -1, ModesWorkdir: root}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	built, err := rt.BuildApp()
	if err != nil {
		t.Fatal(err)
	}
	defs := agentdef.Mem()
	put := func(name, body string) {
		t.Helper()
		if err := defs.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: name},
			agentdef.Files{agentdef.ManifestName: []byte("format = \"tenon/v1\"\nid = \"" + name + "\"\ndescription = \"t\"\n" + body)}); err != nil {
			t.Fatal(err)
		}
	}
	put("reader", "tools = [\"read_file\", \"glob\"]\n")
	put("capped", "[use.permission]\nmode = \"read-only\"\n")
	built.Builder.Definitions = agentdef.Multi(rt.Rt.Defs, defs)

	// 1. tools 白名单：shell 不在可用集里。
	a, _, err := built.Builder.Build("s-reader", "reader", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range a.Toolset.Available() {
		if n == "shell" || n == "write_file" {
			t.Fatalf("白名单外的 %s 不应可用：%v", n, a.Toolset.Available())
		}
	}

	// 2. [use.permission] read-only：会话档位是 full，写文件仍按上限判定（read-only 下 write 为 deny）。
	b, cfg, err := built.Builder.Build("s-capped", "capped", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.OpenAs(context.Background(), cfg, store.SessionMeta{ID: "s-capped", AgentID: "capped"}); err != nil {
		t.Fatalf("带 [use.permission] 的会话应能创建: %v", err)
	}
	for _, s := range cfg.Tools {
		if s.Name == "write_file" {
			t.Fatal("上限 read-only 下必然 deny 的 write_file 不应显露给模型")
		}
	}
	run := b.Hub.ExecuteIn(context.Background(), b.Toolset, b.Ctx,
		[]session.ToolCall{{ID: "c1", Name: "write_file", Args: json.RawMessage(`{"path":"x.txt","content":"x"}`)}}, nil)
	res, _ := run.Next()
	out := ""
	for _, p := range res.Output.Content {
		out += p.Text
	}
	if res.Err != nil || !strings.Contains(out, "权限拒绝") || !strings.Contains(out, "智能体权限上限") {
		t.Fatalf("上限档位应拒绝写文件：err=%v out=%q", res.Err, out)
	}
}
