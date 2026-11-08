package component

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agentdef"
)

// defSource 造一个内存定义来源（平台命名空间，minimal manifest）。
func defSource(t *testing.T, names ...string) agentdef.Source {
	t.Helper()
	m := agentdef.Mem()
	for _, name := range names {
		files := agentdef.Files{"agent.toml": []byte(
			"format = \"tenon/v1\"\nid = \"" + name + "\"\ndescription = \"测试定义\"\n")}
		if err := m.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: name}, files); err != nil {
			t.Fatalf("Put %s：%v", name, err)
		}
	}
	return m
}

func TestRegistrarTemplate(t *testing.T) {
	src := defSource(t, "x")
	p := reg(t, "tpl", &fake{setup: func(cx *Registrar) error {
		cx.Template(src)
		return nil
	}})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatalf("Assemble：%v", err)
	}
	ts := asm.Templates()
	if len(ts) != 1 {
		t.Fatalf("Templates = %d 份，期望 1", len(ts))
	}
	if ts[0].Owner != p {
		t.Fatalf("Owner = %q，期望 %q", ts[0].Owner, p)
	}
	defs, err := asm.Definitions(nil)
	if err != nil {
		t.Fatalf("Definitions：%v", err)
	}
	a, ref, err := agentdef.Resolve(context.Background(), defs, agentdef.Scope{}, "x")
	if err != nil {
		t.Fatalf("Resolve 模板：%v", err)
	}
	if a.ID != "x" || ref.Namespace != agentdef.Platform || ref.Name != "x" {
		t.Fatalf("Resolve = %q %s", a.ID, ref)
	}
}

func TestAssemblyDefinitionsApp(t *testing.T) {
	src := defSource(t, "x")
	p := reg(t, "tpl", &fake{setup: func(cx *Registrar) error {
		cx.Template(src)
		return nil
	}})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatalf("Assemble：%v", err)
	}
	// 应用自带平台定义与模板合并，互不冲突。
	app := defSource(t, "app")
	defs, err := asm.Definitions(app)
	if err != nil {
		t.Fatalf("Definitions(app)：%v", err)
	}
	refs, err := defs.List(context.Background(), agentdef.Scope{})
	if err != nil {
		t.Fatalf("List：%v", err)
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	if got := strings.Join(names, ","); got != "app,x" {
		t.Fatalf("平台定义 = %s", got)
	}
	// 应用平台定义与模板同名 → 装配失败（03 3.5 启动报错）。
	dup := defSource(t, "x")
	if _, err := asm.Definitions(dup); err == nil || !strings.Contains(err.Error(), "同名") {
		t.Fatalf("同名应报错，实际 err=%v", err)
	}
}
