package apphost

// 子会话不越过父会话的限制（2026-11-04 审查 P2 遗留，subagent 选定义的前提）：父定义的 tools 白名单
// 收窄子会话的可用工具；父定义的 [use.permission] 上限档位叠加到子会话上。原先子会话用内置 subagent
// 定义，两样都不继承：只许读的父会话派出的子智能体能写文件、跑命令。

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"tenon/runtime/agentdef"
	"tenon/runtime/session"
	"tenon/runtime/store"
)

func TestChildInheritsParentLimits(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TENON_HOME", t.TempDir())
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	extra := root + "/extra.toml"
	if err := os.WriteFile(extra, []byte("[[plugin]]\nname = \"subagent\"\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Start(StartOptions{Flags: Flags{Workdir: root, Profile: extra, Yes: true, MaxSteps: -1},
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
	put("lead", "tools = [\"read_file\", \"glob\", \"write_file\", \"subagent\"]\n[use.permission]\nmode = \"read-only\"\n")
	built.Builder.Definitions = agentdef.Multi(rt.Rt.Defs, defs)
	ctx := context.Background()
	p, pcfg, err := built.Builder.Build("p", "lead", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OpenAs(ctx, pcfg, store.SessionMeta{ID: "p", AgentID: "lead"}); err != nil {
		t.Fatal(err)
	}

	c, ccfg, err := built.Builder.Build("p/sub-1", "subagent", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range c.Toolset.Available() {
		if n == "shell" || n == "edit" {
			t.Fatalf("父会话白名单外的 %s 不应对子会话可用：%v", n, c.Toolset.Available())
		}
	}
	if _, err := c.OpenAs(ctx, ccfg, store.SessionMeta{ID: "p/sub-1", ParentID: "p", Origin: "subagent", AgentID: "subagent"}); err != nil {
		t.Fatal(err)
	}
	run := c.Hub.ExecuteIn(ctx, c.Toolset, c.Ctx,
		[]session.ToolCall{{ID: "c1", Name: "write_file", Args: json.RawMessage(`{"path":"x.txt","content":"x"}`)}}, nil)
	res, _ := run.Next()
	out := ""
	for _, pt := range res.Output.Content {
		out += pt.Text
	}
	if res.Err != nil || !strings.Contains(out, "权限拒绝") {
		t.Fatalf("父会话上限 read-only 应管到子会话：err=%v out=%q", res.Err, out)
	}
}
