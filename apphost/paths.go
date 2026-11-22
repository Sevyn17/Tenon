package apphost

// 用户级文件位置与「受信项目也不能设的键」由宿主注入（docs 底座边界 T4）。
// 都是进程级设置，与 tenon.SetAppID 一样在装配前调用；不调用时一切照旧：
// 文件都在用户级目录（Home）下、名字不变，禁设名单只有框架自己的 models.base_url / catalog。

import (
	"slices"
	"sort"
	"sync"

	"github.com/Sevyn17/tenon/runtime/appid"
)

// Paths 用户级文件与目录的位置。空字段 = 缺省 <Home>/<缺省名>（trust.toml、workspaces.json、skills.toml、
// mcp-enabled.toml、plugins-enabled.toml、modes/、permissions.toml）。值应是绝对路径。
type Paths struct {
	Trust          string // 项目信任与档位放宽确认
	Recent         string // 最近工作区（WorkspaceOptions.RecentPath 非空时以它为准）
	Skills         string // 技能启停记录
	MCPEnabled     string // MCP 服务启用记录
	PluginsEnabled string // 插件包启停记录
	ModesDir       string // 用户级自定义档位目录
	Permissions    string // 用户级 permissions.toml
}

// SetPaths 设置本进程的用户级路径（整体替换上一次的设置；零值 = 全部缺省）。
func SetPaths(p Paths) {
	appid.SetUserPaths(map[string]string{
		appid.FileTrust:          p.Trust,
		appid.FileRecent:         p.Recent,
		appid.FileSkills:         p.Skills,
		appid.FileMCPEnabled:     p.MCPEnabled,
		appid.FilePluginsEnabled: p.PluginsEnabled,
		appid.DirModes:           p.ModesDir,
		appid.FilePermissions:    p.Permissions,
	})
}

// CurrentPaths 当前生效的位置（含缺省；取不到用户目录且没设覆盖的项为空）。
func CurrentPaths() Paths {
	return Paths{
		Trust:          appid.UserPath(appid.FileTrust),
		Recent:         appid.UserPath(appid.FileRecent),
		Skills:         appid.UserPath(appid.FileSkills),
		MCPEnabled:     appid.UserPath(appid.FileMCPEnabled),
		PluginsEnabled: appid.UserPath(appid.FilePluginsEnabled),
		ModesDir:       appid.UserPath(appid.DirModes),
		Permissions:    appid.UserPath(appid.FilePermissions),
	}
}

var (
	deniedMu sync.RWMutex
	// projectDenied 受信项目也不能设的键（端点 / 凭据 / 存储位置）。插件包（用户级与项目级）同样不能设，见 LoadAll。
	// 框架只登记自己的 models；产品插件（存储、网关等）经 DenyProjectKeys 自己登记。
	projectDenied = map[string][]string{
		"models": {"base_url", "catalog"},
	}
)

// DenyProjectKeys 登记插件 plugin 的配置键 keys：受信项目的 profile、插件包都不能设（读的时候剥离并提示，
// plugins.set 写项目层被拒）。用于端点、凭据、存储位置这类键。可多次调用，键去重追加；通常在插件包的 init 里调。
func DenyProjectKeys(plugin string, keys ...string) {
	if plugin == "" || len(keys) == 0 {
		return
	}
	deniedMu.Lock()
	defer deniedMu.Unlock()
	cur := projectDenied[plugin]
	for _, k := range keys {
		if k != "" && !slices.Contains(cur, k) {
			cur = append(cur, k)
		}
	}
	projectDenied[plugin] = cur
}

// deniedKeys plugin 的禁设键（副本）。
func deniedKeys(plugin string) []string {
	deniedMu.RLock()
	defer deniedMu.RUnlock()
	return append([]string(nil), projectDenied[plugin]...)
}

// deniedAll 禁设名单的副本。
func deniedAll() map[string][]string {
	deniedMu.RLock()
	defer deniedMu.RUnlock()
	out := make(map[string][]string, len(projectDenied))
	for k, v := range projectDenied {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// deniedPlugins 禁设名单的插件名（排序，提示顺序固定）。
func deniedPlugins() []string {
	deniedMu.RLock()
	defer deniedMu.RUnlock()
	out := make([]string, 0, len(projectDenied))
	for n := range projectDenied {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
