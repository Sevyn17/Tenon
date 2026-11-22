package apphost

// 工作区管理器（appapi.Workspaces）：一个进程里一次只开一个工作区，切换 = 关掉旧的运行时，在新目录上重新
// Start + BuildApp，换上新的应用接口层。插件都按进程作用域读工作目录（fs、shell、权限、沙箱、提示词、技能、钩子、MCP），所以整套运行时跟着工作区走，插件不用改。会话库是用户级的（产品的存储插件缺省放在用户级目录），
// 各工作区共用，会话行记着所属工作区（Host.Workspace），列表只列当前工作区的。
//
// 传输层（网页服务 / 桌面端）通过 Current 取当前的 App；OnSwitch 在换上新 App 之后调用，传输层据此断开旧连接，
// 客户端重连后 hello 报新的 workdir。最近打开的目录记在用户级目录（Home）的 workspaces.json。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/resmgr"
)

// MaxRecentWorkspaces 最近列表最多记几个目录。
const MaxRecentWorkspaces = 20

// WorkspaceOptions 管理器参数：每次打开工作区都用同一份启动参数，只换 Workdir。
type WorkspaceOptions struct {
	Flags      Flags
	SleepAfter time.Duration
	// Upload 同 AppOptions.Upload。
	Upload bool
	// OnSwitch 新工作区的 App 换上之后调用（传输层断开旧连接、改用新 App）；可空。
	OnSwitch func(api *appapi.App)
	// RecentPath 最近列表文件；空 = 用户级目录（Home）的 workspaces.json（取不到用户目录时不记）。
	RecentPath string
	// Extensions 产品扩展（appapi.Options.Extensions）；切换工作区后新 App 沿用同一组。
	Extensions []appapi.Extension
	// ResourceIdle 统一资源管理器里没有引用的实例（如 MCP 进程）多久回收；0 = resmgr.DefaultIdle（5 分钟），<0 = 立即回收。
	ResourceIdle time.Duration
	// ResourceMax 资源管理器的实例数上限；0 = 不限。
	ResourceMax int
}

// Workspace 一个已打开的工作区：运行时 + 应用接口层。
type Workspace struct {
	Runtime *Runtime
	Built   *Built
}

// WorkspaceManager 实现 appapi.Workspaces。
type WorkspaceManager struct {
	o WorkspaceOptions

	mu      sync.Mutex // 串行化 Open
	cur     *Workspace
	curRoot string
	view    sync.RWMutex // 保护 cur 的读（Current）
	// res 统一资源管理器（docs/50 5.1）：跨工作区存活，每次打开工作区 Bind 一份视图注入给组件（service:resources）。
	res *resmgr.Manager
}

// OpenWorkspaceManager 打开启动时的工作区（o.Flags.Workdir）。失败时没有任何资源残留。
func OpenWorkspaceManager(o WorkspaceOptions) (*WorkspaceManager, error) {
	m := &WorkspaceManager{o: o, res: resmgr.New(resmgr.Options{Idle: o.ResourceIdle, Max: o.ResourceMax})}
	ws, err := m.start(o.Flags.Workdir)
	if err != nil {
		m.res.Close()
		return nil, err
	}
	m.cur, m.curRoot = ws, ws.Runtime.Root
	m.remember(ws.Runtime.Root)
	return m, nil
}

// Current 当前工作区。
func (m *WorkspaceManager) Current() *Workspace {
	m.view.RLock()
	defer m.view.RUnlock()
	return m.cur
}

// Close 关掉当前工作区与资源管理器里的全部实例（进程退出时）。
func (m *WorkspaceManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.view.Lock()
	ws := m.cur
	m.cur = nil
	m.view.Unlock()
	if ws != nil {
		ws.Runtime.Close()
	}
	if m.res != nil {
		m.res.Close()
	}
}

// hold 暂缓资源回收（见 resmgr.Manager.Hold）；没有管理器时什么也不做。
func (m *WorkspaceManager) hold() func() {
	if m.res == nil {
		return func() {}
	}
	return m.res.Hold()
}

// Resources 统一资源管理器：列出在跑的实例（管理页、协议用）。管理器关闭后仍可调 List（为空）。
func (m *WorkspaceManager) Resources() *resmgr.Manager { return m.res }

// start 在 dir 上装配并接线一套运行时，跑 ready 点位，把装配提示发成 notice。
func (m *WorkspaceManager) start(dir string) (*Workspace, error) { return m.startWith(dir, nil) }

// startWith 见 start；pre 非空时用已经读好的信任与 profile（Reload 回落用）。
func (m *WorkspaceManager) startWith(dir string, pre *loaded) (*Workspace, error) {
	f := m.o.Flags
	f.Workdir = dir
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// 本工作区在资源管理器里的视图：运行时关掉时交还它取过的全部引用（Runtime.Close）。
	var bound *resmgr.Bound
	if m.res != nil {
		bound = m.res.Bind(resmgr.Owner{Workspace: WorkspaceKey(abs)})
	}
	// 网页端、桌面端都没有终端可问：无信任记录的项目按不受信（同启动时）；界面走协议（UI 为 nil）。
	rt, err := start(StartOptions{Flags: f, SleepAfter: m.o.SleepAfter,
		App:       AppOptions{MaxSteps: f.MaxSteps, Upload: m.o.Upload, Workspaces: m, Extensions: m.o.Extensions},
		resources: bound}, pre)
	if err != nil {
		if bound != nil {
			bound.Close()
		}
		return nil, err
	}
	built, err := rt.BuildApp()
	if err != nil {
		rt.Close()
		return nil, err
	}
	// 这时还没有连接：提示记在 App 上，客户端连上、hello 之后补发（客户端改进 M10 T19，能力 notices）。
	for _, e := range built.Ready(context.Background()) {
		built.API.NoticeKey("", wire.NoticeError, "ready", appapi.NoticeKeyReadyFailed, map[string]string{"error": e.Error()}, "ready: "+e.Error())
	}
	for _, n := range rt.Notes {
		built.API.NoticeFrom("", wire.NoticeInfo, "startup", n)
	}
	return &Workspace{Runtime: rt, Built: built}, nil
}

// Open 切到 path：校验目录 → 关掉当前工作区 → 在新目录上装配 → 换上并通知传输层。
// 会话库是用户级的、所有工作区共用：先关旧的再开新的，同一个库任何时候只有一套运行时在写。
// 新目录装配失败时重新打开原来的目录（也通知传输层，旧 App 已经关了）。和当前工作区是同一个目录时什么也不做。
// 发起切换的 workspace.open 命令跑在旧 App 上：旧运行时关了以后它照样能把结果交回去（只是返回值，不碰存储）。
func (m *WorkspaceManager) Open(_ context.Context, path string) (wire.WorkspaceInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return wire.WorkspaceInfo{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return wire.WorkspaceInfo{}, fmt.Errorf("directory does not exist: %s", abs)
	}
	if !st.IsDir() {
		return wire.WorkspaceInfo{}, fmt.Errorf("not a directory: %s", abs)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && NormPath(m.curRoot) == NormPath(abs) {
		return m.info(m.cur.Runtime.Root, m.cur.Runtime.Trusted, true), nil
	}
	oldRoot := m.curRoot
	// 旧运行时交还引用到新运行时取到之间不回收：全局范围的实例（如 MCP 进程）留给新工作区接着用。
	defer m.hold()()
	m.swap(nil, "")
	ws, err := m.start(abs)
	if err != nil {
		back, err2 := m.start(oldRoot)
		if err2 != nil {
			return wire.WorkspaceInfo{}, fmt.Errorf("%v; reverting to the previous workspace also failed: %v", err, err2)
		}
		m.swap(back, back.Runtime.Root)
		return wire.WorkspaceInfo{}, err
	}
	m.swap(ws, ws.Runtime.Root)
	m.remember(ws.Runtime.Root)
	return m.info(ws.Runtime.Root, ws.Runtime.Trusted, true), nil
}

// Reload 在当前目录上重建运行时（appapi.Reloader，workspace.reload）：重读信任、各层 profile、插件包，插件 Setup
// 读的外部配置（自定义模型、登录令牌等）也随之重读。和 Open 一样先关旧的再开新的（会话库同时只能有一套运行时在写），
// 换上后通知传输层，客户端重连后重新 hello。
// 新配置起不来时用上一次成功读到的信任与 profile 再开一次，返回 *appapi.ReloadReverted（运行时照常可用）；
// 这也起不来就没有当前工作区，返回错误（只能重启进程）。有回合在跑时由调用方（appapi）先拦下或先取消。
func (m *WorkspaceManager) Reload(_ context.Context) (wire.WorkspaceInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return wire.WorkspaceInfo{}, ErrNoWorkspace
	}
	root, prev := m.curRoot, m.cur.Runtime.loaded
	defer m.hold()() // 同 Open：重建期间全局实例不回收
	m.swap(nil, "")
	ws, err := m.start(root)
	if err != nil {
		if prev == nil {
			return wire.WorkspaceInfo{}, err
		}
		back, err2 := m.startWith(root, prev)
		if err2 != nil {
			return wire.WorkspaceInfo{}, fmt.Errorf("%v; reverting to the previous config also failed: %v", err, err2)
		}
		back.Built.API.NoticeKey("", wire.NoticeWarn, "workspace", appapi.NoticeKeyReloadReverted, map[string]string{"error": err.Error()},
			"runtime rebuild failed, reverted to the previous config: "+err.Error())
		m.swap(back, back.Runtime.Root)
		return wire.WorkspaceInfo{}, &appapi.ReloadReverted{Err: err}
	}
	m.swap(ws, ws.Runtime.Root)
	return m.info(ws.Runtime.Root, ws.Runtime.Trusted, true), nil
}

// swap 关掉当前工作区（若有），换上 ws（nil = 暂时没有）；换上新的时通知传输层。调用方持有 m.mu。
func (m *WorkspaceManager) swap(ws *Workspace, root string) {
	m.view.Lock()
	old := m.cur
	m.cur = ws
	if ws != nil {
		m.curRoot = root
	}
	m.view.Unlock()
	if old != nil && old != ws {
		old.Runtime.Close()
	}
	if ws != nil && m.o.OnSwitch != nil {
		m.o.OnSwitch(ws.Built.API)
	}
}

// List 当前工作区在前，其余按最近打开倒序；已经不存在的目录标 missing。
func (m *WorkspaceManager) List() []wire.WorkspaceInfo {
	cur := m.Current()
	var out []wire.WorkspaceInfo
	curKey := ""
	if cur != nil {
		curKey = NormPath(cur.Runtime.Root)
		out = append(out, m.info(cur.Runtime.Root, cur.Runtime.Trusted, true))
	}
	trust, _ := LoadTrust()
	for _, r := range m.recent() {
		if NormPath(r.Path) == curKey {
			out[0].OpenedAt = r.At
			continue
		}
		wi := m.info(r.Path, LookupTrust(trust, ProjectKey(r.Path)) == "trusted", false)
		wi.OpenedAt = r.At
		if st, err := os.Stat(r.Path); err != nil || !st.IsDir() {
			wi.Missing = true
		}
		out = append(out, wi)
	}
	return out
}

func (m *WorkspaceManager) info(path string, trusted, current bool) wire.WorkspaceInfo {
	return wire.WorkspaceInfo{Path: path, Name: filepath.Base(path), Trusted: trusted, Current: current}
}

// —— 最近列表 ——

type recentEntry struct {
	Path string `json:"path"`
	At   int64  `json:"at"` // 毫秒
}

func (m *WorkspaceManager) recentPath() string { return recentFile(m.o.RecentPath) }

// recentFile 最近列表文件：path 非空就用它，否则是用户级目录（Home）的 workspaces.json；取不到用户目录时为空。
func recentFile(path string) string {
	if path != "" {
		return path
	}
	return appid.UserPath(appid.FileRecent)
}

// RecentWorkspaces 最近打开的工作区目录，最近的在前（与 WorkspaceManager.List 同一份记录）。
// path 同 WorkspaceOptions.RecentPath（空 = 用户级目录的 workspaces.json）。文件不存在或读不了时返回 nil；
// 不检查目录是否还在，调用方自己判断（桌面端不带 -dir 启动时取第一个还在的目录）。
func RecentWorkspaces(path string) []string {
	var out []string
	for _, r := range readRecent(recentFile(path)) {
		out = append(out, r.Path)
	}
	return out
}

func (m *WorkspaceManager) recent() []recentEntry { return readRecent(m.recentPath()) }

func readRecent(p string) []recentEntry {
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out []recentEntry
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// remember 把 root 记到最近列表最前面（同一目录去重）；写失败不影响切换。
func (m *WorkspaceManager) remember(root string) {
	p := m.recentPath()
	if p == "" {
		return
	}
	key := NormPath(root)
	list := []recentEntry{{Path: root, At: time.Now().UnixMilli()}}
	for _, r := range m.recent() {
		if NormPath(r.Path) != key {
			list = append(list, r)
		}
	}
	if len(list) > MaxRecentWorkspaces {
		list = list[:MaxRecentWorkspaces]
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
	}
}

// ErrNoWorkspace 管理器已关闭。
var ErrNoWorkspace = errors.New("no workspace is open")
