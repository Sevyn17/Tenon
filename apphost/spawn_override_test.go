package apphost

// 委派参数覆盖（Mortise docs/62 K5、docs/56 第 3 节）：mode、tools 只能收窄，更宽的按主会话水平并说明；
// system_prompt 接在系统块最后；不传 mode 时子会话跟随主会话档位。

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

func startWorkspace(t *testing.T) *Runtime {
	t.Helper()
	root := t.TempDir()
	t.Setenv("TENON_HOME", t.TempDir())
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	extra := root + "/extra.toml"
	if err := os.WriteFile(extra, []byte("[[plugin]]\nname = \"session-start\"\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Start(StartOptions{Flags: Flags{Workdir: root, Profile: extra, Mode: "workspace", MaxSteps: -1},
		App: AppOptions{MaxSteps: -1, ModesWorkdir: root}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return rt
}

func TestSpawnOverrideNarrows(t *testing.T) {
	rt := startWorkspace(t)
	built, err := rt.BuildApp()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, pcfg, err := built.Builder.Build("p", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OpenAs(ctx, pcfg, store.SessionMeta{ID: "p"}); err != nil {
		t.Fatal(err)
	}
	run := func(id string, o host.SpawnOptions, tool, args string) (avail []string, sys []session.SystemBlock, notes []string, out string) {
		t.Helper()
		o.OnNarrow = func(n string) { notes = append(notes, n) }
		c, ccfg, err := built.Builder.BuildWith(id, "subagent", true, &o)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.OpenAs(ctx, ccfg, store.SessionMeta{ID: id, ParentID: "p", Origin: "subagent", AgentID: "subagent"}); err != nil {
			t.Fatal(err)
		}
		if tool != "" {
			r := c.Hub.ExecuteIn(ctx, c.Toolset, c.Ctx, []session.ToolCall{{ID: "c1", Name: tool, Args: json.RawMessage(args)}}, nil)
			res, _ := r.Next()
			for _, pt := range res.Output.Content {
				out += pt.Text
			}
		}
		return c.Toolset.Available(), ccfg.System, notes, out
	}

	// tools：主会话没有的去掉并说明；子会话没有委派工具，也加不进。
	avail, _, notes, _ := run("p/sub-1", host.SpawnOptions{Tools: []string{"read_file", "session_start", "nope_*"}}, "", "")
	for _, n := range avail {
		if n != "read_file" && n != "read_artifact" {
			t.Fatalf("tools 应收窄为 read_file：%v", avail)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "tools 已收窄为 read_file") || !strings.Contains(notes[0], "session_start") {
		t.Fatalf("tools 收窄说明：%v", notes)
	}

	// mode 更严：read-only 上限管到子会话。
	_, _, notes, out := run("p/sub-2", host.SpawnOptions{Mode: "read-only"}, "write_file", `{"path":"x.txt","content":"x"}`)
	if !strings.Contains(out, "权限拒绝") || len(notes) != 0 {
		t.Fatalf("mode read-only 应拒写：out=%q notes=%v", out, notes)
	}

	// mode 更宽：按主会话 workspace，说明已收窄；写文件仍按 workspace 放行。
	_, _, notes, out = run("p/sub-3", host.SpawnOptions{Mode: "full"}, "write_file", `{"path":"y.txt","content":"y"}`)
	if len(notes) != 1 || !strings.Contains(notes[0], "mode 已收窄为 workspace") {
		t.Fatalf("mode 收窄说明：%v", notes)
	}
	if strings.Contains(out, "权限拒绝") {
		t.Fatalf("workspace 下写文件应放行：%q", out)
	}

	// system_prompt 接在最后。
	_, sys, _, _ := run("p/sub-4", host.SpawnOptions{SystemPrompt: "你是代码审查员。"}, "", "")
	if len(sys) < 2 || sys[len(sys)-1].Text != "你是代码审查员。" || sys[len(sys)-1].Source != "spawn" {
		t.Fatalf("system_prompt 应接在系统块最后：%+v", sys)
	}
}
