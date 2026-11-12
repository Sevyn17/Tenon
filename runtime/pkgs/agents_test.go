package pkgs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agentdef"
)

// agentTOML 一份最小定义。id 须与目录名相同。
func agentTOML(id string) string {
	return "format = \"tenon/v1\"\nid = \"" + id + "\"\ndescription = \"测试定义\"\n"
}

// TestPackageAgents 包的 agents 目录按 agentdef.Dir 布局带定义：
// 解析后能取到，与直接用 agentdef.Dir 读同一目录的版本相同；坏定义的错误带包目录。
func TestPackageAgents(t *testing.T) {
	root := t.TempDir()
	dir := mk(t, root, "demo", "name = \"demo\"\nagents = \"agents\"", "agents/")
	defDir := filepath.Join(dir, "agents", "platform", "helper")
	if err := os.MkdirAll(defDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defDir, "agent.toml"), []byte(agentTOML("helper")), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Parse(dir, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if p.AgentsDir != filepath.Join(dir, "agents") {
		t.Fatalf("AgentsDir = %s", p.AgentsDir)
	}
	if !strings.Contains(p.Contents(), "定义") {
		t.Fatalf("Contents 应提到定义：%s", p.Contents())
	}

	srcs := AgentSources([]*Package{p})
	if len(srcs) != 1 {
		t.Fatalf("应有一个定义来源：%d", len(srcs))
	}
	ctx := context.Background()
	scope := agentdef.Scope{Tenant: "local"}
	got, ref, err := agentdef.Resolve(ctx, srcs[0], scope, "helper")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Namespace != agentdef.Platform || got.ID != "helper" {
		t.Fatalf("解析结果 = %s %s", ref, got.ID)
	}
	direct, err := agentdef.FS(os.DirFS(p.AgentsDir), p.AgentsDir).Load(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != direct.Version {
		t.Fatalf("版本应与直接读目录相同：%s != %s", got.Version, direct.Version)
	}

	infos := Infos(ctx, []*Package{p}, scope)
	if len(infos) != 1 || len(infos[0].Agents) != 1 || infos[0].Agents[0] != "platform:helper" {
		t.Fatalf("摘要 = %+v", infos)
	}
}

// TestPackageAgentsBadDefinition 定义缺必填字段时，错误带包目录，且该定义不进来源。
func TestPackageAgentsBadDefinition(t *testing.T) {
	root := t.TempDir()
	dir := mk(t, root, "demo", "name = \"demo\"\nagents = \"agents\"", "agents/")
	defDir := filepath.Join(dir, "agents", "platform", "broken")
	if err := os.MkdirAll(defDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 缺 id：目录存在，Parse 不拦（它不解析定义），Load 时报错。
	if err := os.WriteFile(filepath.Join(defDir, "agent.toml"), []byte("description = \"缺 id\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Parse(dir, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = agentdef.Resolve(context.Background(), AgentSources([]*Package{p})[0], agentdef.Scope{}, "broken")
	if err == nil || !strings.Contains(err.Error(), filepath.ToSlash(dir)) {
		t.Fatalf("坏定义的错误应带包目录：%v", err)
	}
}

// TestPackageAgentsRejectsBadDir agents 必须是包目录内存在的相对路径。
func TestPackageAgentsRejectsBadDir(t *testing.T) {
	cases := map[string]struct{ manifest, want string }{
		"escape":  {"name = \"escape\"\nagents = \"../x\"", "跳出了包目录"},
		"missing": {"name = \"missing\"\nagents = \"no-such\"", "不存在"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := mk(t, t.TempDir(), name, c.manifest)
			_, err := Parse(dir, ScopeUser)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%v，想要含 %q", err, c.want)
			}
		})
	}
}

// TestAgentSourcesUserFirst 同名定义用户级包优先于项目级包（与包本身的覆盖方向相反）。
func TestAgentSourcesUserFirst(t *testing.T) {
	mkPkg := func(version string) *Package {
		root := t.TempDir()
		dir := mk(t, root, "demo", "name = \"demo\"\nagents = \"agents\"", "agents/")
		defDir := filepath.Join(dir, "agents", "platform", "helper")
		if err := os.MkdirAll(defDir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "format = \"tenon/v1\"\nid = \"helper\"\ndescription = \"" + version + "\"\n"
		if err := os.WriteFile(filepath.Join(defDir, "agent.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Parse(dir, ScopeUser)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	user, proj := mkPkg("用户级"), mkPkg("项目级")
	proj.Scope = ScopeProject
	src := agentdef.Multi(AgentSources([]*Package{user, proj})...)
	got, _, err := agentdef.Resolve(context.Background(), src, agentdef.Scope{}, "helper")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "用户级" {
		t.Fatalf("同名定义应取用户级，得 %q", got.Description)
	}
}
