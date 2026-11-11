// Package ext 扩展点的名字与类型化键（2026-11-04 审查 P1-48 / P1-49）。
//
// 叶子包：不 import 任何本模块的包，component、assemble、toolhub、store、host 与各模块都可以引用它，
// 不再靠复制常量绕开导入环。点位、服务、插槽的名字只在这里定义一次。
//
// 类型化键 Key[T] 把服务名和它的 Go 类型绑在一起：键定义在服务类型所在的包里
// （如 toolhub.KeySandbox、host.Key、assemble.KeyUI），组件用 component.Get(cx, key) 取，
// 返回值的类型由键推出，拼错键名是编译错误而不是运行期的"缺依赖"；
// Requires 里写 key.Need() / key.Maybe()，可选依赖不必手拼 "?" 前缀。
package ext

// Key 带类型的服务 / 插槽键。零值不可用，用 NewKey 构造。
type Key[T any] struct{ name string }

// NewKey 构造类型化键。name 用本包的常量（service:<名> / slot:<名>）。
func NewKey[T any](name string) Key[T] { return Key[T]{name: name} }

// Name 键名（service:<名> / slot:<名>）。
func (k Key[T]) Name() string { return k.name }

// Need 写进 Meta.Requires：必需依赖。
func (k Key[T]) Need() string { return k.name }

// Maybe 写进 Meta.Requires：可选依赖（提供者缺席也能装配）。
func (k Key[T]) Maybe() string { return Optional + k.name }

// Point 带类型的点位：点位名和登记项的 Go 类型绑在一起（计划/08 3.5）。和 Key 一样，具体的点位值定义在
// 登记项类型所在的包里（如 assemble.SessionTools），组件用 component.RegisterAt(cx, 点位, 名字, 实现) 登记，
// 实现的类型不对是编译错误。零值不可用，用 NewPoint 构造。
type Point[T any] struct{ name string }

// NewPoint 构造类型化点位。name 用本包的 Point* 常量。
func NewPoint[T any](name string) Point[T] { return Point[T]{name: name} }

// Name 点位名。
func (p Point[T]) Name() string { return p.name }

// Optional 可选依赖前缀。
const Optional = "?"

// 应用预置的服务（component.Spec.Inject 的键）。
const (
	ServiceHost     = "service:host"     // *host.Host
	ServiceUI       = "service:ui"       // assemble.UI
	ServiceWorkdir  = "service:workdir"  // string：工作目录绝对路径
	ServiceTrust    = "service:trust"    // bool：项目是否受信；未预置按受信
	ServicePackages = "service:packages" // []*pkgs.Package：已加载的插件包
)

// 组件提供的服务。
const (
	ServiceDB         = "service:db"         // assemble.DBHandle（store-sqlite）
	ServiceSQL        = "service:sql"        // store.SQL（store-sqlite）
	ServiceModelhub   = "service:modelhub"   // *assemble.Models（models）
	ServiceCompactor  = "service:compactor"  // agent.Compactor，可实现 assemble.Redactable（compact-basic）
	ServiceSandbox    = "service:sandbox"    // toolhub.Confiner（sandbox）
	ServicePermission = "service:permission" // toolhub.SandboxPolicy（permission）
)

// 插槽。
const (
	SlotSecrets = "slot:secrets" // assemble.Secrets；缺省 EnvSecrets
)

// 会话级点位（assemble.Collect 读取）。
const (
	PointSessionTools    = "session_tools"     // assemble.ToolsEntry
	PointSessionInit     = "session_init"      // assemble.InitEntry：会话构造时初始化（04 4.3.1 拆会话层后剩下的正当用途）
	PointSystemBlock     = "system_block"      // assemble.BlockEntry
	PointAfterTurn       = "after_turn"        // assemble.AfterTurnFunc
	PointReady           = "ready"             // assemble.ReadyFunc
	PointPermItems       = "perm_items"        // toolhub.Item
	PointStatus          = "status"            // component.StatusEntry
	PointOnSessionDelete = "on_session_delete" // store.Cleaner
)
