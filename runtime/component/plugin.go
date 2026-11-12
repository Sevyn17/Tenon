// Package plugin：插件管理器（plugin-manager 定稿 §5）。
//
// 插件编译期注册（Register），profile 选用；装配期按依赖拓扑排序逐个 Setup，
// Setup 只做注册（插槽、服务、层、钩子……），资源一律在 Start 里建并挂到作用域上。
// 装配结束后注册表只读。
package component

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/ext"
	"github.com/Sevyn17/tenon/runtime/inject"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/useconf"
)

// Meta 插件元数据。Requires / Provides 用 "slot:<名>" / "service:<名>"。
type Meta struct {
	Name     string
	Version  string
	Requires []string
	Provides []string
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

// Component 插件接口。
type Component interface {
	Meta() Meta
	Setup(cx *Registrar) error
	Start(sc *Scope) error
}

// ServiceSQL 存储的 SQL 出口服务键（值为 store.SQL），由存储插件提供。
const ServiceSQL = ext.ServiceSQL

// Requires 插件对底层能力的要求（框架设计 05 §5.7）。装配期检查，不满足直接装配失败。
type Requires struct {
	// SQL 需要执行 SQL 与自建表迁移（组件自有 p_<名>_* 表）：
	// 装配里必须有人提供 ServiceSQL；纯非 SQL 存储（如内存实现）会导致装配失败。
	SQL bool
}

// Requirer 声明能力要求的插件实现它；没实现等于无要求。
type Requirer interface {
	Requires() Requires
}

// Factory 构造插件实例。
type Factory func() Component

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

// Instantiate 实例化一个已登记的插件（应用预设按名字取内置组件用）；未登记返回 false。
func Instantiate(name string) (Component, bool) {
	f, ok := lookup(name)
	if !ok {
		return nil, false
	}
	return f(), true
}

// —— 装配期上下文 ——//

// Registrar 插件 Setup 时拿到的登记入口。
type Registrar struct {
	asm    *Assembly
	plugin string
	meta   Meta
	config any
}

// Plugin 当前插件名。
func (cx *Registrar) Plugin() string { return cx.plugin }

// Config 已解码的本插件配置（Meta.Config 的同类型指针）；无配置为 nil。
func (cx *Registrar) Config() any { return cx.config }

// AgentConfig 登记本组件的使用配置原型（06 6.2）：零值结构体指针，字段用 toml 标签。
// 装配结束后由框架按定义里的 [use.<组件>] 与租户/用户层算出每个会话的值（06 6.3）。
func (cx *Registrar) AgentConfig(proto any) error {
	sc, err := useconf.SchemaOf(cx.plugin, proto)
	if err != nil {
		return err
	}
	if _, dup := cx.asm.agentSchemas[cx.plugin]; dup {
		return fmt.Errorf("插件 %s 重复登记使用配置", cx.plugin)
	}
	cx.asm.agentSchemas[cx.plugin] = sc
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "agent_config", Name: cx.plugin})
	return nil
}

// Template 登记本组件的内置定义模板（04 4.2、03 3.5）：模板归入平台命名空间，
// 与应用随程序发布的平台定义合并成会话定义的来源。
//
// 这里只登记、不校验；冲突（模板之间同名、与应用平台定义同名）在 Assembly.Definitions
// 里报告，装配失败即启动报错（03 3.5）。
func (cx *Registrar) Template(src agentdef.Source) {
	cx.asm.templates = append(cx.asm.templates, agentdef.Template{Owner: cx.plugin, Source: src})
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "template", Name: cx.plugin})
}

// StatusEntry 一份状态提交登记（Registrar.Status；04 4.7）。
type StatusEntry struct {
	Key string
	Fn  inject.Func
}

// Status 登记一份状态提交（框架设计 12.5）：每次调模型前框架调用 fn，按 key 只发变化的
// 片段，合并成一条 status 条目追加在本次请求末尾。key 可 * 结尾表示动态前缀。
func (cx *Registrar) Status(key string, fn inject.Func) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("插件 %s：状态提交缺 key", cx.plugin)
	}
	if fn == nil {
		return fmt.Errorf("插件 %s：状态提交 %s 缺函数", cx.plugin, key)
	}
	cx.asm.items[PointStatus] = append(cx.asm.items[PointStatus], Item{Plugin: cx.plugin, Name: key,
		Impl: StatusEntry{Key: key, Fn: fn}})
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: PointStatus, Name: key})
	return nil
}

// FillSlot 填插槽（必须在 Provides 里声明 "slot:<名>"）。
func (cx *Registrar) FillSlot(name string, impl any) error {
	key := "slot:" + name
	if !slices.Contains(cx.meta.Provides, key) {
		return fmt.Errorf("插件 %s 未声明提供 %s", cx.plugin, key)
	}
	cx.asm.values[key] = impl
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "slot", Name: name})
	return nil
}

// Provide 登记插件服务（必须在 Provides 里声明 "service:<名>"）。
func (cx *Registrar) Provide(name string, svc any) error {
	key := "service:" + name
	if !slices.Contains(cx.meta.Provides, key) {
		return fmt.Errorf("插件 %s 未声明提供 %s", cx.plugin, key)
	}
	cx.asm.values[key] = svc
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "service", Name: name})
	return nil
}

// Use 取依赖：只能取 Requires 里声明过的（含插槽默认实现）。
func Use[T any](cx *Registrar, key string) (T, error) {
	var zero T
	if !slices.Contains(cx.meta.Requires, key) && !slices.Contains(cx.meta.Requires, Optional+key) {
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
func UseOptional[T any](cx *Registrar, key string) (T, bool, error) {
	var zero T
	if !slices.Contains(cx.meta.Requires, Optional+key) {
		return zero, false, fmt.Errorf("插件 %s 未声明可选依赖 %s", cx.plugin, key)
	}
	if _, ok := cx.asm.values[key]; !ok {
		return zero, false, nil
	}
	t, err := Use[T](cx, key)
	return t, err == nil, err
}

// Register 通用点位登记（工具、适配器、钩子、事件订阅、OnSessionDelete、乙类点位……）。
// 由开位的模块按 point 名取用（Items）。
func (cx *Registrar) Register(point, name string, impl any) {
	cx.asm.items[point] = append(cx.asm.items[point], Item{Plugin: cx.plugin, Name: name, Impl: impl})
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: point, Name: name})
}

// RegisterAt 类型化点位登记：impl 的类型由点位决定，写错是编译错误（计划/08 3.5）。
// 内置组件都用它；Register(point, name, any) 留给外置插件与动态点位。
func RegisterAt[T any](cx *Registrar, p ext.Point[T], name string, impl T) {
	cx.Register(p.Name(), name, impl)
}

// PointItems 返回一个读取 point 上全部登记的函数：装配结束后调用才完整（别的组件可能排在本组件之后 Setup）。
// 开位的组件用它在运行时取登记项，不必让应用在装配后再接线（2026-11-04 审查 P1-25）。
func (cx *Registrar) PointItems(point string) func() []Item {
	asm := cx.asm
	return func() []Item { return asm.items[point] }
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
	// Instances：直接给出的插件实例（应用自带组件、测试替身用）；有实例时不再走注册表。
	// 名字仍是 Entry.Name，实例的 Meta().Name 缺省时补成这个名字。
	Instances map[string]Component
}

// Optional 可选依赖前缀：Requires 写 "?service:sandbox"，提供者缺席也能装配。
const Optional = ext.Optional

// Assembly 装配结果（只读）。
type Assembly struct {
	order     []string
	metas     map[string]Meta
	configs   map[string]any
	sources   map[string]map[string]string // 插件 → 配置键 → 来源
	values    map[string]any
	fromDef   map[string]bool
	injected  map[string]bool
	items     map[string][]Item
	points    []Registration
	mandatory map[string]bool
	plugins   map[string]Component
	scopes    map[string]*Scope
	// agentSchemas 各组件登记的使用配置原型（06 6.2）；键是组件名。
	agentSchemas map[string]useconf.Schema
	// templates 组件登记的内置定义模板（03 3.5、计划/01 T1b-2c）；装配结束后只读。
	templates []agentdef.Template
	// middleware 洋葱式中间件（04 4.3）：点位名 → 按登记顺序的中间件。
	middleware map[string][]mwReg
	// guardsByPoint 判定区规则（04 4.3.1）：点位名 → 按登记顺序的判定。
	guardsByPoint map[string][]guardReg
	// authorizer 决策服务（04 4.3.1）：恰好一个，nil = 用内核缺省。
	authorizer       Authorizer
	authorizerPlugin string
	// collections 组件登记的集合（05 5.6，T4a）；OpenCollections 之前句柄不可用。
	collections []collectionReg
	// collMigrations 集合迁移登记，按 After 拓扑排序后跑。
	collMigrations []collectionMigration
	// subscriptions 组件登记的事件订阅（04 4.6，T4b）；deliverEvents 在 Start 里按读位点投递。
	subscriptions []subscription
	// eventBackoff 订阅失败重试的起始间隔。零值取 EventRetryBackoff；测试调小。
	eventBackoff time.Duration
	// resources 组件登记的资源类型（04 4.7，T4c）。键是地址类型名。
	resources map[string]ResourceType
	// matchers 订阅匹配器（04 4.7，T4c）。内置三个在组件 Setup 之前登记，组件同名登记拒绝。
	matchers map[string]matcherReg
	// packages 已加载插件包的摘要（T4d，explain 用）；应用在装配后经 SetPackages 填。
	packages []PackageInfo
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
		mandatory: map[string]bool{}, plugins: map[string]Component{}, scopes: map[string]*Scope{},
		agentSchemas: map[string]useconf.Schema{}, resources: map[string]ResourceType{}, matchers: map[string]matcherReg{},
		middleware: map[string][]mwReg{}, guardsByPoint: map[string][]guardReg{}}
	a.installBuiltinMatchers()
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
		p, ok := spec.Instances[n]
		if !ok {
			f, found := lookup(n)
			if !found {
				return nil, fmt.Errorf("%w: %s", ErrUnknown, n)
			}
			p = f()
		}
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
		cx := &Registrar{asm: a, plugin: n, meta: a.metas[n], config: a.configs[n]}
		if err := a.plugins[n].Setup(cx); err != nil {
			return nil, fmt.Errorf("插件 %s Setup: %w", n, err)
		}
		for _, pv := range a.metas[n].Provides {
			if _, ok := a.values[pv]; !ok {
				return nil, fmt.Errorf("插件 %s 声明提供 %s 但未登记", n, pv)
			}
		}
	}
	if err := a.checkRequirements(); err != nil {
		return nil, err
	}
	return a, nil
}

// checkRequirements 自检：声明了能力要求的插件必须拿到对应服务（框架设计 05 §5.7）。
// 放在 Setup 之后，因为提供方在 Setup 里才登记。
func (a *Assembly) checkRequirements() error {
	for _, n := range a.order {
		rq, ok := a.plugins[n].(Requirer)
		if !ok {
			continue
		}
		if r := rq.Requires(); r.SQL {
			if _, ok := a.values[ServiceSQL]; !ok {
				return fmt.Errorf("插件 %s 需要 SQL 存储（Requires{SQL: true}），但装配里没有提供 %s；换成 SQL 存储或关掉这个插件", n, ServiceSQL)
			}
		}
	}
	return nil
}

// Items 通用点位上的全部登记（按装配顺序）。
func (a *Assembly) Items(point string) []Item { return a.items[point] }

// PointStatus 状态提交点位（04 4.7 / 框架设计 12.5）。
const PointStatus = ext.PointStatus

// Statuses 已登记的状态提交（按装配顺序）。
func (a *Assembly) Statuses() []inject.Reg {
	var out []inject.Reg
	for _, it := range a.items[PointStatus] {
		se, ok := it.Impl.(StatusEntry)
		if !ok {
			continue
		}
		out = append(out, inject.Reg{Key: se.Key, Owner: it.Plugin, Fn: se.Fn})
	}
	return out
}

// Value 取插槽 / 服务的最终实现。
func (a *Assembly) Value(key string) (any, bool) {
	v, ok := a.values[key]
	return v, ok
}

// Order 装配顺序。
func (a *Assembly) Order() []string { return append([]string(nil), a.order...) }

// AgentSchemas 各组件登记的使用配置原型，按组件名排序（建会话时算 use_config 用）。
func (a *Assembly) AgentSchemas() []useconf.Schema {
	out := make([]useconf.Schema, 0, len(a.agentSchemas))
	for _, name := range a.Order() {
		if sc, ok := a.agentSchemas[name]; ok {
			out = append(out, sc)
		}
	}
	return out
}

// AgentSchema 查某个组件的使用配置原型。
func (a *Assembly) AgentSchema(component string) (useconf.Schema, bool) {
	sc, ok := a.agentSchemas[component]
	return sc, ok
}

// Templates 组件登记的内置定义模板（按装配顺序；03 3.5）。
func (a *Assembly) Templates() []agentdef.Template {
	return append([]agentdef.Template(nil), a.templates...)
}

// Definitions 把应用的平台定义与组件模板合成会话定义的来源（03 3.5、计划/01 T1b-2c）。
//
// 模板之间同名、模板与应用平台定义同名时返回错误；应用层把这个错误当装配失败，
// 即启动报错。app 可以是 nil（应用没有随程序发布的平台定义）。
func (a *Assembly) Definitions(app agentdef.Source) (agentdef.Source, error) {
	return agentdef.WithTemplates(app, a.templates)
}

// Start 按装配顺序启动插件，各自在 root 下开插件作用域。失败时已启动的倒序关闭。
//
// 集合在第一个用到它的插件 Start 之前打开（T4a）：存储由 WithStore 预置时，启动前就打开；
// 存储由插件提供时，在该插件 Start 之后、依赖它的插件 Start 之前打开。
func (a *Assembly) Start(root *Scope) error {
	if a.injected[ext.ServiceDB] {
		db, err := a.openedDB()
		if err != nil {
			return err
		}
		if err := a.OpenCollections(root.Context(), db); err != nil {
			return err
		}
		if err := a.deliverEvents(root.Context(), root, db); err != nil {
			return err
		}
	}
	for _, n := range a.order {
		sc := root.Child("plugin:" + n)
		a.scopes[n] = sc
		if err := a.plugins[n].Start(sc); err != nil {
			_ = root.Close(root.Context())
			return fmt.Errorf("插件 %s Start: %w", n, err)
		}
		if a.metas[n].providesDB() && !a.injected[ext.ServiceDB] {
			db, err := a.openedDB()
			if err != nil {
				_ = root.Close(root.Context())
				return err
			}
			if err := a.OpenCollections(root.Context(), db); err != nil {
				_ = root.Close(root.Context())
				return err
			}
			if err := a.deliverEvents(root.Context(), root, db); err != nil {
				_ = root.Close(root.Context())
				return err
			}
		}
	}
	return nil
}

// providesDB 这个插件是否提供存储服务。
func (m Meta) providesDB() bool {
	for _, p := range m.Provides {
		if p == ext.ServiceDB {
			return true
		}
	}
	return false
}

// openedDB 取已打开的存储。Start 之前为 nil，报"尚未启动"。
func (a *Assembly) openedDB() (store.Store, error) {
	v, ok := a.values[ext.ServiceDB]
	if !ok {
		return nil, fmt.Errorf("装配: 没有提供 %s 的存储", ext.ServiceDB)
	}
	h, ok := v.(interface{ DB() store.Store })
	if !ok || h.DB() == nil {
		return nil, fmt.Errorf("装配: 存储 %s 尚未打开", ext.ServiceDB)
	}
	return h.DB(), nil
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

// Get 按类型化键取依赖（审查 P1-48）：返回值类型由键推出，键名不必手写字符串。语义同 Use。
func Get[T any](cx *Registrar, k ext.Key[T]) (T, error) { return Use[T](cx, k.Name()) }

// GetOptional 按类型化键取可选依赖（Requires 写 k.Maybe()）。语义同 UseOptional。
func GetOptional[T any](cx *Registrar, k ext.Key[T]) (T, bool, error) {
	return UseOptional[T](cx, k.Name())
}
