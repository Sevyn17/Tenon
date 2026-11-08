package permission

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Config 权限配置文件（用户级 ~/.tenon/permissions.toml、项目级 <dir>/.tenon/permissions.toml）。
//
//	mode  = "workspace"                    # 新会话缺省档位
//	ask   = ["shell(git push:*)"]
//	deny  = ["shell(rm -rf:*)", "write_file(.env)"]
type Config struct {
	Mode  string   `toml:"mode"`
	Allow []string `toml:"allow"`
	Ask   []string `toml:"ask"`
	Deny  []string `toml:"deny"`
}

// LoadConfig 读配置；文件不存在返回空配置。
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("permission: %s: %w", path, err)
	}
	if c.Mode != "" {
		if err := checkID(c.Mode); err != nil {
			return c, fmt.Errorf("permission: %s: %w", path, err)
		}
	}
	// §9.7 公共规则只能加严：放宽一律写进有名字的档位。
	if len(c.Allow) > 0 {
		return c, fmt.Errorf("permission: %s：公共规则只认 deny / ask，allow 请写进自定义档位的 [items]（tenon mode copy <基础档位> <新名>）", path)
	}
	if _, err := c.Parsed(); err != nil {
		return c, fmt.Errorf("permission: %s: %w", path, err)
	}
	return c, nil
}

// Parsed 把 allow/ask/deny 字符串转成规则。
func (c Config) Parsed() ([]Rule, error) {
	var rs []Rule
	for _, g := range []struct {
		list []string
		a    Action
	}{{c.Deny, Deny}, {c.Ask, AskUser}, {c.Allow, Allow}} {
		for _, s := range g.list {
			r, err := ParseRule(s, g.a)
			if err != nil {
				return nil, err
			}
			rs = append(rs, r)
		}
	}
	return rs, nil
}

// Merge 后者的 mode 覆盖前者；规则累加（deny 优先级由 Decide 保证，项目级无法放宽用户级 deny）。
func Merge(cs ...Config) Config {
	var out Config
	for _, c := range cs {
		if c.Mode != "" {
			out.Mode = c.Mode
		}
		out.Allow = append(out.Allow, c.Allow...)
		out.Ask = append(out.Ask, c.Ask...)
		out.Deny = append(out.Deny, c.Deny...)
	}
	return out
}

// Ext 键：会话档位与“本次会话内都允许”的规则存进会话 Ext，重启后仍生效。
const (
	ExtMode  = "permission.mode"
	ExtRules = "permission.allow"
)

// Sessions 进程内各会话的权限状态（档位 + 会话规则）。子会话（id 含 /）跟随顶层会话。
type Sessions struct {
	Root    string
	Rules   []Rule    // 公共规则（permissions.toml）
	Default Mode      // 未设置档位的会话用它
	Reg     *Registry // 档位注册表（nil = 只有内置档位）
	// Fixed 固定沙箱（-sandbox，或平台无沙箱实现时为 danger-full-access）；空 = 按档位。
	Fixed toolhub.SandboxMode
	// Load 首次用到某会话时读取其持久状态（可空）。
	Load func(sessionID string) (Mode, []string)
	// ModeApproved 受信项目的放宽档位是否已被用户确认（§9.6，应用按 trust.toml 实现；可空 = 一律未确认）。
	// 项目档位当新会话缺省时也要过这道确认，没确认过退回内置缺省档位。
	ModeApproved func(mode Mode, widened []string) bool

	mu     sync.Mutex
	state  map[string]*sessState
	tick   uint64         // 最近使用计数（state 超过 MaxCached 时按它淘汰）
	gen    map[Mode]int   // 档位定义变化次数（Notice 用）
	items  []toolhub.Item // 权限项注册表（首个会话装配时登记，mode show / 档位标注用）
	lookup func(tool string) []toolhub.Item
}

type sessState struct {
	mode  Mode
	gen   int
	rules []Rule
	raw   []string
	used  uint64
}

// MaxCached 内存里最多缓存多少个会话的权限状态。状态是会话头 Ext 的缓存（档位、会话规则都已持久化，
// Load 能读回），超过上限按最近使用淘汰最旧的四分之一（2026-11-04 审查 P2：原先只增不减）。
// 没有 Load 时状态只在内存里，不淘汰。
var MaxCached = 4096

// evict 超过上限时淘汰最近最少使用的会话。调用方持锁。
func (s *Sessions) evict() {
	if s.Load == nil || len(s.state) <= MaxCached {
		return
	}
	ids := make([]string, 0, len(s.state))
	for id := range s.state {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return s.state[ids[i]].used < s.state[ids[j]].used })
	for _, id := range ids[:len(ids)-MaxCached*3/4] {
		delete(s.state, id)
	}
}

// Top 顶层会话 id（子智能体 / 工作组成员 id 为 parent/...）。
func Top(id string) string {
	for i := 0; i < len(id); i++ {
		if id[i] == '/' {
			return id[:i]
		}
	}
	return id
}

func (s *Sessions) reg() *Registry {
	if s.Reg != nil {
		return s.Reg
	}
	return builtinReg()
}

func (s *Sessions) get(id string) *sessState {
	id = Top(id)
	s.mu.Lock()
	if st := s.state[id]; st != nil {
		s.tick++
		st.used = s.tick
		s.mu.Unlock()
		return st
	}
	s.mu.Unlock()
	def := s.defaultMode() // 不持锁：要读权限项注册表、可能调应用的确认查询
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		s.state = map[string]*sessState{}
	}
	if st := s.state[id]; st != nil {
		return st
	}
	st := &sessState{mode: def}
	if s.Load != nil {
		s.mu.Unlock()
		m, raw := s.Load(id)
		s.mu.Lock()
		if cur := s.state[id]; cur != nil {
			return cur
		}
		if m != "" {
			st.mode = m
		}
		for _, r := range raw {
			if rule, err := ParseRule(r, Allow); err == nil {
				st.rules = append(st.rules, rule)
				st.raw = append(st.raw, r)
			}
		}
	}
	st.gen = s.gen[st.mode]
	s.tick++
	st.used = s.tick
	s.state[id] = st
	s.evict()
	return st
}

// defaultMode 新会话的缺省档位：permissions.toml 的 mode；它若是受信项目提交的放宽档位
// 且用户没确认过，退回内置缺省档位（不让项目悄悄把所有新会话放宽，§9.6）。调用方不持锁。
func (s *Sessions) defaultMode() Mode {
	m := s.Default
	if m == "" {
		return Default
	}
	if d, ok := s.reg().Get(m); ok && d.Project {
		if w := ProjectWidened(d, s.itemsSnapshot()); len(w) > 0 && (s.ModeApproved == nil || !s.ModeApproved(m, w)) {
			return Default
		}
	}
	return m
}

// ProjectWidened 项目档位放宽了什么（应用确认放宽、记 trust.toml 时也用它，两边清单才一致）：按权限项比对（Widened），再加上继承根比内置缺省档位宽的情况
// （权限项注册表要到第一个会话装配时才有，这时只能从继承根看出「base = full」这类放宽）。
func ProjectWidened(d *ModeDef, items []toolhub.Item) []string {
	w := Widened(d, items)
	if rank(d.Root) > rank(Default) {
		w = append(w, "基础档位 "+string(d.Root))
	}
	return w
}

// rank 内置档位从严到宽的次序；未知（没有内置根）按最严。
func rank(m Mode) int {
	for i, b := range Builtins {
		if b == m {
			return i
		}
	}
	return -1
}

// itemsSnapshot 权限项注册表（首个会话装配时登记）；未登记时为空，Widened 只看档位自身写的项。
func (s *Sessions) itemsSnapshot() []toolhub.Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]toolhub.Item(nil), s.items...)
}

// Registry 档位注册表。
func (s *Sessions) Registry() *Registry { return s.reg() }

// Mode 会话当前档位。
func (s *Sessions) Mode(id string) Mode {
	st := s.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	return st.mode
}

// Def 会话当前档位的定义；档位已不存在（自定义文件被删）时退回内置 ask。
func (s *Sessions) Def(id string) *ModeDef {
	if d, ok := s.reg().Get(s.Mode(id)); ok {
		return d
	}
	return builtinDef(Ask)
}

// SetMode 切换档位（调用方负责持久化 ExtMode）。档位不存在时报错。
func (s *Sessions) SetMode(id string, m Mode) error {
	if _, ok := s.reg().Get(m); !ok {
		return fmt.Errorf("没有档位 %q", m)
	}
	st := s.get(id)
	s.mu.Lock()
	st.mode, st.gen = m, s.gen[m]
	s.mu.Unlock()
	return nil
}

// Notice 每回合开始调用：自定义档位文件有变化时重读，若本会话档位受影响返回一条提示（否则空）。
func (s *Sessions) Notice(id string) string {
	changed, err := s.reg().Refresh()
	s.mu.Lock()
	if s.gen == nil {
		s.gen = map[Mode]int{}
	}
	for _, m := range changed {
		s.gen[m]++
	}
	s.mu.Unlock()
	st := s.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		return fmt.Sprintf("[权限] 档位配置读取失败，沿用旧配置：%v", err)
	}
	if st.gen == s.gen[st.mode] {
		return ""
	}
	st.gen = s.gen[st.mode]
	if _, ok := s.reg().Get(st.mode); !ok {
		return fmt.Sprintf("[权限] 档位 %s 已不存在，本回合起按“每次询问”处理", st.mode)
	}
	return fmt.Sprintf("[权限] 档位 %s 的配置已更新，本回合起生效", st.mode)
}

// AllowForSession 追加一条会话规则，返回全部会话规则的字符串（供持久化 ExtRules）。
func (s *Sessions) AllowForSession(id string, r Rule) []string {
	st := s.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	r.Action = Allow
	for _, x := range st.raw {
		if x == r.String() {
			return append([]string(nil), st.raw...)
		}
	}
	st.rules = append(st.rules, r)
	st.raw = append(st.raw, r.String())
	return append([]string(nil), st.raw...)
}

// SessionRules 会话规则（展示用）。
func (s *Sessions) SessionRules(id string) []string {
	st := s.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), st.raw...)
}

// SandboxMode 实现 toolhub.SandboxPolicy：会话 shell 的沙箱模式。
func (s *Sessions) SandboxMode(id string) toolhub.SandboxMode { return s.SandboxFor(id, "shell") }

// SandboxFor 会话里某工具（shell、mcp__srv__*、hooks 等）的沙箱模式。
func (s *Sessions) SandboxFor(id, tool string) toolhub.SandboxMode {
	return s.Policy(id).SandboxOf(tool)
}

// Policy 会话策略：公共规则 + 会话规则 + 档位定义（deny/ask 规则仍优先于会话 allow）。
func (s *Sessions) Policy(id string) Policy {
	def := s.Def(id)
	st := s.get(id)
	s.mu.Lock()
	rules := append(append([]Rule(nil), s.Rules...), st.rules...)
	m := st.mode
	s.mu.Unlock()
	return Policy{Mode: m, Def: def, Rules: rules, Root: s.Root, Sandbox: s.Fixed}
}

// AllowsSandbox 实现 toolhub.SandboxApprover：没有 deny / ask 规则命中，且某条带沙箱模式的 allow 规则覆盖本次申请。
func (s *Sessions) AllowsSandbox(id, tool string, args json.RawMessage, req toolhub.SandboxMode) bool {
	p := s.Policy(id)
	all := p.allRules()
	for _, r := range all {
		if (r.Action == Deny || r.Action == AskUser) && p.hit(r, tool, args) {
			return false
		}
	}
	p.Rules = all
	return p.AllowsSandbox(tool, args, req)
}

// DefaultSandboxFor 新会话缺省档位下某工具的沙箱（进程级子进程用，如 MCP stdio 服务）。
func (s *Sessions) DefaultSandboxFor(tool string) toolhub.SandboxMode {
	if s.Fixed != "" {
		return s.Fixed
	}
	d, ok := s.reg().Get(s.defaultMode())
	if !ok {
		d = builtinDef(Ask)
	}
	_ = tool
	return d.SandboxLevel()
}
