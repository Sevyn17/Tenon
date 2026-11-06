package boot

// plugins.set 的文件侧（appapi.Profiles）：用户层 ~/.ybase/profile.toml、项目层 <dir>/.ybase/profile.toml。
// 只改这两层；-profile 指定的文件与插件包不动（它们在叠加顺序里各有位置，界面改不到时 pending 会显示出来）。

import (
	"fmt"
	"path/filepath"

	"ybase/appapi/wire"
	"ybase/runtime/component"
)

// Profiles 实现 appapi.Profiles。
type Profiles struct {
	Extra   string // -profile
	Root    string
	Trusted bool
}

func (p Profiles) path(scope string) (string, error) {
	switch scope {
	case "user":
		h := Home()
		if h == "" {
			return "", fmt.Errorf("取不到用户目录（可设 YBASE_HOME）")
		}
		return filepath.Join(h, "profile.toml"), nil
	case "project":
		return filepath.Join(p.Root, ".ybase", "profile.toml"), nil
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
			sc.Writable, sc.Reason = false, "项目未受信，不读项目 profile（ybase trust 可信任）"
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
	return component.EditProfile(path, e, ifRevision)
}

// Reload 与启动时同一套叠加（LoadAll）。
func (p Profiles) Reload() (*component.Profile, error) {
	q, _, _, err := LoadAll(p.Extra, p.Root, p.Trusted)
	return q, err
}

func (p Profiles) Denied() map[string][]string {
	out := map[string][]string{}
	for k, v := range projectDenied {
		out[k] = append([]string(nil), v...)
	}
	return out
}
