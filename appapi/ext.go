package appapi

// 产品扩展点（Mortise docs/61 第 3 节，T0）：产品注册带命名空间的命令、能力与事件，不改 tenon 协议版本。
//
// 规则（docs/61 3.2）：
//   - 扩展命令名一律 <ns>.<域>.<动作>；只有冻结的「历史产品根」（platform.、models.providers.）允许不带命名空间。
//   - 扩展命令不能落在 tenon 保留根下（session.、mode.、workspace. 等，见 reservedRoots），也不能与核心命令或别的扩展同名：
//     New 返回错误，启动失败。
//   - 缺省只给 operator；ExtCommand.Viewer 打开 viewer。
//   - 扩展能力并进 hello.capabilities；hello.extensions 列出 {namespace, version}（旧客户端忽略）。
//   - 产品事件经 App.Emit 发出，kind 必须带某个已注册扩展的命名空间前缀；会话事件只推订阅了该会话的连接，
//     session 为空的全局事件推给所有连接。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/Sevyn17/tenon/appapi/guard"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/bus"
)

// Extension 产品扩展：一组带命名空间的命令、能力与事件。
type Extension interface {
	Namespace() string               // 如 "acme"；命令名与事件 kind 的前缀
	Commands() map[string]ExtCommand // 键是完整命令名
	Capabilities() []string          // 并进 hello.capabilities
}

// ExtVersioner Extension 的可选扩展：hello.extensions 里报的版本；没实现报空串。
type ExtVersioner interface {
	Version() string
}

// ExtCommand 一条扩展命令。
type ExtCommand struct {
	Handler ExtHandler
	Viewer  bool // viewer 也能调（缺省只给 operator）
}

// ExtHandler 扩展命令的处理函数。返回的错误统一按 AsError 归类。
type ExtHandler func(ctx context.Context, c ExtCall, args json.RawMessage) (any, error)

// ExtCall 一次扩展命令调用的上下文。
type ExtCall struct {
	App       *App           // 发事件（Emit）、重建（Rebuild）、进程内调核心命令（Call）
	Principal wire.Principal // 连接身份；进程内调用是 LocalPrincipal
}

// Bind 把类型化的处理函数包成 ExtHandler：解析参数（空或 null = 零值；解析失败 bad_request）、错误归类成 *Error。
func Bind[In, Out any](f func(context.Context, ExtCall, In) (Out, error)) ExtHandler {
	return func(ctx context.Context, c ExtCall, raw json.RawMessage) (any, error) {
		var in In
		if t := bytes.TrimSpace(raw); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
			if err := json.Unmarshal(t, &in); err != nil {
				return nil, Errorf(wire.CodeBadRequest, "bad arguments: %v", err)
			}
		}
		out, err := f(ctx, c, in)
		if err != nil {
			return nil, AsError(err)
		}
		return out, nil
	}
}

// reservedRoots tenon 保留的命令根（docs/61 3.2）：扩展命令不能以它们开头（历史产品根除外）。
// 含 B 卡将要加的 terminal. 等。不带点的项按「等于或以它加点开头」判断（interaction 另外覆盖 interactions.）。
var reservedRoots = []string{
	"session.", "sessions.", "mode.", "workspace.", "models.", "mcp.", "skills.", "packages.", "plugins.",
	"changes.", "artifact.", "interaction", "trace.", "feedback.", "hello",
	"terminal.", "browser.", "memory.", "agents.", "goal.", "tasks.", "git.",
}

// legacyRoots 冻结的「历史产品根」：platform.*、models.providers.* 迁出后名字不变，由产品扩展注册（docs/61 3.2）。
// 只有这两个根允许不带命名空间。冻结：不要再加。
var legacyRoots = []string{"platform.", "models.providers."}

// ReservedRoots tenon 保留的命令根（只读副本，产品自己的一致性测试用）。
func ReservedRoots() []string { return slices.Clone(reservedRoots) }

// LegacyRoots 冻结的历史产品根（只读副本）。
func LegacyRoots() []string { return slices.Clone(legacyRoots) }

func hasRoot(name, root string) bool {
	if strings.HasSuffix(root, ".") || root == "interaction" {
		return strings.HasPrefix(name, root)
	}
	return name == root || strings.HasPrefix(name, root+".")
}

// extTable 装配后的扩展命令表。零值（没有扩展）可用。
type extTable struct {
	cmds  map[string]ExtCommand
	ns    []string // 命名空间，按注册顺序
	caps  []string
	infos []wire.ExtensionInfo
}

// buildExt 校验并合并扩展：命名空间合法且不重复，命令名带命名空间（或历史产品根）、不落在保留根、不与核心命令或别的扩展同名。
func buildExt(exts []Extension) (*extTable, error) {
	t := &extTable{cmds: map[string]ExtCommand{}}
	core := map[string]bool{}
	for _, c := range ConnCommands() {
		core[c] = true
	}
	owner := map[string]string{}
	for _, e := range exts {
		if e == nil {
			continue
		}
		ns := e.Namespace()
		if ns == "" || strings.ContainsAny(ns, ". \t\n") {
			return nil, fmt.Errorf("appapi: invalid extension namespace %q (must be non-empty, without dots or spaces)", ns)
		}
		for _, r := range reservedRoots {
			if hasRoot(ns+".", r) || strings.TrimSuffix(r, ".") == ns {
				return nil, fmt.Errorf("appapi: extension namespace %q is a reserved root", ns)
			}
		}
		for _, r := range legacyRoots {
			if strings.TrimSuffix(r, ".") == ns || strings.HasPrefix(r, ns+".") {
				return nil, fmt.Errorf("appapi: extension namespace %q conflicts with a legacy root", ns)
			}
		}
		if slices.Contains(t.ns, ns) {
			return nil, fmt.Errorf("appapi: extension namespace %q registered twice", ns)
		}
		t.ns = append(t.ns, ns)
		names := make([]string, 0)
		cmds := e.Commands()
		for k := range cmds {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			c := cmds[name]
			if c.Handler == nil {
				return nil, fmt.Errorf("appapi: extension %s command %s has no handler", ns, name)
			}
			legacy := false
			for _, r := range legacyRoots {
				if strings.HasPrefix(name, r) && len(name) > len(r) {
					legacy = true
				}
			}
			if !legacy {
				if !strings.HasPrefix(name, ns+".") || len(name) == len(ns)+1 {
					return nil, fmt.Errorf("appapi: extension %s command %s must start with %s.", ns, name, ns)
				}
				for _, r := range reservedRoots {
					if hasRoot(name, r) {
						return nil, fmt.Errorf("appapi: extension %s command %s is under reserved root %s", ns, name, r)
					}
				}
			}
			if core[name] {
				return nil, fmt.Errorf("appapi: extension %s command %s collides with a core command", ns, name)
			}
			if o, dup := owner[name]; dup {
				return nil, fmt.Errorf("appapi: command %s registered by both %s and %s", name, o, ns)
			}
			owner[name] = ns
			t.cmds[name] = c
		}
		for _, c := range e.Capabilities() {
			if c != "" && !slices.Contains(t.caps, c) {
				t.caps = append(t.caps, c)
			}
		}
		info := wire.ExtensionInfo{Namespace: ns}
		if v, ok := e.(ExtVersioner); ok {
			info.Version = v.Version()
		}
		t.infos = append(t.infos, info)
	}
	return t, nil
}

func (t *extTable) lookup(cmd string) (ExtCommand, bool) {
	if t == nil {
		return ExtCommand{}, false
	}
	c, ok := t.cmds[cmd]
	return c, ok
}

func (t *extTable) empty() bool { return t == nil || len(t.ns) == 0 }

// extInfos hello.extensions；没有扩展时为 nil（omitempty 不发，旧客户端看到的 hello 不变）。
func (a *App) extInfos() []wire.ExtensionInfo {
	if a.ext.empty() {
		return nil
	}
	return slices.Clone(a.ext.infos)
}

// ExtCommands 本 App 装配的扩展命令名（排序后）。Commands / ConnCommands 只列核心命令。
func (a *App) ExtCommands() []string {
	if a.ext == nil {
		return nil
	}
	out := make([]string, 0, len(a.ext.cmds))
	for k := range a.ext.cmds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// callExt 执行扩展命令；不是扩展命令时 ok = false。
func (a *App) callExt(ctx context.Context, p wire.Principal, cmd string, args json.RawMessage) (out any, ok bool, err error) {
	c, ok := a.ext.lookup(cmd)
	if !ok {
		return nil, false, nil
	}
	// 扩展命令不能跨工作区（docs/63 W-3）：handler 里经 App.Call 调核心命令时 all_workspaces 不生效。
	out, err = c.Handler(withoutAllWorkspaces(withPrincipal(ctx, p)), ExtCall{App: a, Principal: p}, args)
	if err != nil {
		return nil, true, AsError(err)
	}
	return out, true, nil
}

// —— 产品事件 ——

// extBusKind 总线上产品事件的种类（data 是 extEvent）；线上 kind 是 extEvent.Kind。
const extBusKind = "ext_event"

type extEvent struct {
	Kind    string
	Payload any
}

// ErrExtKind Emit 的 kind 没有带已注册扩展的命名空间前缀。
var ErrExtKind = errors.New("appapi: event kind must start with a registered extension namespace and a dot")

// Emit 发产品事件（易失，不进事件表）。kind 必须以某个已注册扩展的命名空间加点开头（如 acme.automation.run），
// 否则返回 ErrExtKind。session 非空：推给订阅了这个会话（或它的顶层会话）的连接；空：推给所有连接。
func (a *App) Emit(session, kind string, data any) error {
	if !a.extKindOK(kind) {
		return fmt.Errorf("%w: %q", ErrExtKind, kind)
	}
	EmitOn(a.o.Bus, session, kind, data)
	return nil
}

// EmitOn 不经 App 发产品事件（docs/62 K1）：给不持有 App 的组件插件用，总线取自 component.Use(cx, assemble.ServiceHost) 的 host.Bus。
// 语义同 App.Emit：易失、不进事件表；session 非空只推订阅了这个会话（或它的顶层会话）的连接，空推所有连接。
// 这里不知道装了哪些扩展，命名空间校验在连接推送前（与进程内 App.Subscribe 转换时）做：kind 不带已注册扩展的
// 命名空间前缀的事件被丢弃，不会到任何连接。data 须可 JSON 序列化（线上 data 原样编码；以后映射到 extproc 消息）。
// b 为 nil 时什么也不做。
func EmitOn(b *bus.Bus, session, kind string, data any) {
	if b == nil {
		return
	}
	b.Publish(bus.Envelope{Session: session, Kind: extBusKind, Data: extEvent{Kind: kind, Payload: data}})
}

// extKindOK kind 是否以某个已注册扩展的命名空间加点开头（且点后非空）。
func (a *App) extKindOK(kind string) bool {
	if a == nil || a.ext == nil {
		return false
	}
	for _, ns := range a.ext.ns {
		if strings.HasPrefix(kind, ns+".") && len(kind) > len(ns)+1 {
			return true
		}
	}
	return false
}

// Service 取当前装配里登记的服务 / 插槽最终实现（docs/62 K1，即 component.Assembly.Value）：
// 扩展命令经 c.App.Service("service:acme.terminal") 找到当前装配的对象。切工作区或重建后
// 新 App 对应新装配，旧 App 上取到的是旧装配的值。没有这个键、或 App 没接装配（Options.Services 为空）时返回 nil, false。
func (a *App) Service(key string) (any, bool) {
	if a == nil || a.o.Services == nil {
		return nil, false
	}
	return a.o.Services(key)
}

// watchExt 连接级产品事件：全局的都推，会话的只推订阅了的。只有装了扩展的 App 才起。
func (c *Conn) watchExt(sub *bus.Sub) {
	defer guard.Recover("appapi extension event watcher", func() { c.Close() })
	for e := range sub.C() {
		ev, ok := e.Data.(extEvent)
		if !ok || !c.a.extKindOK(ev.Kind) { // EmitOn 不校验命名空间，推送前在这里拒
			continue
		}
		if e.Session != "" {
			c.mu.Lock()
			_, sub1 := c.subs[e.Session]
			_, sub2 := c.subs[topOf(e.Session)]
			c.mu.Unlock()
			if !sub1 && !sub2 {
				continue
			}
		}
		c.push(c.ctx, []wire.Envelope{c.envelope(e.Session, ev.Kind, 0, 0, ev.Payload)})
	}
}

// —— 重建 ——

// Rebuild 产品命令改了运行时依赖的配置后重建运行时（复用 mcp / providers 的「先写、判空闲、重建、失败回滚」）。
// 调用方先写好磁盘，再调 Rebuild；undo 把磁盘改回（可空）。
//   - 没有能重建的工作区管理器（CLI）：什么也不做，返回 nil（下次运行生效）。
//   - 有回合在跑且没带 force：调 undo，返回 not_idle（details.running / sessions）；带 force 先取消再重建。
//   - 重建失败：调 undo；回到了原配置返回 invalid（details.reverted = true），否则 internal。
//
// 成功后本 App 已被新 App 替换（旧连接随后断开）。
func (a *App) Rebuild(ctx context.Context, force bool, undo func() error) error {
	rl, ok := a.o.Workspaces.(Reloader)
	if a.o.Workspaces == nil || !ok {
		return nil
	}
	fail := func(e *Error) error {
		if undo != nil {
			if err := undo(); err != nil {
				e.Message += "; undo failed: " + err.Error()
			}
		}
		return e
	}
	ids, err := a.runningIDs(ctx)
	if err != nil {
		return fail(AsError(err))
	}
	if len(ids) > 0 {
		if !force {
			e := Errorf(wire.CodeNotIdle, "%d session(s) running: rebuild would interrupt them (use force)", len(ids))
			e.Details = map[string]any{"running": len(ids), "sessions": ids}
			return fail(e)
		}
		a.cancelAll(ctx, ids)
	}
	if _, err := rl.Reload(ctx); err != nil {
		var rv *ReloadReverted
		if errors.As(err, &rv) {
			e := Errorf(wire.CodeInvalid, "%s", rv.Error())
			e.Details = map[string]any{"reverted": true}
			return fail(e)
		}
		return fail(Errorf(wire.CodeInternal, "rebuild failed: %v", err))
	}
	return nil
}

// —— 进程内订阅 ——

// EventSub 进程内事件订阅（App.Subscribe）。事件已转成线上信封（与连接上收到的同形），持久事件带 cursor。
type EventSub struct {
	ch   chan wire.Envelope
	mu   sync.Mutex
	err  error
	stop context.CancelFunc
}

// C 事件通道；订阅结束（ctx 结束、Close、跟不上被断开）时关闭，原因看 Err。
func (s *EventSub) C() <-chan wire.Envelope { return s.ch }

// Err 订阅结束的原因：正常结束为 nil 或 context 错误；跟不上被断开是 *bus.ErrSlow。
func (s *EventSub) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close 结束订阅。
func (s *EventSub) Close() { s.stop() }

// Subscribe 进程内订阅会话事件（自动化等进程内消费者用，不经协议连接，docs/57 4.2 钩子 2）：
// session 为空 = 本进程所有会话；非空 = 这个会话及其子会话。kinds 为空 = 全部公开事件，否则只要这些线上 kind
// （如 wire.EvTurnEnded、wire.EvInteractionOpen，或扩展事件 kind）。只收订阅之后的实时事件，不回放；
// 消费跟不上时订阅被断开（Err 为 *bus.ErrSlow）。ctx 结束自动注销。
func (a *App) Subscribe(ctx context.Context, session string, kinds ...string) *EventSub {
	sctx, cancel := context.WithCancel(ctx)
	s := &EventSub{ch: make(chan wire.Envelope, 64), stop: cancel}
	src := a.o.Bus.Subscribe(sctx, bus.Filter{Session: session, Tree: session != ""})
	tr := &Conn{a: a}
	want := func(k string) bool { return len(kinds) == 0 || slices.Contains(kinds, k) }
	go func() {
		defer close(s.ch)
		defer src.Close()
		defer guard.Recover("appapi in-process subscription", nil)
		for {
			select {
			case <-sctx.Done():
				s.mu.Lock()
				s.err = ctx.Err()
				s.mu.Unlock()
				return
			case e, ok := <-src.C():
				if !ok {
					s.mu.Lock()
					s.err = src.Err()
					if errors.Is(s.err, bus.ErrClosed) && ctx.Err() != nil {
						s.err = ctx.Err()
					}
					s.mu.Unlock()
					return
				}
				var out []wire.Envelope
				if ev, ok := e.Data.(extEvent); ok {
					if !a.extKindOK(ev.Kind) { // 与连接同一条校验（EmitOn 发出的未登记命名空间）
						continue
					}
					out = []wire.Envelope{tr.envelope(e.Session, ev.Kind, 0, 0, ev.Payload)}
				} else {
					out = tr.translate(e)
				}
				for _, w := range out {
					if !want(w.Kind) {
						continue
					}
					select {
					case s.ch <- w:
					case <-sctx.Done():
						return
					}
				}
			}
		}
	}()
	return s
}
