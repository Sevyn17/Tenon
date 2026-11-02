package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fake 测试用插件：按 Meta 声明，Setup 时执行 setup。
type fake struct {
	meta  Meta
	setup func(*Registrar) error
	start func(*Scope) error
}

func (f *fake) Meta() Meta { return f.meta }
func (f *fake) Setup(cx *Registrar) error {
	if f.setup != nil {
		return f.setup(cx)
	}
	return nil
}
func (f *fake) Start(sc *Scope) error {
	if f.start != nil {
		return f.start(sc)
	}
	return nil
}

// reg 用唯一前缀注册，避免测试间同名冲突。
func reg(t *testing.T, name string, f *fake) string {
	t.Helper()
	full := t.Name() + "/" + name
	f.meta.Name = full
	Register(full, func() Plugin { return f })
	return full
}

func entries(names ...string) []Entry {
	out := make([]Entry, len(names))
	for i, n := range names {
		out[i] = Entry{Name: n}
	}
	return out
}

func TestAssembleTopoOrderAndUse(t *testing.T) {
	var got string
	store := reg(t, "store", &fake{meta: Meta{Provides: []string{"slot:SessionStore"}},
		setup: func(cx *Registrar) error { return cx.FillSlot("SessionStore", "db") }})
	user := reg(t, "user", &fake{meta: Meta{Requires: []string{"slot:SessionStore", "slot:Secrets"}},
		setup: func(cx *Registrar) error {
			s, err := Use[string](cx, "slot:SessionStore")
			got = s
			if err != nil {
				return err
			}
			if _, err := Use[string](cx, "service:undeclared"); err == nil {
				return errors.New("未声明的依赖不应可取")
			}
			return nil
		}})
	a, err := Assemble(Spec{Plugins: entries(user, store), Defaults: map[string]any{"slot:Secrets": "env"}})
	if err != nil {
		t.Fatal(err)
	}
	if o := a.Order(); o[0] != store || o[1] != user || got != "db" {
		t.Fatalf("order=%v got=%q", o, got)
	}
	if v, _ := a.Value("slot:Secrets"); v != "env" {
		t.Fatalf("插槽默认实现未生效: %v", v)
	}
}

func TestAssembleSelfChecks(t *testing.T) {
	a := reg(t, "a", &fake{meta: Meta{Requires: []string{"service:b"}, Provides: []string{"service:a"}},
		setup: func(cx *Registrar) error { return cx.Provide("a", 1) }})
	b := reg(t, "b", &fake{meta: Meta{Requires: []string{"service:a"}, Provides: []string{"service:b"}},
		setup: func(cx *Registrar) error { return cx.Provide("b", 1) }})
	s1 := reg(t, "s1", &fake{meta: Meta{Provides: []string{"slot:X"}}})
	s2 := reg(t, "s2", &fake{meta: Meta{Provides: []string{"slot:X"}}})
	lone := reg(t, "lone", &fake{meta: Meta{Requires: []string{"service:none"}}})
	cases := []struct {
		spec Spec
		want error
	}{
		{Spec{Plugins: entries(a, b)}, ErrCycle},
		{Spec{Plugins: entries(s1, s2)}, ErrDupSlot},
		{Spec{Plugins: entries(lone)}, ErrMissingDep},
		{Spec{Plugins: entries(s1), Mandatory: []string{"perm"}}, ErrMandatory},
		{Spec{Plugins: entries("nobody")}, ErrUnknown},
	}
	for i, c := range cases {
		if _, err := Assemble(c.spec); !errors.Is(err, c.want) {
			t.Fatalf("case %d: err=%v want %v", i, err, c.want)
		}
	}
}

func TestLayerOrderingAndGuardRules(t *testing.T) {
	mk := func(name string, specs ...LayerSpec) string {
		return reg(t, name, &fake{setup: func(cx *Registrar) error {
			for _, s := range specs {
				if err := cx.AddLayer(s, nil); err != nil {
					return err
				}
			}
			return nil
		}})
	}
	compact := mk("compact", LayerSpec{Point: "model", Position: Transform, Outermost: true})
	route := mk("route", LayerSpec{Point: "model", Position: Transform, Order: 1})
	redact := mk("redact", LayerSpec{Point: "model", Position: Transform})
	perm := mk("perm", LayerSpec{Point: "model", Position: Guard, Failure: FailClosed})
	check := mk("check", LayerSpec{Point: "model", Position: Guard, Failure: FailClosed})
	a, err := Assemble(Spec{Plugins: entries(perm, redact, route, compact, check), Mandatory: []string{perm, redact}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range a.Layers("model") {
		got = append(got, strings.TrimPrefix(l.Plugin, t.Name()+"/"))
	}
	// 外→内：Outermost Transform；可选 Transform（route）；强制 Transform（redact）；可选 Guard；强制 Guard 最内。
	if strings.Join(got, ",") != "compact,route,redact,check,perm" {
		t.Fatalf("层序 %v", got)
	}
	bad := mk("bad", LayerSpec{Point: "model", Position: Guard, Failure: FailOpen})
	if _, err := Assemble(Spec{Plugins: entries(bad)}); err == nil {
		t.Fatal("Guard 不能 FailOpen")
	}
	c2 := mk("c2", LayerSpec{Point: "model", Position: Transform, Outermost: true})
	if _, err := Assemble(Spec{Plugins: entries(compact, c2)}); err == nil {
		t.Fatal("两个 Outermost 应失败")
	}
}

type compactCfg struct {
	Threshold float64 `cfg:"threshold" default:"0.85" title:"触发阈值" group:"压缩" widget:"slider" override:"agent"`
	Keep      int     `cfg:"keep_recent_turns" default:"2"`
	Name      string  `cfg:"summary_alias" default:"cheap"`
	On        bool    `cfg:"enabled" default:"true"`
}

func TestConfigDecodeAndSchema(t *testing.T) {
	var seen *compactCfg
	n := reg(t, "compact", &fake{meta: Meta{Config: &compactCfg{}}, setup: func(cx *Registrar) error {
		seen = cx.Config().(*compactCfg)
		return nil
	}})
	a, err := Assemble(Spec{Plugins: []Entry{{Name: n, Config: map[string]any{"keep_recent_turns": int64(3)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if seen.Threshold != 0.85 || seen.Keep != 3 || seen.Name != "cheap" || !seen.On {
		t.Fatalf("配置 %+v", seen)
	}
	if a.sources[n]["keep_recent_turns"] != "profile" || a.sources[n]["threshold"] != "default" {
		t.Fatalf("来源 %v", a.sources[n])
	}
	sch := SchemaOf(&compactCfg{})
	if len(sch) != 4 || sch[0].Widget != "slider" || !sch[0].Override || sch[1].Override {
		t.Fatalf("schema %+v", sch)
	}
	if _, err := Assemble(Spec{Plugins: []Entry{{Name: n, Config: map[string]any{"typo": 1}}}}); err == nil {
		t.Fatal("未知配置键应报错")
	}
}

func TestProfileIncludeMandatoryUnion(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("base.toml", `
mandatory = ["permission"]
[[plugin]]
name = "permission"
[[plugin]]
name = "compact"
config = { threshold = 0.8 }
`)
	child := write("child.toml", `
include = ["base.toml"]
mandatory = ["redact"]
[[plugin]]
name = "redact"
[[plugin]]
name = "compact"
config = { threshold = 0.9 }
`)
	p, err := LoadProfile(child)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Mandatory, ",") != "permission,redact" {
		t.Fatalf("mandatory 应取并集: %v", p.Mandatory)
	}
	if len(p.Plugins) != 3 || p.Plugins[1].Config["threshold"] != 0.9 {
		t.Fatalf("后写覆盖整行: %+v", p.Plugins)
	}
	if !strings.HasSuffix(p.Sources["compact"], "child.toml") || !strings.HasSuffix(p.Sources["permission"], "base.toml") {
		t.Fatalf("来源 %v", p.Sources)
	}
	bad := write("bad.toml", `
include = ["base.toml"]
[[plugin]]
name = "permission"
disabled = true
`)
	if _, err := LoadProfile(bad); err == nil {
		t.Fatal("子 profile 不能停用父 profile 的强制插件")
	}
	loop := write("loop.toml", `include = ["loop.toml"]`)
	if _, err := LoadProfile(loop); err == nil {
		t.Fatal("include 循环应报错")
	}
}

func TestStartFailureClosesStarted(t *testing.T) {
	closed := 0
	ok := reg(t, "ok", &fake{start: func(sc *Scope) error {
		sc.OnClose(func(context.Context) error { closed++; return nil })
		return nil
	}})
	boom := reg(t, "zboom", &fake{start: func(*Scope) error { return errors.New("boom") }})
	a, err := Assemble(Spec{Plugins: entries(ok, boom)})
	if err != nil {
		t.Fatal(err)
	}
	root := NewRoot("proc")
	if err := a.Start(root); err == nil || closed != 1 {
		t.Fatalf("Start 失败应回收已启动插件: err=%v closed=%d", err, closed)
	}
}

func TestExplain(t *testing.T) {
	n := reg(t, "compact", &fake{meta: Meta{Version: "1", Config: &compactCfg{}},
		setup: func(cx *Registrar) error {
			cx.Register("tool", "read", nil)
			return cx.AddLayer(LayerSpec{Point: "model", Position: Transform, Outermost: true}, nil)
		}})
	a, err := Assemble(Spec{Plugins: entries(n), Defaults: map[string]any{"slot:Secrets": "env"}})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	a.Explain(&b, map[string]string{n: "base.toml"})
	out := b.String()
	for _, want := range []string{"compact", "base.toml", "threshold", "default", "tool", "read", "model", "Transform", "slot:Secrets"} {
		if !strings.Contains(out, want) {
			t.Fatalf("explain 缺 %q:\n%s", want, out)
		}
	}
}

func TestInjectAndOptional(t *testing.T) {
	var got any
	var gotOpt bool
	user := reg(t, "user", &fake{meta: Meta{Requires: []string{"service:ui", "?service:sandbox"}},
		setup: func(cx *Registrar) error {
			v, err := Use[string](cx, "service:ui")
			if err != nil {
				return err
			}
			got = v
			_, ok, err := UseOptional[string](cx, "service:sandbox")
			gotOpt = ok
			return err
		}})
	if _, err := Assemble(Spec{Plugins: []Entry{{Name: user}}, Inject: map[string]any{"service:ui": "tty"}}); err != nil {
		t.Fatal(err)
	}
	if got != "tty" || gotOpt {
		t.Fatalf("got %v opt %v", got, gotOpt)
	}
	// 缺必选依赖仍报错
	if _, err := Assemble(Spec{Plugins: []Entry{{Name: user}}}); !errors.Is(err, ErrMissingDep) {
		t.Fatalf("want missing dep, got %v", err)
	}
	// 可选依赖有提供者：排在其后并能取到
	sb := reg(t, "sb", &fake{meta: Meta{Provides: []string{"service:sandbox"}},
		setup: func(cx *Registrar) error { return cx.Provide("sandbox", "box") }})
	a, err := Assemble(Spec{Plugins: []Entry{{Name: user}, {Name: sb}}, Inject: map[string]any{"service:ui": "tty"}})
	if err != nil {
		t.Fatal(err)
	}
	if !gotOpt {
		t.Fatal("optional dep not seen")
	}
	if o := a.Order(); o[0] != sb {
		t.Fatalf("order %v", o)
	}
	// 插件不能提供应用预置的服务
	dup := reg(t, "dup", &fake{meta: Meta{Provides: []string{"service:ui"}},
		setup: func(cx *Registrar) error { return cx.Provide("ui", "x") }})
	if _, err := Assemble(Spec{Plugins: []Entry{{Name: dup}}, Inject: map[string]any{"service:ui": "tty"}}); err == nil {
		t.Fatal("want dup error")
	}
}
