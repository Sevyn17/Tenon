package skills

// 单个技能的启停记录（Mortise 客户端改进 M11，配置T18；docs/50 5.1「全局一份定义 + 按范围记录启用」）。
//
// 记录放在用户级目录的 skills.toml（appid.Home()，如 ~/.<app>/skills.toml），不写进项目仓库：
//
//	[global]
//	disabled = ["pdf"]
//
//	[workspaces.'c:\users\me\proj']
//	enabled = ["pdf"]     # 本工作区覆盖：全局停用、这里启用
//	disabled = ["deploy"] # 本工作区覆盖：这里停用
//
// 判定：工作区覆盖优先，其次全局，都没写 = 启用。工作区的键与会话库一致（规范化的绝对路径，Windows 下小写）。
// 每次判定都重读文件：新建的会话按最新记录装系统块，不用重建运行时。

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

// 启停范围（skills.setEnabled 的 scope、停用原因的 disabled_scope）。
const (
	ScopeGlobal    = "global"
	ScopeWorkspace = "workspace"
)

// StateFile 启停记录的文件名（在用户级目录下）。
const StateFile = "skills.toml"

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

// State 一个工作区看到的启停记录。Path 空 = <appid.Home()>/skills.toml（调用时取，测试改 $<前缀>_HOME 即可）。
type State struct {
	Path      string
	Workspace string // WorkspaceKey；空 = 只有全局记录
}

// stateMu 同一进程里读改写串行（多个工作区的 State 共用一个文件）。
var stateMu sync.Mutex

type stateList struct {
	Enabled  []string `toml:"enabled,omitempty"`
	Disabled []string `toml:"disabled,omitempty"`
}

type stateFile struct {
	Global     stateList            `toml:"global"`
	Workspaces map[string]stateList `toml:"workspaces,omitempty"`
}

// Status 一个技能的有效状态。Scope 是让它停用的那条记录的范围（启用时为空）。
type Status struct {
	Enabled bool
	Scope   string
}

// Reason 停用原因（给人看）；启用时为空。
func (st Status) Reason() string {
	switch {
	case st.Enabled:
		return ""
	case st.Scope == ScopeWorkspace:
		return "已在本工作区停用"
	}
	return "已全局停用"
}

func (s *State) path() string {
	if s.Path != "" {
		return s.Path
	}
	return appid.UserPath(StateFile) // 宿主可改位置（apphost.Paths.Skills）
}

func (s *State) read() (stateFile, error) {
	var f stateFile
	p := s.path()
	if p == "" {
		return f, nil
	}
	if _, err := toml.DecodeFile(p, &f); err != nil && !os.IsNotExist(err) {
		return f, fmt.Errorf("读 %s：%w", p, err)
	}
	return f, nil
}

func statusIn(f stateFile, ws, name string) Status {
	if w, ok := f.Workspaces[ws]; ok && ws != "" {
		if slices.Contains(w.Disabled, name) {
			return Status{Scope: ScopeWorkspace}
		}
		if slices.Contains(w.Enabled, name) {
			return Status{Enabled: true}
		}
	}
	if slices.Contains(f.Global.Disabled, name) {
		return Status{Scope: ScopeGlobal}
	}
	return Status{Enabled: true}
}

// Statuses 一次读文件，给出这些技能的有效状态。nil 接收者或读失败时全部启用（读失败同时返回错误）。
func (s *State) Statuses(names []string) (map[string]Status, error) {
	out := make(map[string]Status, len(names))
	var f stateFile
	var err error
	if s != nil {
		stateMu.Lock()
		f, err = s.read()
		stateMu.Unlock()
		if err != nil {
			f = stateFile{}
		}
	}
	for _, n := range names {
		out[n] = statusIn(f, s.workspace(), n)
	}
	return out, err
}

func (s *State) workspace() string {
	if s == nil {
		return ""
	}
	return s.Workspace
}

// Set 改一条记录并写回。scope 为 workspace 时：与全局结果相同就删掉本工作区的覆盖，不同才记覆盖。
func (s *State) Set(name string, enabled bool, scope string) error {
	if s == nil {
		return fmt.Errorf("没有启停记录")
	}
	switch scope {
	case ScopeGlobal:
	case ScopeWorkspace:
		if s.Workspace == "" {
			return fmt.Errorf("没有工作区，不能按工作区记录")
		}
	default:
		return fmt.Errorf("scope 只能是 %s / %s，得到 %q", ScopeGlobal, ScopeWorkspace, scope)
	}
	p := s.path()
	if p == "" {
		return fmt.Errorf("取不到用户目录，无法保存技能启停记录")
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	if scope == ScopeGlobal {
		f.Global.Disabled = toggle(f.Global.Disabled, name, !enabled)
	} else {
		w := f.Workspaces[s.Workspace]
		w.Enabled, w.Disabled = toggle(w.Enabled, name, false), toggle(w.Disabled, name, false)
		if inherit := !slices.Contains(f.Global.Disabled, name); inherit != enabled {
			if enabled {
				w.Enabled = toggle(w.Enabled, name, true)
			} else {
				w.Disabled = toggle(w.Disabled, name, true)
			}
		}
		if f.Workspaces == nil {
			f.Workspaces = map[string]stateList{}
		}
		if len(w.Enabled) == 0 && len(w.Disabled) == 0 {
			delete(f.Workspaces, s.Workspace)
		} else {
			f.Workspaces[s.Workspace] = w
		}
	}
	return writeState(p, f)
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

func writeState(p string, f stateFile) error {
	var b strings.Builder
	b.WriteString("# 技能启停记录（skills.setEnabled 维护）：[global] 全局停用；[workspaces.'<工作区>'] 按工作区覆盖。\n")
	if err := toml.NewEncoder(&b).Encode(f); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return wsfs.WriteAtomic(p, []byte(b.String()))
}
