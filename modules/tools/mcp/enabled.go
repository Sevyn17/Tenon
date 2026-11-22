package mcp

// 按工作区启用 MCP 服务的记录（Mortise 客户端改进 M11，配置T14；docs/50 5.1「使用分开」）。
//
// 记录放在用户级目录的 mcp-enabled.toml（appid.Home()，如 ~/.<app>/mcp-enabled.toml），不写进项目仓库，
// 结构与技能的 skills.toml 相同（管理页、协议两边按同一套规则处理）：
//
//	[global]
//	disabled = ["github"]  # 全局停用
//
//	[workspaces.'c:\users\me\proj']
//	enabled = ["github"]   # 本工作区覆盖：全局停用、这里启用
//	disabled = ["fs"]      # 本工作区覆盖：这里停用
//
// 判定：工作区覆盖优先，其次全局，都没写 = 启用（定义了就能用，项目受信后项目里的服务直接生效）。
// 工作区的键与会话库一致（规范化的绝对路径，Windows 下小写）。运行时启动（打开工作区、workspace.reload）时读一次：
// 改了记录要重建运行时才生效；全局范围的服务实例由资源管理器保留，重建时不重启。
//
// 不放进 mcp.toml：那是定义（可能拷给别人、由管理页整体改写），启用记录带本机路径、按工作区记，两者生命周期不同。

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/modules/internal/wsfs"
	"github.com/Sevyn17/tenon/runtime/appid"
)

// 启用记录的范围（Enabled.Set 的 scope、EnabledStatus.Scope）。
const (
	EnableGlobal    = "global"
	EnableWorkspace = "workspace"
)

// EnabledFile 启用记录的文件名（在用户级目录下）。
const EnabledFile = "mcp-enabled.toml"

// WorkspaceKey 工作区范围记录的键：规范化的绝对路径（Windows 下转小写），与 apphost.WorkspaceKey 同规则。
func WorkspaceKey(dir string) string {
	if a, err := filepath.Abs(dir); err == nil {
		dir = a
	}
	dir = filepath.Clean(dir)
	if os.PathSeparator == '\\' {
		dir = strings.ToLower(dir)
	}
	return dir
}

// Enabled 一个工作区看到的启用记录。Path 空 = <appid.Home()>/mcp-enabled.toml（调用时取）。
type Enabled struct {
	Path      string
	Workspace string // WorkspaceKey；空 = 只有全局记录
}

// EnabledStatus 一个服务的有效启用状态。Scope 是让它停用的那条记录的范围（启用时为空）。
type EnabledStatus struct {
	Enabled bool
	Scope   string
}

var enabledMu sync.Mutex // 同一进程里读改写串行（多个工作区共用一个文件）

type enabledList struct {
	Enabled  []string `toml:"enabled,omitempty"`
	Disabled []string `toml:"disabled,omitempty"`
}

type enabledFile struct {
	Global     enabledList            `toml:"global"`
	Workspaces map[string]enabledList `toml:"workspaces,omitempty"`
}

func (e *Enabled) path() string {
	if e.Path != "" {
		return e.Path
	}
	return appid.UserPath(EnabledFile) // 宿主可改位置（apphost.Paths.MCPEnabled）
}

func (e *Enabled) read() (enabledFile, error) {
	var f enabledFile
	p := e.path()
	if p == "" {
		return f, nil
	}
	if _, err := toml.DecodeFile(p, &f); err != nil && !os.IsNotExist(err) {
		return enabledFile{}, fmt.Errorf("读 %s：%w", p, err)
	}
	return f, nil
}

func enabledIn(f enabledFile, ws, name string) EnabledStatus {
	if w, ok := f.Workspaces[ws]; ok && ws != "" {
		if slices.Contains(w.Disabled, name) {
			return EnabledStatus{Scope: EnableWorkspace}
		}
		if slices.Contains(w.Enabled, name) {
			return EnabledStatus{Enabled: true}
		}
	}
	if slices.Contains(f.Global.Disabled, name) {
		return EnabledStatus{Scope: EnableGlobal}
	}
	return EnabledStatus{Enabled: true}
}

// Statuses 一次读文件，给出这些服务的有效状态。nil 接收者或读失败时全部启用（读失败同时返回错误）。
func (e *Enabled) Statuses(names []string) (map[string]EnabledStatus, error) {
	out := make(map[string]EnabledStatus, len(names))
	var f enabledFile
	var err error
	ws := ""
	if e != nil {
		ws = e.Workspace
		enabledMu.Lock()
		f, err = e.read()
		enabledMu.Unlock()
	}
	for _, n := range names {
		out[n] = enabledIn(f, ws, n)
	}
	return out, err
}

// Set 改一条记录并写回。scope 为 workspace 时：与全局结果相同就删掉本工作区的覆盖，不同才记覆盖。
func (e *Enabled) Set(name string, enabled bool, scope string) error {
	if e == nil {
		return fmt.Errorf("没有 MCP 启用记录")
	}
	switch scope {
	case EnableGlobal:
	case EnableWorkspace:
		if e.Workspace == "" {
			return fmt.Errorf("没有工作区，不能按工作区记录")
		}
	default:
		return fmt.Errorf("scope 只能是 %s / %s，得到 %q", EnableGlobal, EnableWorkspace, scope)
	}
	p := e.path()
	if p == "" {
		return fmt.Errorf("取不到用户目录，无法保存 MCP 启用记录")
	}
	enabledMu.Lock()
	defer enabledMu.Unlock()
	f, err := e.read()
	if err != nil {
		return err
	}
	if scope == EnableGlobal {
		f.Global.Disabled = toggle(f.Global.Disabled, name, !enabled)
	} else {
		w := f.Workspaces[e.Workspace]
		w.Enabled, w.Disabled = toggle(w.Enabled, name, false), toggle(w.Disabled, name, false)
		if inherit := !slices.Contains(f.Global.Disabled, name); inherit != enabled {
			if enabled {
				w.Enabled = toggle(w.Enabled, name, true)
			} else {
				w.Disabled = toggle(w.Disabled, name, true)
			}
		}
		if f.Workspaces == nil {
			f.Workspaces = map[string]enabledList{}
		}
		if len(w.Enabled) == 0 && len(w.Disabled) == 0 {
			delete(f.Workspaces, e.Workspace)
		} else {
			f.Workspaces[e.Workspace] = w
		}
	}
	var b strings.Builder
	b.WriteString("# MCP 服务启用记录（按工作区启用；mcp 管理页维护）：[global] 全局停用；[workspaces.'<工作区>'] 按工作区覆盖。\n")
	if err := toml.NewEncoder(&b).Encode(f); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return wsfs.WriteAtomic(p, []byte(b.String()))
}

// toggle 有序去重地加入或去掉 name。
func toggle(xs []string, name string, in bool) []string {
	xs = slices.DeleteFunc(slices.Clone(xs), func(x string) bool { return x == name })
	if in {
		xs = append(xs, name)
		sort.Strings(xs)
	}
	return xs
}
