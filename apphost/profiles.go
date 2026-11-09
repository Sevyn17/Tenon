package apphost

// plugins.set 的文件侧（appapi.Profiles）：用户层 ~/.tenon/profile.toml、项目层 <dir>/.tenon/profile.toml。
// 只改这两层；-profile 指定的文件与插件包不动（它们在叠加顺序里各有位置，界面改不到时 pending 会显示出来）。

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
)

// Profiles 实现 appapi.Profiles。
type Profiles struct {
	Base    string // 产品内置 profile 原文（空 = DefaultProfileTOML）
	Extra   string // -profile
	Root    string
	Trusted bool
}

func (p Profiles) path(scope string) (string, error) {
	switch scope {
	case "user":
		h := Home()
		if h == "" {
			return "", fmt.Errorf("取不到用户目录（可设 %s）", appid.EnvName("HOME"))
		}
		return filepath.Join(h, "profile.toml"), nil
	case "project":
		return filepath.Join(appid.Project(p.Root), "profile.toml"), nil
	}
	return "", fmt.Errorf("未知 scope %q", scope)
}

// Scopes 两层：项目未受信时项目层只读（写了也不会被读）。
func (p Profiles) Scopes() []wire.ProfileScope {
	var out []wire.ProfileScope
	for _, s := range []string{"user", "project"} {
		path, err := p.path(s)
		if err != nil {
			out = append(out, wire.ProfileScope{Scope: s, Reason: err.Error()})
			continue
		}
		rev, err := component.Revision(path)
		sc := wire.ProfileScope{Scope: s, Path: path, Revision: rev, Writable: err == nil}
		if err != nil {
			sc.Reason = err.Error()
		}
		if s == "project" && !p.Trusted {
			sc.Writable, sc.Reason = false, "项目未受信，不读项目 profile（"+appid.Command("trust")+" 可信任）"
		}
		out = append(out, sc)
	}
	return out
}

func (p Profiles) Edit(scope string, e component.PluginEdit, ifRevision *string) (string, error) {
	path, err := p.path(scope)
	if err != nil {
		return "", err
	}
	if e.Enabled != nil && !*e.Enabled && providesDB(e.Name) { // 存储不锁定是哪个插件，但不能一个都不剩
		cur, err := p.Reload()
		if err != nil {
			return "", err
		}
		left := false
		for _, st := range cur.States() {
			left = left || (st.On && st.Name != e.Name && providesDB(st.Name))
		}
		if !left {
			return "", appapi.Errorf(wire.CodeInvalid, "关掉 %s 就没有存储组件了：先开另一个提供 %s 的插件", e.Name, assemble.ServiceDB)
		}
	}
	return component.EditProfile(path, e, ifRevision)
}

// providesDB 插件是否提供会话存储（service:db）。
func providesDB(name string) bool {
	m, ok := component.MetaOf(name)
	return ok && slices.Contains(m.Provides, assemble.ServiceDB)
}

// Reload 与启动时同一套叠加（LoadAll）。
func (p Profiles) Reload() (*component.Profile, error) {
	q, _, _, err := LoadAll(p.Base, p.Extra, p.Root, p.Trusted)
	return q, err
}

func (p Profiles) Denied() map[string][]string {
	out := map[string][]string{}
	for k, v := range projectDenied {
		out[k] = append([]string(nil), v...)
	}
	return out
}
