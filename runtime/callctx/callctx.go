// Package callctx：一次调用的上下文（框架设计 04 §4.4 的 *Ctx）。
//
// 组件是全局单例，会话相关的东西不放在组件字段里，而是随每次调用传进来：
// 租户、会话（含定义名与版本）、触发者、使用配置、业务 meta。
// 内核写、组件只读；内核在会话打开时填好，之后不再变。
//
// 公共别名 tenon.Ctx 在 T1b-2 随 SDK 一起提供，本包是它的实现。
package callctx

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Session 会话身份：*Ctx 里最常读的几项（完整行在存储里，这里只放调用要用到的）。
type Session struct {
	ID         string
	Child      bool   // 子会话（子智能体 / 工作组成员）：不带委派工具，深度上限 1
	Definition string // 定义名；空 = 未绑定定义
	Hash       string // 定义整包内容哈希（版本，框架设计 D1）
	Creator    string // 创建者（用户 id；空 = 本机）
	Parent     string // 父会话 id
	Key        string // 会话键
}

// Ctx 调用上下文。零值不可用，用 New 构造。
type Ctx struct {
	mu      sync.RWMutex
	tenant  string
	workdir string
	session Session
	meta    map[string]any // sessions.meta 解析结果（应用写的业务键）
	use     map[string]any // 使用配置 [use.*] 解析结果：组件名 → 该组件的配置
	// untrusted 本回合输入是否含不可信来源（内核每回合设置，见 SetUntrusted）。
	untrusted bool
	// trigger 本轮触发者（human 用户 id；空 = 非人触发）。守卫按它重解用户层上限（08 8.3，T4f）。
	trigger string
	// reResolve 按某个用户重解使用配置（内核按会话创建者算一份；这里按本轮触发者再算，T4f）。
	// nil = 不支持按触发者重解（退回会话算好的那份）。
	reResolve func(user, component string) (map[string]any, bool)
	// scope 会话作用域（04 4.3.1）：中间件拿「每会话一份」状态与历史回读用。
	// 由 assemble 在构造会话时设进来；类型是 any，避免 callctx 依赖 component。
	scope any
}

// Scope 会话作用域（04 4.3.1）。nil = 没有（装配期或测试）。
func (c *Ctx) Scope() any {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.scope
}

// SetScope 设会话作用域（assemble 在构造会话时调）。
func (c *Ctx) SetScope(s any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scope = s
}

// Options 内核构造 Ctx 用。
type Options struct {
	Tenant    string // 单机恒为本地租户
	Workdir   string
	SessionID string
	Child     bool
}

// New 构造一个只带租户、工作目录与会话 id 的 Ctx；其余字段由内核随后 Bind。
func New(o Options) *Ctx {
	return &Ctx{tenant: o.Tenant, workdir: o.Workdir,
		session: Session{ID: o.SessionID, Child: o.Child}}
}

// Tenant 租户（08 8.8：取不到租户一律报错的替代——内核建 Ctx 时必填）。
func (c *Ctx) Tenant() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tenant
}

// Session 会话身份。
func (c *Ctx) Session() Session {
	if c == nil {
		return Session{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.session
}

// SessionID 会话 id（空串 = 没有会话，如装配期调用）。
func (c *Ctx) SessionID() string { return c.Session().ID }

// Child 是否子会话。
func (c *Ctx) Child() bool { return c.Session().Child }

// Workdir 工作目录。
func (c *Ctx) Workdir() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.workdir
}

// Meta 应用写在会话上的业务键（只读副本）。
func (c *Ctx) Meta() map[string]any {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(c.meta))
	for k, v := range c.meta {
		out[k] = v
	}
	return out
}

// Use 本会话给某组件算好的使用配置（[use.<组件>]）；没有返回 (nil, false)。
func (c *Ctx) Use(component string) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.use[component]
	return v, ok
}

// Config 把本会话给某组件算好的使用配置填进组件自己的结构体（04 §4.4 的 *Ctx.Config）。
// 没有这份配置就原样返回，proto 保持组件的缺省值。值在建会话时已由框架算好并校验过。
func (c *Ctx) Config(component string, proto any) error {
	raw, ok := c.Use(component)
	if !ok || raw == nil || proto == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, proto); err != nil {
		return fmt.Errorf("callctx: 解析 [use.%s] 失败：%w", component, err)
	}
	return nil
}

// Bind 内核填会话身份与使用配置：会话打开（或新建）后调用一次，之后不变。
func (c *Ctx) Bind(s Session, meta, use json.RawMessage) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s.Child = s.Child || c.session.Child // Child 由构造方（子会话）决定，存储里没有这一列
	c.session = s
	c.meta = decodeObject(meta)
	c.use = decodeObject(use)
}

// SetTrigger 设置本轮触发者（human 用户 id；空 = 非人触发）。守卫按它重解用户层上限（T4f）。
func (c *Ctx) SetTrigger(user string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.trigger = user
	c.mu.Unlock()
}

// Trigger 本轮触发者（空 = 非人触发）。
func (c *Ctx) Trigger() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.trigger
}

// SetReResolver 内核注入「按用户重解使用配置」的能力（T4f）：f 返回该用户层算好的某组件配置。
func (c *Ctx) SetReResolver(f func(user, component string) (map[string]any, bool)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.reResolve = f
	c.mu.Unlock()
}

// UseForTrigger 按本轮触发者取某组件的使用配置（T4f，08 8.3）：有触发者且内核支持重解时用重解值，
// 否则退回会话算好的那份（Use）。第二返回值 false = 该组件没配置。
func (c *Ctx) UseForTrigger(component string) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	trig, f := c.trigger, c.reResolve
	c.mu.RUnlock()
	if trig != "" && f != nil {
		if v, ok := f(trig, component); ok {
			return v, true
		}
	}
	return c.Use(component)
}

// SetUntrusted 内核在每回合开始（以及回合中插入新输入）时设置：本回合的输入里有没有不可信来源
// （外部入口的信封、不可信输入）。权限组件据此把 allow 降为询问。只会在回合内由假变真，回合开始重设。
func (c *Ctx) SetUntrusted(v bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.untrusted = v
}

// Untrusted 本回合的输入是否含不可信来源。
func (c *Ctx) Untrusted() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.untrusted
}

// decodeObject 解析 JSON 对象；空或坏数据按空对象处理（meta 是应用写的数据，坏值不该让调用失败）。
func decodeObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
