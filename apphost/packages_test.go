package apphost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/store"
)

// agentTOML 一份最小定义。id 须与目录名相同。
func agentTOML(id string) string {
	return "format = \"tenon/v1\"\nid = \"" + id + "\"\ndescription = \"测试定义\"\n"
}

// writePkg 在 plugins 根下写一个带定义的插件包。
func writePkg(t *testing.T, pluginsRoot, name, defName string) {
	t.Helper()
	defDir := filepath.Join(pluginsRoot, name, "agents", "platform", defName)
	if err := os.MkdirAll(defDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name = \"" + name + "\"\nversion = \"0.1.0\"\nagents = \"agents\"\n"
	if err := os.WriteFile(filepath.Join(pluginsRoot, name, "tenon-plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defDir, "agent.toml"), []byte(agentTOML(defName)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPackageAgents 插件包带的定义进可用定义列表，explain 能看到；项目未受信时项目级包的定义不加载。
func TestPackageAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	writePkg(t, filepath.Join(home, "plugins"), "user-pkg", "from-user")

	root := t.TempDir()
	writePkg(t, filepath.Join(root, ".tenon", "plugins"), "proj-pkg", "from-project")

	start := func(trusted bool) *Runtime {
		t.Helper()
		rt, err := Start(StartOptions{Flags: Flags{Workdir: root, Yes: true, TrustProject: trusted, MaxSteps: -1},
			App: AppOptions{MaxSteps: -1, ModesWorkdir: root}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(rt.Close)
		return rt
	}
	has := func(rt *Runtime, name string) bool {
		t.Helper()
		_, _, err := agentdef.Resolve(context.Background(), rt.Rt.Defs, agentdef.Scope{Tenant: store.Tenant}, name)
		return err == nil
	}

	untrusted := start(false)
	if !has(untrusted, "from-user") {
		t.Fatal("用户级包的定义应可用")
	}
	if has(untrusted, "from-project") {
		t.Fatal("项目未受信时，项目级包的定义不应加载")
	}

	trusted := start(true)
	if !has(trusted, "from-user") || !has(trusted, "from-project") {
		t.Fatal("受信后两个包的定义都应可用")
	}
	var b strings.Builder
	trusted.Rt.Asm.Explain(&b, nil)
	out := b.String()
	for _, want := range []string{"插件包", "user-pkg", "proj-pkg", "platform:from-user", "platform:from-project"} {
		if !strings.Contains(out, want) {
			t.Fatalf("explain 缺 %q：\n%s", want, out)
		}
	}
}
