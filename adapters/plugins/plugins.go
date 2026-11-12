// Package plugins 从磁盘发现、加载插件包，并生成插件包骨架。
//
// 放在内核之外：runtime/pkgs 只解析一份已经定位好的包目录（Parse），不扫目录、不决定用户目录和项目目录。
// 用户级 <home>/plugins 与项目级 <项目目录>/plugins 的路径规则、受信判断都在这里。
package plugins

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/pkgs"
)

// scaffoldManifest 骨架清单（注释说明各段，可直接删改）。
const scaffoldManifest = `# 插件包清单（见 docs/90 - 归档/10 - 框架模块/15 - 插件包.md）。改完重启生效。
name = "%s"
version = "0.1.0"
description = ""

# 技能目录（相对包目录），格式同 ~/.tenon/skills：<名字>/SKILL.md 或 <名字>.md。需开 skills 插件。
skills = "skills"

# 提示词片段（相对包目录），按顺序拼成一个系统块。需开 prompt 插件（缺省开）。
prompt = ["prompt.md"]

# 定义目录（相对包目录），布局同 agentdef.DirSource：platform/<名字>/agent.toml、
# tenant/<租户>/<名字>/agent.toml、user/<租户>/<用户>/<名字>/agent.toml。
# agents = "agents"

# MCP 服务，字段同 mcp.toml 的 [[server]]。需开 mcp 插件。${TENON_PLUGIN_DIR} = 包目录。
# [[server]]
# name = "fs"
# command = "npx"
# args = ["-y", "@modelcontextprotocol/server-filesystem", "."]
# trust = "readonly"

# 钩子，字段同 hooks.toml 的 [[hook]]。需开 hooks 插件。
# [[hook]]
# event = "pre_tool_use"
# matcher = "shell"
# command = "echo ok"

# 内置插件的缺省配置（低于 ~/.tenon/profile.toml）。不能开关插件。
# [config.compact-basic]
# keep_ratio = 0.2
`

// Discover 列出 root 下每个带清单的子目录并解析（按目录名排序）。root 不存在返回空；
// 没有清单的子目录跳过并记一条提示；清单有错直接返回错误。
func Discover(root, scope string) ([]*pkgs.Package, []string, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("读插件包目录 %s：%w", root, err)
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	var out []*pkgs.Package
	var notes []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, pkgs.ManifestName)); err != nil {
			notes = append(notes, fmt.Sprintf("跳过 %s：没有 %s", dir, pkgs.ManifestName))
			continue
		}
		p, err := pkgs.Parse(dir, scope)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, p)
	}
	return out, notes, nil
}

// HasAny root 下是否有子目录（用于“项目带插件包”的信任判断与提示）。
func HasAny(root string) bool {
	ents, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			return true
		}
	}
	return false
}

// Load 用户级 <home>/plugins 与项目级 <dir>/.tenon/plugins（只在受信时）。
// 同名包项目级覆盖用户级（记一条提示）；段内容之间的冲突（如 MCP 服务重名）由处理该段的组件检查。顺序：用户级在前、项目级在后，各按名字排序。
func Load(home, root string, trusted bool) ([]*pkgs.Package, []string, error) {
	var all []*pkgs.Package
	var notes []string
	if home != "" {
		ps, ns, err := Discover(filepath.Join(home, "plugins"), pkgs.ScopeUser)
		if err != nil {
			return nil, nil, err
		}
		all, notes = append(all, ps...), append(notes, ns...)
	}
	projDir := filepath.Join(appid.Project(root), "plugins")
	if trusted {
		ps, ns, err := Discover(projDir, pkgs.ScopeProject)
		if err != nil {
			return nil, nil, err
		}
		notes = append(notes, ns...)
		for _, p := range ps {
			for i, q := range all {
				if q.Name == p.Name {
					notes = append(notes, fmt.Sprintf("插件包 %s：项目级 %s 覆盖用户级 %s", p.Name, p.Dir, q.Dir))
					all = append(all[:i], all[i+1:]...)
					break
				}
			}
			all = append(all, p)
		}
	} else if HasAny(projDir) {
		notes = append(notes, "项目未受信，跳过项目插件包 "+projDir+"（"+appid.Command("trust")+" 可信任）")
	}
	return all, notes, nil
}

// Scaffold 在 root/<name> 生成插件包骨架（清单 + 空技能目录 + 提示词片段）。目录已存在报错。
func Scaffold(root, name string) (string, error) {
	if !pkgs.ValidName(name) {
		return "", fmt.Errorf("插件包名 %q 不合法（小写字母、数字与连字符）", name)
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("%s 已存在", dir)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, pkgs.ManifestName), []byte(fmt.Sprintf(scaffoldManifest, name)), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), nil, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}
