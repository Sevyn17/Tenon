package assemble

// H1 回归：component.WrapGuard 登记的判定必须真的接进会话的工具栈。
//
// 原来 WrapGuard 只写进 Assembly.guardsByPoint，没有任何生产代码调 EvalPoint，
// 于是 redact 的「拒写脱敏占位符」这条防护在真实会话里永不触发（测试只测了函数本身）。
// 这个用例走真实装配（Assemble → Wire → Build → Hub.Gates），验证 Gate 真的装上了。

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// pgRegisterOnce 幂等注册（照 contextmw_test.go 的既有模式）：
// component.Register 是全局注册，-count=N 重跑同一测试会撞「重复注册」panic（D1）。
var pgRegOnce sync.Map // name -> *sync.Once

func pgRegisterOnce(name string, f component.Factory) {
	onceAny, _ := pgRegOnce.LoadOrStore(name, &sync.Once{})
	onceAny.(*sync.Once).Do(func() { component.Register(name, f) })
}

// wrapGuardComp 一个登记了 ToolCall 判定的组件。
type wrapGuardComp struct{}

func (*wrapGuardComp) Meta() component.Meta { return component.Meta{Name: "wg-test"} }
func (*wrapGuardComp) Setup(cx *component.Registrar) error {
	component.WrapGuard(cx, component.ToolCall, "wg-rule",
		func(ctx context.Context, req component.ToolCallReq) (component.Verdict, string) {
			if strings.Contains(req.Tool, "forbidden") {
				return component.Deny, "禁止的工具"
			}
			return component.Abstain, ""
		})
	return nil
}
func (*wrapGuardComp) Start(*component.Scope) error { return nil }

// pointGuards 适配器在登记了判定时返回非 nil，且结论正确。
func TestPointGuardsAdapter(t *testing.T) {
	name := t.Name() + "/wg"
	pgRegisterOnce(name, func() component.Component { return &wrapGuardComp{} })
	asm, err := component.Assemble(component.Spec{Plugins: []component.Entry{{Name: name}}})
	if err != nil {
		t.Fatal(err)
	}
	if !asm.HasGuards(component.PointToolCallMw) {
		t.Fatal("判定应已登记")
	}
	pg := pointGuards(asm)
	if pg == nil {
		t.Fatal("pointGuards 适配器不该返回 nil（H1：登记了却没接线）")
	}
	if v, why := pg(context.Background(), component.ToolCallReq{Tool: "forbidden_tool"}); v != component.Deny {
		t.Fatalf("应拒绝，实际 %v %q", v, why)
	}
	if v, _ := pg(context.Background(), component.ToolCallReq{Tool: "ok_tool"}); v != component.Abstain {
		t.Fatalf("应不表态，实际 %v", v)
	}
}

// 没登记判定时适配器返回 nil（零开销）。
func TestPointGuardsNilWhenEmpty(t *testing.T) {
	asm, err := component.Assemble(component.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if pointGuards(asm) != nil {
		t.Fatal("没登记判定时应返回 nil")
	}
}

// Builder.PointGuards 装进 Hub 后，Gate 真的能拦（端到端走 Build）。
func TestPointGuardsReachHub(t *testing.T) {
	name := t.Name() + "/wg2"
	pgRegisterOnce(name, func() component.Component { return &wrapGuardComp{} })
	asm, err := component.Assemble(component.Spec{Plugins: []component.Entry{{Name: name}}})
	if err != nil {
		t.Fatal(err)
	}
	b := &Builder{DB: memstore.New(), Inits: []InitFunc{permInit()}, PointGuards: pointGuards(asm)}
	a, _, err := b.Build("s1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range a.Hub.Gates() {
		if strings.Contains(g.Name, "guardpoint") {
			found = true
		}
	}
	if !found {
		names := make([]string, 0)
		for _, g := range a.Hub.Gates() {
			names = append(names, g.Name)
		}
		t.Fatalf("判定区没装进 Hub，实际 Gate: %v", names)
	}
}
