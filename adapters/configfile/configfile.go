// Package configfile 从 TOML 文件读部署期配置（18 方案第四节的文件实现）。
//
// 放在内核之外：runtime/ 只定义 component.LayeredConfig 接口，不读文件。
// apphost 用它接用户级与项目级配置；嵌入方想要文件配置时显式传给 tenon.WithLayeredConfig。
//
// 两层，低优先级在前：用户级 <home>/<文件名>.toml，项目级 <项目目录>/<文件名>.toml。
// 文件不存在的层不出现；项目层的 Trusted 由调用方传入（本包不判断受信）。
// 插件包不在这里：它的贡献是结构化的段，走 pkgs.Service。
package configfile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/component"
)

// Source 按目录读 TOML 的配置来源。
type Source struct {
	// Home 用户级目录（如 ~/.tenon）；空 = 没有用户层。
	Home string
	// Project 项目级目录（如 <工作目录>/.tenon）；空 = 没有项目层。
	Project string
	// Trusted 项目是否受信，原样标到项目层上；组件按它决定用不用项目层。
	Trusted bool
}

// New 用目录构造来源。
func New(home, project string, trusted bool) *Source {
	return &Source{Home: home, Project: project, Trusted: trusted}
}

// Files 本实现认识的配置文件标识（不含扩展名）。
var Files = []string{"hooks", "permissions", "mcp"}

// Layers 见 component.LayeredConfig。用户层在前（低优先级），项目层在后。
func (s *Source) Layers(context.Context) ([]component.ConfigLayer, error) {
	var out []component.ConfigLayer
	if s.Home != "" {
		l, err := readLayer(s.Home, component.OriginUser, "user", true)
		if err != nil {
			return nil, err
		}
		if l != nil {
			out = append(out, *l)
		}
	}
	if s.Project != "" {
		l, err := readLayer(s.Project, component.OriginProject, "project", s.Trusted)
		if err != nil {
			return nil, err
		}
		if l != nil {
			out = append(out, *l)
		}
	}
	return out, nil
}

// readLayer 读一个目录下认识的配置文件；一个都没有返回 nil。
func readLayer(dir, origin, name string, trusted bool) (*component.ConfigLayer, error) {
	l := component.ConfigLayer{Name: name, Origin: origin, Trusted: trusted, Where: dir,
		Values: map[string]any{}}
	found := false
	for _, f := range Files {
		path := filepath.Join(dir, f+".toml")
		v := map[string]any{}
		if _, err := toml.DecodeFile(path, &v); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("configfile: 读 %s: %w", path, err)
		}
		l.Values[f] = v
		found = true
	}
	if !found {
		return nil, nil
	}
	return &l, nil
}
