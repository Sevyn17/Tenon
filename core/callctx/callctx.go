// Package callctx：一次调用的上下文（框架设计 04 §4.4 的 *Ctx）。
//
// 组件是全局单例，会话相关的东西不放在组件字段里，而是随每次调用传进来：
// 租户、会话（含定义名与版本）、触发者、使用配置、业务 meta、trace。
// 内核写、组件只读；内核在会话打开时填好，之后不再变。
//
// 公共别名 ybase.Ctx 在 T1b-2 随 SDK 一起提供，本包是它的实现。
package callctx

import (
	"encoding/json"
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

// Trace 一次任务的追踪标识；沿信封传播（框架设计 11）。
type Trace struct {
	ID string
}

// Ctx 调用上下文。零值不可用，用 New 构造。
type Ctx struct {
	mu      sync.RWMutex
	tenant  string
	workdir string
	session Session
	meta    map[string]any // sessions.meta 解析结果（应用写的业务键）
	use     map[string]any // 使用配置 [use.*] 解析结果：组件名 → 该组件的配置
	trace   Trace
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

// Trace 追踪标识。
func (c *Ctx) Trace() Trace {
	if c == nil {
		return Trace{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.trace
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

// BindTrace 内核填 trace（一次任务贯穿多个会话时用）。
func (c *Ctx) BindTrace(t Trace) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace = t
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
