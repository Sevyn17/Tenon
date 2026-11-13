package group

// 组件生命周期用例（2026-11-13）：此前 group 的 Setup 里就取 DBHandle.DB()，
// 而 DB() 在 Start 之前返回 nil（存储由插件提供时，它要等自己的 Start 跑完才打开），
// 于是 group 的 Start 里 New(...) 拿到 nil db、报「缺少依赖（host / store / messages / reads）」。
// 走 apphost（存储来自产品的存储组件）时必然踩到，group 装了也用不了。
//
// 现有 group_test.go 的 rig() 直接调 New(...)，绕过了 Setup→Start，所以抓不到。
// 本文件用真实的 component.Assemble + Assembly.Start 覆盖那条路径。

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// lateStore 模拟产品的存储插件：Provide 句柄时存储还没打开（DB() 返回 nil），
// 自己的 Start 跑完才打开。这是存储插件的真实时序（Setup 里 Provide，Start 里打开）。
type lateStore struct {
	db    store.Store
	ready bool
}

func (s *lateStore) DB() store.Store {
	if s.ready {
		return s.db
	}
	return nil // 不能返回带类型的 nil 接口
}

func (s *lateStore) Blobs() toolhub.Artifacts { return nil }

func (s *lateStore) Meta() component.Meta {
	return component.Meta{Name: "store-fake", Provides: []string{assemble.ServiceDB}}
}

func (s *lateStore) Setup(cx *component.Registrar) error {
	return cx.Provide("db", assemble.DBHandle(s))
}

func (s *lateStore) Start(*component.Scope) error {
	s.ready = true // 存储在这里才打开
	return nil
}

// nopUI 不打印的界面。
type nopUI struct{}

func (nopUI) Print(string)        {}
func (nopUI) Warn(string, string) {}
func (nopUI) Ask(context.Context, string, []string) (string, error) {
	return "", nil
}

// TestGroupStartsWhenStoreOpensLate 存储句柄在 Setup 期不可用、Start 期才可用时，group 仍能启动。
//
// 回归用例：修复前 group 在 Setup 里就取 DB()（拿到 nil），Start 时 New 报「缺少依赖」。
func TestGroupStartsWhenStoreOpensLate(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	store1 := &lateStore{db: db}

	asm, err := component.Assemble(component.Spec{
		Plugins: []component.Entry{{Name: "store-fake"}, {Name: ServiceName}},
		Instances: map[string]component.Component{
			"store-fake": store1,
			ServiceName:  &plug{},
		},
		Inject: map[string]any{
			assemble.ServiceHost: &host.Host{DB: db},
			assemble.ServiceUI:   nopUI{},
		},
		Defaults: assemble.SlotDefaults(),
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	root := component.NewRoot("t")
	if err := asm.Start(root); err != nil {
		t.Fatalf("Start 失败（本次修复的 bug：Setup 里取 DB() 拿到 nil）：%v", err)
	}
	t.Cleanup(func() { _ = root.Close(ctx) })
}
