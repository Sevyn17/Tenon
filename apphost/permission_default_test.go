package apphost

// Mortise docs/75 P1：定义的 [use.permission] default 作新会话缺省档位。
// Open（缺省定义）与 Create（session.create 指定定义）两条路径都应用；没写 default 回落用户全局缺省；
// default 宽于同段 mode 上限时建会话报错；mode.set 照常可改；子会话跟随顶层会话。

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/modules/security/permission"
	"github.com/Sevyn17/tenon/runtime/agentdef"
)

func TestDefinitionDefaultMode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TENON_HOME", t.TempDir())
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	defs := agentdef.Mem()
	put := func(name, body string) {
		t.Helper()
		if err := defs.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: name},
			agentdef.Files{agentdef.ManifestName: []byte("format = \"tenon/v1\"\nid = \"" + name + "\"\ndescription = \"t\"\n" + body)}); err != nil {
			t.Fatal(err)
		}
	}
	put("ro", "[use.permission]\ndefault = \"read-only\"\n")
	put("plain", "")
	put("bad", "[use.permission]\nmode = \"read-only\"\ndefault = \"workspace\"\n")
	put("unknown", "[use.permission]\ndefault = \"nope\"\n")
	rt, err := Start(StartOptions{Flags: Flags{Workdir: root, MaxSteps: -1},
		App: AppOptions{MaxSteps: -1, ModesWorkdir: root, DefaultAgent: "ro", Agents: []agentdef.Source{defs}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	built, err := rt.BuildApp()
	if err != nil {
		t.Fatal(err)
	}
	if built.Perms == nil {
		t.Fatal("没有 permission 服务")
	}
	ctx := context.Background()
	global := built.Perms.DefaultMode()
	if global == permission.ReadOnly {
		t.Fatalf("测试前提：用户全局缺省不应是 read-only（%s）", global)
	}

	// 1. Open 路径：缺省定义 ro → read-only。
	if _, err := rt.Host.Open(ctx, "s-open"); err != nil {
		t.Fatal(err)
	}
	if m := built.Perms.Mode("s-open"); m != permission.ReadOnly {
		t.Fatalf("Open 路径应用定义 default：%s", m)
	}
	// 子会话跟随顶层会话。
	if m := built.Perms.Mode("s-open/sub-1"); m != permission.ReadOnly {
		t.Fatalf("子会话应跟随顶层：%s", m)
	}

	// 2. Create 路径（session.create）：指定 ro → read-only；plain 没写 → 用户全局缺省。
	if _, err := rt.Host.Create(ctx, "s-create", "ro", nil); err != nil {
		t.Fatal(err)
	}
	if m := built.Perms.Mode("s-create"); m != permission.ReadOnly {
		t.Fatalf("Create 路径应用定义 default：%s", m)
	}
	if _, err := rt.Host.Create(ctx, "s-plain", "plain", nil); err != nil {
		t.Fatal(err)
	}
	if m := built.Perms.Mode("s-plain"); m != global {
		t.Fatalf("没写 default 应回落用户全局缺省 %s：%s", global, m)
	}

	// 3. mode.set 照常可改（default 只作缺省）。
	if err := built.Perms.SetMode("s-create", permission.Ask); err != nil || built.Perms.Mode("s-create") != permission.Ask {
		t.Fatalf("SetMode 应生效：%v %s", err, built.Perms.Mode("s-create"))
	}

	// 4. default 宽于上限、档位不存在：建会话报错。
	for _, def := range []string{"bad", "unknown"} {
		_, err := rt.Host.Create(ctx, "s-"+def, def, nil)
		if err == nil || !strings.Contains(err.Error(), "[use.permission] default") {
			t.Fatalf("%s 应被拒：%v", def, err)
		}
	}
}
