// Package appid 应用身份：程序名、数据目录名、环境变量前缀（2026-11-09）。
//
// 框架只是框架，这些名字由应用决定。缺省是 tenon / .tenon / TENON；应用在装配前调一次 Set
// （SDK 里是 tenon.SetAppID），之后框架各处按它取用户级目录、项目目录、环境变量名和提示里的命令名。
//
// 叶子包：不 import 本模块的任何包。进程级设置：一个进程只跑一个应用，Set 应在 tenon.New / apphost.Start 之前调用。
//
// 不随应用变的名字：插件包清单 tenon-plugin.toml、${TENON_PLUGIN_DIR}、交给钩子脚本的 TENON_HOOK_EVENT /
// TENON_SESSION_ID。它们是框架和第三方插件、脚本之间的协议，所有基于框架的应用都一样，插件写一次到处能用。
package appid

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// ID 应用身份。零值字段取缺省。
type ID struct {
	// Name 程序名：提示文字里的命令（"<Name> trust"）、协议 hello 的服务端名、MCP clientInfo、临时目录名。缺省 "tenon"。
	Name string
	// Dir 数据目录名：用户级 ~/<Dir>，项目级 <工作目录>/<Dir>。缺省 ".tenon"。
	Dir string
	// Env 环境变量前缀（不带下划线）：<Env>_HOME、<Env>_API_KEY 等。缺省 "TENON"。
	Env string
}

// Default 缺省身份。
var Default = ID{Name: "tenon", Dir: ".tenon", Env: "TENON"}

var cur atomic.Pointer[ID]

// Set 设置本进程的应用身份；空字段取缺省。
func Set(id ID) {
	id = id.withDefaults()
	cur.Store(&id)
}

// Get 当前应用身份（没 Set 过时为 Default）。
func Get() ID {
	if p := cur.Load(); p != nil {
		return *p
	}
	return Default
}

func (id ID) withDefaults() ID {
	if id.Name == "" {
		id.Name = Default.Name
	}
	if id.Dir == "" {
		id.Dir = Default.Dir
	}
	id.Env = strings.TrimSuffix(id.Env, "_")
	if id.Env == "" {
		id.Env = Default.Env
	}
	return id
}

// EnvName 环境变量名：EnvName("HOME") = "TENON_HOME"。
func (id ID) EnvName(suffix string) string { return id.withDefaults().Env + "_" + suffix }

// Getenv 读 <Env>_<suffix>。
func (id ID) Getenv(suffix string) string { return os.Getenv(id.EnvName(suffix)) }

// Command 提示文字里的命令：Command("trust") = "tenon trust"。
func (id ID) Command(sub string) string { return id.withDefaults().Name + " " + sub }

// Home 用户级目录：$<Env>_HOME，缺省 ~/<Dir>；取不到返回空。
func (id ID) Home() string {
	id = id.withDefaults()
	if h := id.Getenv("HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, id.Dir)
}

// Project 项目级目录：<root>/<Dir>。
func (id ID) Project(root string) string { return filepath.Join(root, id.withDefaults().Dir) }

// 以下按当前身份取，各模块直接用。

// Name 当前程序名。
func Name() string { return Get().Name }

// DirName 当前数据目录名。
func DirName() string { return Get().Dir }

// EnvName 当前前缀下的环境变量名。
func EnvName(suffix string) string { return Get().EnvName(suffix) }

// Getenv 读当前前缀下的环境变量。
func Getenv(suffix string) string { return Get().Getenv(suffix) }

// Command 当前程序名下的命令。
func Command(sub string) string { return Get().Command(sub) }

// Home 当前用户级目录。
func Home() string { return Get().Home() }

// Project 当前项目级目录。
func Project(root string) string { return Get().Project(root) }

// 用户级目录下由框架读写的文件与目录（UserPath 的 name）。宿主可经 SetUserPaths 改到别处。
const (
	FileTrust          = "trust.toml"           // 项目信任与档位放宽确认
	FileRecent         = "workspaces.json"      // 最近工作区
	FileSkills         = "skills.toml"          // 技能启停记录
	FileMCPEnabled     = "mcp-enabled.toml"     // MCP 服务启用记录
	FilePluginsEnabled = "plugins-enabled.toml" // 插件包启停记录
	FilePermissions    = "permissions.toml"     // 用户级权限配置
	DirModes           = "modes"                // 用户级自定义档位目录
)

var userPaths atomic.Pointer[map[string]string]

// SetUserPaths 设置本进程的用户级路径覆盖：键是上面的 File* / Dir* 名，值是绝对路径；空值与 nil = 用缺省
// （<用户级目录>/<名>）。整体替换上一次的设置。进程级，与 Set 一样应在装配前调用。
func SetUserPaths(m map[string]string) {
	c := map[string]string{}
	for k, v := range m {
		if v != "" {
			c[k] = v
		}
	}
	userPaths.Store(&c)
}

// UserPathIn 用户级文件 name 的位置：有覆盖用覆盖，否则 <home>/<name>；都没有（home 为空）返回空。
func UserPathIn(home, name string) string {
	if p := userPaths.Load(); p != nil {
		if v := (*p)[name]; v != "" {
			return v
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, name)
}

// UserPath 当前用户级目录下 name 的位置（见 UserPathIn）。
func UserPath(name string) string { return UserPathIn(Home(), name) }
