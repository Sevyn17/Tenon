package apphost

// F1（Mortise docs/63）：权限上限不能放宽。会话行冻结的 use_config 与定义当前的 [use.permission]
// 各判一层、取更严：定义在会话建好后收紧，老会话重开立即收紧；定义放宽，老会话保持原上限。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

func TestPermissionCapNeverLoosens(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TENON_HOME", t.TempDir())
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	rt, err := Start(StartOptions{Flags: Flags{Workdir: root, Yes: true, MaxSteps: -1}, // 会话档位 full
		App: AppOptions{MaxSteps: -1, ModesWorkdir: root}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	built, err := rt.BuildApp()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defs := agentdef.Mem()
	put := func(name, body string) {
		t.Helper()
		if err := defs.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: name},
			agentdef.Files{agentdef.ManifestName: []byte("format = \"tenon/v1\"\nid = \"" + name + "\"\ndescription = \"t\"\n" + body)}); err != nil {
			t.Fatal(err)
		}
	}
	built.Builder.Definitions = agentdef.Multi(rt.Rt.Defs, defs)

	// open 构造会话（不存在则建，存在则按会话行恢复），返回写文件的结果文本。
	// 每次调用用不同的 call id：同会话同 call id 会命中工具结果的幂等记录，不会重新判定。
	n := 0
	write := func(id, def string) string {
		t.Helper()
		n++
		a, cfg, err := built.Builder.Build(id, def, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.OpenAs(ctx, cfg, store.SessionMeta{ID: id, AgentID: def}); err != nil {
			t.Fatal(err)
		}
		run := a.Hub.ExecuteIn(ctx, a.Toolset, a.Ctx,
			[]session.ToolCall{{ID: fmt.Sprintf("c%d", n), Name: "write_file", Args: json.RawMessage(`{"path":"` + id + `.txt","content":"x"}`)}}, nil)
		res, _ := run.Next()
		out := ""
		for _, p := range res.Output.Content {
			out += p.Text
		}
		return out
	}
	denied := func(out string) bool { return strings.Contains(out, "智能体权限上限") }

	// 1. 收紧：建会话时定义不设上限，之后改成 read-only；重开老会话应被拒。
	put("tighten", "")
	if out := write("s-tighten", "tighten"); denied(out) {
		t.Fatalf("未设上限时不应拒：%q", out)
	}
	put("tighten", "[use.permission]\nmode = \"read-only\"\n")
	if out := write("s-tighten", "tighten"); !denied(out) {
		t.Fatalf("定义收紧后老会话应立即收紧：%q", out)
	}

	// 2. 放宽：建会话时 read-only，之后去掉上限；老会话仍按冻结的 read-only 判。
	put("loosen", "[use.permission]\nmode = \"read-only\"\n")
	if out := write("s-loosen", "loosen"); !denied(out) {
		t.Fatalf("read-only 上限应拒：%q", out)
	}
	put("loosen", "")
	if out := write("s-loosen", "loosen"); !denied(out) {
		t.Fatalf("定义放宽后老会话不应放宽：%q", out)
	}
	// 新会话按新定义。
	if out := write("s-loosen-new", "loosen"); denied(out) {
		t.Fatalf("新会话应按放宽后的定义：%q", out)
	}
}
