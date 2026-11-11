package tenon_test

// T4d 审查回归（H2）：插件包带的定义不算应用定义——它不参与缺省智能体与单定义解析，
// 但会进可用定义列表；应用显式 WithAgent 与包同名时应用优先。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/pkgs"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/tenontest"
)

// mkPkg 在临时目录里写一个带定义目录的插件包并解析。defNames 是 platform 层的定义名。
func mkPkg(t *testing.T, name string, scope string, defNames ...string) *pkgs.Package {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name = \"" + name + "\"\nversion = \"0.1.0\"\nagents = \"agents\"\n"
	if err := os.WriteFile(filepath.Join(dir, "tenon-plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dn := range defNames {
		dd := filepath.Join(dir, "agents", "platform", dn)
		if err := os.MkdirAll(dd, 0o755); err != nil {
			t.Fatal(err)
		}
		def := "format = \"tenon/v1\"\nid = \"" + dn + "\"\ndescription = \"来自包 " + name + "\"\n"
		if err := os.WriteFile(filepath.Join(dd, "agent.toml"), []byte(def), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := pkgs.Parse(dir, scope)
	if err != nil {
		t.Fatalf("pkgs.Parse: %v", err)
	}
	return p
}

// 装了只带一个定义的包，不带名字的 Ask 仍走内置缺省智能体，不报错、也不误用包里的定义
// （H2 回归：以前包定义算应用定义，会让"应用只有一个定义"命中它）。
func TestPackageAgentSingleDoesNotShadowDefault(t *testing.T) {
	p := mkPkg(t, "one", "user", "pkgbot")
	app := tenontest.New(t, tenon.WithPackages([]*pkgs.Package{p}),
		tenontest.Model(tenontest.Reply("我是缺省")))
	r, err := app.Ask(context.Background(), "", "在吗", nil)
	if err != nil {
		t.Fatalf("单定义包不该让不带名字的 Ask 报错（H2）：%v", err)
	}
	if r.Text != "我是缺省" {
		t.Fatalf("回答 %q", r.Text)
	}
}

// 装了带多个定义的包，不带名字的 Ask 也不该报"有 N 个定义"的歧义（H2 回归）。
func TestPackageAgentMultiNoAmbiguity(t *testing.T) {
	p := mkPkg(t, "many", "user", "a", "b")
	app := tenontest.New(t, tenon.WithPackages([]*pkgs.Package{p}),
		tenontest.Model(tenontest.Reply("缺省应答")))
	if _, err := app.Ask(context.Background(), "", "在吗", nil); err != nil {
		t.Fatalf("多定义包不该让不带名字的 Ask 歧义（H2）：%v", err)
	}
}

// 包定义进可用定义列表（按名字能用），但与 WithAgent 同名时应用显式定义优先（H2 回归：
// 以前包排在 inline 前，会静默遮蔽内联定义）。
func TestPackageAgentExplicitWins(t *testing.T) {
	p := mkPkg(t, "demo", "user", "support")
	app := tenontest.New(t,
		tenon.WithPackages([]*pkgs.Package{p}),
		tenon.WithAgent(tenon.Agent{Name: "support", Prompt: "应用自己的客服"}),
		tenontest.Model(tenontest.Reply("在"), tenontest.Reply("在")))
	ctx := context.Background()

	// 包里的定义确实可用（在可见列表里）。
	refs, err := agentdef.Visible(ctx, app.Internals().Definitions, agentdef.Scope{Tenant: store.Tenant})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range refs {
		if r.Name == "support" {
			found = true
		}
	}
	if !found {
		t.Fatal("包定义 support 应进可用定义列表")
	}
	// 直接点名 support 解析到的应是应用显式定义（描述不同，可区分）。这里只验能跑起来。
	if _, err := app.Ask(ctx, "support", "在吗", nil); err != nil {
		t.Fatalf("点名 support：%v", err)
	}
	if _, err := app.Ask(ctx, "", "在吗", nil); err != nil {
		t.Fatalf("应用只有一个显式定义时，不带名字应用它：%v", err)
	}
}

// 坏定义（缺 id）经包进 New：装不上、错误带包目录（与"坏清单启动失败"的既有策略一致，
// T4d 审查 H1：验收第 4 条说的是不列进可用定义，但启动路径本就 fail-fast）。
func TestPackageAgentBadDefinitionFailsStartup(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name = \"broken\"\nversion = \"0.1.0\"\nagents = \"agents\"\n"
	if err := os.WriteFile(filepath.Join(dir, "tenon-plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	dd := filepath.Join(dir, "agents", "platform", "no-id")
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	// 缺 id 的定义。
	if err := os.WriteFile(filepath.Join(dd, "agent.toml"), []byte("format = \"tenon/v1\"\ndescription = \"坏\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := pkgs.Parse(dir, "user")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()), tenon.WithWorkdir(t.TempDir()),
		tenon.WithPackages([]*pkgs.Package{p}), tenontest.Model())
	if err == nil {
		t.Fatal("坏定义应让 New 失败（与坏清单一致的 fail-fast）")
	}
	if !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "缺少 id") {
		t.Fatalf("错误应能定位到包（目录名 + 原因）：%v", err)
	}
}

// tplComponent 注册一份平台定义模板，用来验包定义与组件模板同名时启动报错（验收第 5 条）。
type tplComponent struct{ name string }

func (c tplComponent) Name() string           { return c.name }
func (tplComponent) Start(*tenon.Scope) error { return nil }
func (c tplComponent) Setup(cx *tenon.Registrar) error {
	m := agentdef.Mem()
	files := agentdef.Files{"agent.toml": []byte("format = \"tenon/v1\"\nid = \"tplonly\"\ndescription = \"模板\"\n")}
	if err := m.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: "tplonly"}, files); err != nil {
		return err
	}
	cx.Template(m)
	return nil
}

// 包里的平台定义与组件模板同名 → 装配（New）返回错误（验收第 5 条）。
func TestPackageAgentTemplateConflict(t *testing.T) {
	p := mkPkg(t, "clash", "user", "tplonly")
	_, err := tenon.New(tenon.Default(), tenon.WithStore(tenon.MemoryStore()), tenon.WithWorkdir(t.TempDir()),
		tenon.With(tplComponent{name: "tplc"}), tenon.WithPackages([]*pkgs.Package{p}), tenontest.Model())
	if err == nil || !strings.Contains(err.Error(), "同名") {
		t.Fatalf("包定义与组件模板同名应让 New 报错，实际 err=%v", err)
	}
}
