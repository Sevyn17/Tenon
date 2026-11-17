package apphost

// 缺省模型由哪项设置决定（客户端改进 M3，配置T5）：models 插件只知道「配置里写了」（assemble.OriginConfig），
// 是命令行参数、环境变量还是哪一层 profile 只有宿主知道。这里按优先级从高到低逐层查 models.model / base_url
// 写在哪，细分成 flag / env / profile，OriginRef 写参数名、变量名或文件路径。models.list 的 effective 用。

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Sevyn17/tenon/adapters/profile"
	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/pkgs"
)

// effective.origin 在 apphost 下的细分值（OriginConfig 拆成这三种，取值见 wire.EffectiveModel.Origin）。
const (
	originFlag    = "flag"    // 命令行参数 -model / -base
	originEnv     = "env"     // 环境变量（<前缀>_MODEL / <前缀>_BASE_URL，入口把它们当参数缺省值）
	originProfile = "profile" // 某一层 profile（OriginRef 是文件路径；内置 profile 写「内置 profile」）
)

// refineOrigin 把 m.Origin == config 细分（其余取值不动）。ps 是这次装配用的插件包。
func refineOrigin(m *assemble.Models, f Flags, ps []*pkgs.Package, root string, trusted bool) {
	if m.Origin != assemble.OriginConfig {
		return
	}
	kind, ref := modelOrigin(f, ps, root, trusted)
	m.Origin, m.OriginRef = kind, ref
}

// modelOrigin 按 Overrides 与 LoadAll 的叠加顺序反过来查：参数 → 环境变量 → -profile → 项目 → 用户 → 插件包 → 内置。
func modelOrigin(f Flags, ps []*pkgs.Package, root string, trusted bool) (kind, ref string) {
	// 参数与环境变量：入口把 <前缀>_MODEL / _BASE_URL 当参数缺省值，值相同就算环境变量给的。
	for _, x := range []struct{ val, env, flag string }{
		{f.Model, appid.EnvName("MODEL"), "-model"},
		{f.Base, appid.EnvName("BASE_URL"), "-base"},
	} {
		if x.val == "" {
			continue
		}
		if strings.TrimSpace(os.Getenv(x.env)) == x.val {
			return originEnv, x.env
		}
		return originFlag, x.flag
	}
	sets := func(q *component.Profile, keys ...string) bool {
		if q == nil {
			return false
		}
		for _, e := range q.Plugins {
			if e.Name != "models" {
				continue
			}
			for _, k := range keys {
				if v, ok := e.Config[k]; ok && v != nil && v != "" {
					return true
				}
			}
		}
		return false
	}
	file := func(path string, keys ...string) bool {
		if path == "" {
			return false
		}
		if _, err := os.Stat(path); err != nil {
			return false
		}
		q, err := profile.Load(path)
		return err == nil && sets(q, keys...)
	}
	if file(f.Profile, "model", "base_url") {
		return originProfile, f.Profile
	}
	if trusted { // 项目层不能设 base_url（projectDenied），只看 model
		if proj := filepath.Join(appid.Project(root), "profile.toml"); file(proj, "model") {
			return originProfile, proj
		}
	}
	if tp := TrustPath(); tp != "" {
		if user := filepath.Join(filepath.Dir(tp), "profile.toml"); file(user, "model", "base_url") {
			return originProfile, user
		}
	}
	for i := len(ps) - 1; i >= 0; i-- { // 后加载的包覆盖先加载的
		k := ps[i]
		keys := []string{"model", "base_url"}
		if k.Scope == pkgs.ScopeProject {
			keys = []string{"model"}
		}
		if sets(k.Profile(), keys...) {
			return originProfile, k.File
		}
	}
	base := f.BaseProfile
	if base == "" {
		base = defaultProfileTOML
	}
	if q, err := component.ParseProfile(base, ".", nil, ""); err == nil && sets(q, "model", "base_url") {
		return originProfile, "内置 profile"
	}
	return originProfile, ""
}
