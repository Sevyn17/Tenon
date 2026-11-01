// Package plugin：插件管理器（plugin-manager 定稿 §5）。
//
// 插件编译期注册（Register），profile 选用；装配期按依赖拓扑排序逐个 Setup，
// Setup 只做注册（插槽、服务、层、钩子……），资源一律在 Start 里建并挂到作用域上。
// 装配结束后注册表只读。
package plugin

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// TrustLevel 受信等级（内置恒为 Trusted；为外部插件预留，02 §3）。
type TrustLevel int

const (
	Trusted TrustLevel = iota
	External
)

// Meta 插件元数据。Requires / Provides 用 "slot:<名>" / "service:<名>"。
type Meta struct {
	Name     string
	Version  string
	Requires []string
	Provides []string
	Trust    TrustLevel
	// Config：配置结构体的零值指针（含 struct tag），用于 Schema 与解码；nil = 无配置。
	Config any
	// Default：profile 未写 enabled 时是否开启（plugin-switch-plan §2.1）。
	Default bool
	// Effects：对会话的影响，取 Effect* 常量（explain / 启动横幅明示）。
	Effects []string
}

// 插件影响声明（plugin-switch-plan §3.4）。
const (
	EffectSystemPrompt  = "system_prompt"  // 改系统提示词
	EffectTools         = "tools"          // 增加工具
	EffectRewriteResult = "rewrite_result" // 改写工具结果 / 模型所见内容
	EffectExec          = "exec"           // 执行外部程序
	EffectNetwork       = "network"        // 访问网络
)

// Plugin 插件接口。
type Plugin interface {
	Meta() Meta
	Setup(cx *SetupCx) error
	Start(sc *Scope) error
}

// Factory 构造插件实例。
type Factory func() Plugin

var (
	regMu    sync.Mutex
	registry = map[string]Factory{}
)

// Register 编译期注册（在 init 里调用）。重名 panic——属编程错误。
func Register(name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("plugin: 重复注册 " + name)
	}
	registry[name] = f
}

// Registered 返回已注册插件名（排序）。
func Registered() []string {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// MetaOf 已登记插件的元数据（应用接口层列插件 Schema 用）；未登记返回 false。
func MetaOf(name string) (Meta, bool) {
	f, ok := lookup(name)
	if !ok {
		return Meta{}, false
	}
	return f().Meta(), true
}

func lookup(name string) (Factory, bool) {
	regMu.Lock()
	defer regMu.Unlock()
	f, ok := registry[name]
	return f, ok
}

// —— 层（调用点中间件栈）——//

// Position 层的位置。
type Position int

const (
	Transform Position = iota
	Guard
)

func (p Position) String() string {
	if p == Guard {
		return "Guard"
	}
	return "Transform"
}

// Failure 层失败策略。
type Failure int

const (
	FailOpen Failure = iota
	FailClosed
)

// LayerSpec 层登记规格。
type LayerSpec struct {
	Point     string // "model" / "tool"
	Position  Position
	Failure   Failure
	Timeout   time.Duration
	Order     int  // 同组内显式序
	Outermost bool // 必须是该调用点最外层 Transform（如 compact）
}

// Layer 一条已登记的层。Impl 由调用点模块按自己的接口断言。
type Layer struct {
	Spec      LayerSpec
	Plugin    string
	Mandatory bool
	Impl      any
}

// 层默认超时（01 §3.2）。
const (
	DefaultTransformTimeout = 2 * time.Second
	DefaultGuardTimeout     = 5 * time.Second
)

// —— 装配期上下文 ——//

// SetupCx 插件 Setup 时拿到的登记入口。
type SetupCx struct {
	asm    *Assembly
	plugin string
	meta   Meta
	config any
}

// Plugin 当前插件名。
func (cx *SetupCx) Plugin() string { return cx.plugin }

// Config 已解码的本插件配置（Meta.Config 的同类型指针）；无配置为 nil。
func (cx *SetupCx) Config() any { return cx.config }

// FillSlot 填插槽（必须在 Provides 里声明 "slot:<名>"）。
func (cx *SetupCx) FillSlot(name string, impl any) error {
	key := "slot:" + name
	if !contains(cx.meta.Provides, key) {
		return fmt.Errorf("插件 %s 未声明提供 %s", cx.plugin, key)
	}
	cx.asm.values[key] = impl
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "slot", Name: name})
	return nil
}

// Provide 登记插件服务（必须在 Provides 里声明 "service:<名>"）。
func (cx *SetupCx) Provide(name string, svc any) error {
	key := "service:" + name
	if !contains(cx.meta.Provides, key) {
		return fmt.Errorf("插件 %s 未声明提供 %s", cx.plugin, key)
	}
	cx.asm.values[key] = svc
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "service", Name: name})
	return nil
}

// Use 取依赖：只能取 Requires 里声明过的（含插槽默认实现）。
func Use[T any](cx *SetupCx, key string) (T, error) {
	var zero T
	if !contains(cx.meta.Requires, key) && !contains(cx.meta.Requires, Optional+key) {
		return zero, fmt.Errorf("插件 %s 未声明依赖 %s", cx.plugin, key)
	}
	v, ok := cx.asm.values[key]
	if !ok {
		return zero, fmt.Errorf("依赖 %s 不可用", key)
	}
	t, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("依赖 %s 类型不符（%T）", key, v)
	}
	return t, nil
}

// UseOptional 取可选依赖（Requires 写 "?<key>"）：提供者缺席返回零值与 false。
func UseOptional[T any](cx *SetupCx, key string) (T, bool, error) {
	var zero T
	if !contains(cx.meta.Requires, Optional+key) {
		return zero, false, fmt.Errorf("插件 %s 未声明可选依赖 %s", cx.plugin, key)
	}
	if _, ok := cx.asm.values[key]; !ok {
		return zero, false, nil
	}
	t, err := Use[T](cx, key)
	return t, err == nil, err
}

// AddLayer 登记调用点层。Guard 只能 FailClosed；超时 0 取默认值。
func (cx *SetupCx) AddLayer(spec LayerSpec, impl any) error {
	if spec.Point != "model" && spec.Point != "tool" {
		return fmt.Errorf("未知调用点 %q", spec.Point)
	}
	if spec.Position == Guard {
		if spec.Failure != FailClosed {
			return fmt.Errorf("插件 %s：Guard 层只能 FailClosed", cx.plugin)
		}
		if spec.Outermost {
			return fmt.Errorf("插件 %s：Outermost 只适用于 Transform", cx.plugin)
		}
	}
	if spec.Timeout == 0 {
		spec.Timeout = DefaultTransformTimeout
		if spec.Position == Guard {
			spec.Timeout = DefaultGuardTimeout
		}
	}
	cx.asm.layers = append(cx.asm.layers, Layer{Spec: spec, Plugin: cx.plugin,
		Mandatory: cx.asm.mandatory[cx.plugin], Impl: impl})
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: spec.Point + "_layer",
		Name: spec.Position.String()})
	return nil
}

// Register 通用点位登记（工具、适配器、钩子、事件订阅、OnSessionDelete、乙类点位……）。
// 由开位的模块按 point 名取用（Items）。
func (cx *SetupCx) Register(point, name string, impl any) {
	cx.asm.items[point] = append(cx.asm.items[point], Item{Plugin: cx.plugin, Name: name, Impl: impl})
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: point, Name: name})
}

// Item 通用点位上的一条登记。
type Item struct {
	Plugin string
	Name   string
	Impl   any
}

// Registration explain 用的登记记录。
type Registration struct {
	Plugin, Point, Name string
}

// —— 装配 ——//

// Entry profile 中的一行。
type Entry struct {
	Name    string
	Enabled *bool // 仅 profile 用：nil = 按插件缺省
	Config  map[string]any
}

// Spec 装配输入（由 profile 合并得出）。
type Spec struct {
	Plugins   []Entry
	Mandatory []string
	// Defaults：插槽默认实现（"slot:<名>" → 值），未被插件填充时使用。
	Defaults map[string]any
	// Inject：应用 / 内核预置的服务（"service:host"、"service:ui"……），插件经 Requires 取用；
	// 不能再由插件提供同名项。
	Inject map[string]any
}

// Optional 可选依赖前缀：Requires 写 "?service:sandbox"，提供者缺席也能装配。
const Optional = "?"

// Assembly 装配结果（只读）。
type Assembly struct {
	order     []string
	metas     map[string]Meta
	configs   map[string]any
	sources   map[string]map[string]string // 插件 → 配置键 → 来源
	values    map[string]any
	fromDef   map[string]bool
	injected  map[string]bool
	layers    []Layer
	items     map[string][]Item
	points    []Registration
	mandatory map[string]bool
	plugins   map[string]Plugin
	scopes    map[string]*Scope
}

// 装配错误。
var (
	ErrMissingDep = errors.New("plugin: 缺依赖")
	ErrCycle      = errors.New("plugin: 循环依赖")
	ErrDupSlot    = errors.New("plugin: 插槽多实现")
	ErrMandatory  = errors.New("plugin: 强制插件缺失")
	ErrUnknown    = errors.New("plugin: 未注册的插件")
)

// Assemble 实例化、拓扑排序、逐个 Setup，并做启动自检。
func Assemble(spec Spec) (*Assembly, error) {
	a := &Assembly{metas: map[string]Meta{}, configs: map[string]any{}, sources: map[string]map[string]string{},
		values: map[string]any{}, fromDef: map[string]bool{}, injected: map[string]bool{}, items: map[string][]Item{},
		mandatory: map[string]bool{}, plugins: map[string]Plugin{}, scopes: map[string]*Scope{}}
	enabled := map[string]Entry{}
	for _, e := range spec.Plugins {
		enabled[e.Name] = e
	}
	for _, m := range spec.Mandatory {
		if _, ok := enabled[m]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrMandatory, m)
		}
		a.mandatory[m] = true
	}
	providers := map[string]string{}
	names := make([]string, 0, len(enabled))
	for n := range enabled {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, ok := lookup(n)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknown, n)
		}
		p := f()
		m := p.Meta()
		if m.Name == "" {
			m.Name = n
		}
		a.plugins[n], a.metas[n] = p, m
		for _, pv := range m.Provides {
			if prev, dup := providers[pv]; dup {
				if strings.HasPrefix(pv, "slot:") {
					return nil, fmt.Errorf("%w: %s 由 %s 与 %s 同时提供", ErrDupSlot, pv, prev, n)
				}
				return nil, fmt.Errorf("plugin: 服务 %s 由 %s 与 %s 重复提供", pv, prev, n)
			}
			providers[pv] = n
		}
		cfg, src, err := decodeConfig(m.Config, enabled[n].Config)
		if err != nil {
			return nil, fmt.Errorf("插件 %s 配置: %w", n, err)
		}
		a.configs[n], a.sources[n] = cfg, src
	}
	for k, v := range spec.Inject {
		if pv, dup := providers[k]; dup {
			return nil, fmt.Errorf("plugin: %s 由应用预置，插件 %s 不能再提供", k, pv)
		}
		a.values[k] = v
		a.injected[k] = true
	}
	for k, v := range spec.Defaults {
		if _, filled := providers[k]; !filled {
			a.values[k] = v
			a.fromDef[k] = true
		}
	}
	// 拓扑排序（Kahn，同层按名字典序，结果确定）。
	deps := map[string][]string{}
	indeg := map[string]int{}
	for _, n := range names {
		indeg[n] += 0
		for _, r := range a.metas[n].Requires {
			opt := strings.HasPrefix(r, Optional)
			r = strings.TrimPrefix(r, Optional)
			if pv, ok := providers[r]; ok {
				if pv != n {
					deps[pv] = append(deps[pv], n)
					indeg[n]++
				}
				continue
			}
			if !a.fromDef[r] && !a.injected[r] && !opt {
				return nil, fmt.Errorf("%w: %s 需要 %s", ErrMissingDep, n, r)
			}
		}
	}
	var ready []string
	for _, n := range names {
		if indeg[n] == 0 {
			ready = append(ready, n)
		}
	}
	for len(ready) > 0 {
		sort.Strings(ready)
		n := ready[0]
		ready = ready[1:]
		a.order = append(a.order, n)
		for _, d := range deps[n] {
			if indeg[d]--; indeg[d] == 0 {
				ready = append(ready, d)
			}
		}
	}
	if len(a.order) != len(names) {
		var stuck []string
		for _, n := range names {
			if indeg[n] > 0 {
				stuck = append(stuck, n)
			}
		}
		return nil, fmt.Errorf("%w: %s", ErrCycle, strings.Join(stuck, ", "))
	}
	for _, n := range a.order {
		cx := &SetupCx{asm: a, plugin: n, meta: a.metas[n], config: a.configs[n]}
		if err := a.plugins[n].Setup(cx); err != nil {
			return nil, fmt.Errorf("插件 %s Setup: %w", n, err)
		}
		for _, pv := range a.metas[n].Provides {
			if _, ok := a.values[pv]; !ok {
				return nil, fmt.Errorf("插件 %s 声明提供 %s 但未登记", n, pv)
			}
		}
	}
	if err := a.checkLayers(); err != nil {
		return nil, err
	}
	return a, nil
}

// checkLayers 自检：每个调用点至多一个 Outermost，且必须是排序后的第一层。
func (a *Assembly) checkLayers() error {
	for _, pt := range []string{"model", "tool"} {
		ls := a.Layers(pt)
		for i, l := range ls {
			if l.Spec.Outermost && i != 0 {
				return fmt.Errorf("plugin: %s 调用点有多个 Outermost 层或排序冲突（%s）", pt, l.Plugin)
			}
		}
	}
	return nil
}

// Layers 返回调用点 point 的层，由外到内（plugin-manager §5.2）：
// Transform 在 Guard 外；同位置可选在外、强制在内；Outermost 最外；同组按 Order、再按插件名。
func (a *Assembly) Layers(point string) []Layer {
	var out []Layer
	for _, l := range a.layers {
		if l.Spec.Point == point {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if x.Spec.Position != y.Spec.Position {
			return x.Spec.Position < y.Spec.Position
		}
		if x.Spec.Outermost != y.Spec.Outermost {
			return x.Spec.Outermost
		}
		if x.Mandatory != y.Mandatory {
			return !x.Mandatory
		}
		if x.Spec.Order != y.Spec.Order {
			return x.Spec.Order < y.Spec.Order
		}
		return x.Plugin < y.Plugin
	})
	return out
}

// Items 通用点位上的全部登记（按装配顺序）。
func (a *Assembly) Items(point string) []Item { return a.items[point] }

// Value 取插槽 / 服务的最终实现。
func (a *Assembly) Value(key string) (any, bool) {
	v, ok := a.values[key]
	return v, ok
}

// Order 装配顺序。
func (a *Assembly) Order() []string { return append([]string(nil), a.order...) }

// Start 按装配顺序启动插件，各自在 root 下开插件作用域。失败时已启动的倒序关闭。
func (a *Assembly) Start(root *Scope) error {
	for _, n := range a.order {
		sc := root.Child("plugin:" + n)
		a.scopes[n] = sc
		if err := a.plugins[n].Start(sc); err != nil {
			_ = root.Close(root.Context())
			return fmt.Errorf("插件 %s Start: %w", n, err)
		}
	}
	return nil
}

// Degraded 后台任务重启耗尽的插件（explain 可见）。
func (a *Assembly) Degraded() map[string][]string {
	out := map[string][]string{}
	for n, sc := range a.scopes {
		if d := sc.Degraded(); len(d) > 0 {
			out[n] = d
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
