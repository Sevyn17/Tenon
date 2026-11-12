package component

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// registerOnce 幂等注册：同名已注册时跳过。
//
// `go test -count=2` 会重跑同一测试函数，t.Name() 不变 → 原先直接 Register 会撞
// 「重名 panic」。Register 的 panic 是有意的安全检查（init 里重名属编程错误），
// 所以这里在测试侧做幂等，而不是放宽 Register。
func registerOnce(name string, f Factory) {
	regMu.Lock()
	_, dup := registry[name]
	regMu.Unlock()
	if !dup {
		Register(name, f)
	}
}

// 测试注册表：名字 → 当次要用的 fake 实例。
//
// 为什么不把 *fake 直接塞进 Register 的闭包：`go test -count=2` 会重跑同一测试函数，
// 但 t.Name() 不变，所以注册名相同——而每次重跑产生的 *fake 是新对象（闭包捕获的
// 局部变量如 order 也是新的）。若工厂闭包固定返回第一遍的 *fake，第二遍的断言就会
// 看到第一遍的残留状态（D1 暴露的第二个问题：middleware_test 等一批用例失败）。
//
// 所以：工厂闭包只查表，每次 Setup 时用「当前」的 fake。
var (
	testFakes   sync.Map // full name -> *fake
	testRegOnce sync.Map // full name -> *sync.Once
)

// reg 用唯一前缀注册，避免测试间同名冲突；-count=N 重跑安全。
func reg(t *testing.T, name string, f *fake) string {
	t.Helper()
	full := t.Name() + "/" + name
	f.meta.Name = full
	testFakes.Store(full, f) // 每次重跑都更新成当次实例
	onceAny, _ := testRegOnce.LoadOrStore(full, &sync.Once{})
	onceAny.(*sync.Once).Do(func() {
		Register(full, func() Component {
			v, _ := testFakes.Load(full)
			ff, _ := v.(*fake)
			return ff
		})
	})
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

type compactCfg struct {
	Threshold float64 `cfg:"threshold" default:"0.85" title:"触发阈值" group:"压缩" widget:"slider"`
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
	if len(sch) != 4 || sch[0].Widget != "slider" {
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
			cx.Register("session_layer", "compact-layer", nil)
			return nil
		}})
	a, err := Assemble(Spec{Plugins: entries(n), Defaults: map[string]any{"slot:Secrets": "env"}})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	a.SetPackages([]PackageInfo{{Name: "demo", Scope: "user", File: "demo/tenon-plugin.toml", Contents: "定义", Agents: []string{"platform:helper"}}})
	a.Explain(&b, map[string]string{n: "base.toml"})
	out := b.String()
	for _, want := range []string{"compact", "base.toml", "threshold", "default", "tool", "read", "session_layer", "compact-layer", "slot:Secrets", "插件包", "demo", "platform:helper"} {
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
