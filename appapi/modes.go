package appapi

// 插件能力的小接口：应用用插件服务对象实现后经 Options 注入（appapi 不 import 插件包）。

import (
	"context"
	"encoding/json"

	"ybase/appapi/wire"
	"ybase/runtime/component"
	"ybase/runtime/reqlog"
	"ybase/runtime/session"
)

// ModeInfo mode.list 的一行。
type ModeInfo struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Builtin bool     `json:"builtin"`
	Project bool     `json:"project"`           // 来自项目 .ybase/modes
	Widened []string `json:"widened,omitempty"` // 相对基础档位放宽的权限项
}

// ModeState 会话当前档位。
type ModeState struct {
	Mode    string `json:"mode"`
	Label   string `json:"label"`
	Sandbox string `json:"sandbox"`
}

// ModeItem mode.show 的一行：权限项在档位下的取值与来源。
type ModeItem struct {
	Item    string `json:"item"`
	Title   string `json:"title"`
	Value   string `json:"value"`
	Source  string `json:"source"`
	Bounded bool   `json:"bounded"` // 匹配是否构成边界（shell 命令匹配不构成）
}

// Modes 权限档位（permission 插件提供，应用适配）。
type Modes interface {
	List() []ModeInfo
	Get(session string) ModeState
	// Set 切换档位（只改内存，持久化由 App 做）。项目档位放宽了权限且未确认过时，
	// confirm=false 返回错误码 need_confirm；confirm=true 记下确认后切换。档位不存在返回 not_found。
	Set(session, mode string, confirm bool) (ModeState, error)
	// Show 档位的完整取值表；mode 为空取 session 的当前档位。
	Show(session, mode string) ([]ModeItem, error)
	// Notice 档位文件有变化时的提示（每回合开始调用）；无变化返回空。
	Notice(session string) string
	// SessionRules 会话已有的允许规则。
	SessionRules(session string) []string
	// SessionAllow “本次会话内都允许”会生成的规则，以及不生成规则的启动器类命令。
	SessionAllow(tool string, args json.RawMessage) (rules, blocked []string)
	// AllowForSession 按调用生成并追加会话规则；返回新生成的规则与会话全部规则（供持久化）。
	AllowForSession(session, tool string, args json.RawMessage) (added, all []string)
	// ExtKeys 会话头 Ext 里存档位与会话规则的键。
	ExtKeys() (mode, rules string)
}

// Compactor 手动压缩（compact-basic 的 *Compactor 直接满足）。
type Compactor interface {
	// Plan 按保留尾部的规则规划压缩；保留量不够切返回 nil 视图。
	Plan(ctx context.Context, sessionID string, core *session.Core, force bool) (*reqlog.CompactionView, map[string]any, error)
	// At 压缩到指定位置 upto。
	At(ctx context.Context, sessionID string, core *session.Core, upto int) (*reqlog.CompactionView, map[string]any, error)
}

// Profiles 可写的 profile 层（plugins.set）：boot 适配，appapi 不知道文件在哪。
type Profiles interface {
	// Scopes 可写的层（user 在前）。
	Scopes() []wire.ProfileScope
	// Edit 改写一层里某插件的开关与配置，返回新 revision；revision 不符返回 component.ErrRevision。
	Edit(scope string, e component.PluginEdit, ifRevision *string) (string, error)
	// Reload 按磁盘上的文件重新合并（重启后的样子）。
	Reload() (*component.Profile, error)
	// Denied 项目层不能设的键。
	Denied() map[string][]string
}

// Files 列工作区文件（fs 插件的 ListFiles 适配）：遵守 .gitignore，只读，不出工作目录。
type Files func(ctx context.Context, query string, limit int) ([]wire.FileInfo, bool, error)

// topOf 顶层会话 id（子会话 id 形如 "<顶层>/<子>"）；会话规则跟随顶层会话持久化。
func topOf(id string) string {
	for i := 0; i < len(id); i++ {
		if id[i] == '/' {
			return id[:i]
		}
	}
	return id
}
