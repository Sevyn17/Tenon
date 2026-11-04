// Package pkgs 插件包（外置插件第一级，见 docs/90 - 归档/10 - 框架模块/15 - 插件包.md）。
//
// 插件包是一个带清单 ybase-plugin.toml 的目录，打包 MCP 服务、用户钩子、技能、提示词片段与内置插件的缺省配置。
// 不引入新协议：包里的内容交给对应的内置插件（mcp / hooks / skills / prompt）按原有规则生效，
// 缺省配置作为一层 profile（内置 < 插件包 < 用户 profile < 项目 profile < -profile）。
//
// 本包只负责发现、解析、校验清单，不认识任何具体插件；把内容接到哪个插件由应用（apps/internal/boot）决定。
package pkgs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"ybase/runtime/component"
	"ybase/runtime/ext"
	"ybase/runtime/toolhub"
)

// ManifestName 清单文件名。
const ManifestName = "ybase-plugin.toml"

// DirVar 包目录占位符：MCP 服务的 command / args / cwd / env 与钩子的 command 里出现时替换为包目录绝对路径；
// MCP stdio 子进程还会收到同名环境变量。
const DirVar = "YBASE_PLUGIN_DIR"

// 来源。
const (
	ScopeUser    = "user"
	ScopeProject = "project"
)

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	serverRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
)

// Server 一个 MCP 服务，字段与 mcp 插件的 [[server]] 相同。
type Server struct {
	Name    string            `toml:"name" json:"name,omitempty"`
	Command string            `toml:"command" json:"command,omitempty"`
	Args    []string          `toml:"args" json:"args,omitempty"`
	Env     map[string]string `toml:"env" json:"env,omitempty"`
	Cwd     string            `toml:"cwd" json:"cwd,omitempty"`
	URL     string            `toml:"url" json:"url,omitempty"`
	Headers map[string]string `toml:"headers" json:"headers,omitempty"`
	Timeout int               `toml:"timeout" json:"timeout,omitempty"`
	Trust   string            `toml:"trust" json:"trust,omitempty"`
	Sandbox string            `toml:"sandbox" json:"sandbox,omitempty"`
}

// Hook 一条钩子，字段与 hooks 插件的 [[hook]] 相同。
type Hook struct {
	Event   string `toml:"event" json:"event,omitempty"`
	Matcher string `toml:"matcher" json:"matcher,omitempty"`
	Command string `toml:"command" json:"command,omitempty"`
	Timeout int    `toml:"timeout" json:"timeout,omitempty"`
	Sandbox string `toml:"sandbox" json:"sandbox,omitempty"`
}

// Manifest ybase-plugin.toml 的内容。
type Manifest struct {
	Name        string   `toml:"name"`
	Version     string   `toml:"version"`
	Description string   `toml:"description"`
	Skills      string   `toml:"skills"` // 技能目录（相对包目录），格式同 ~/.ybase/skills
	Prompt      []string `toml:"prompt"` // 提示词片段文件（相对包目录），按顺序拼成一个系统块
	Server      []Server `toml:"server"`
	Hook        []Hook   `toml:"hook"`
	// Config 内置插件的缺省配置：[config.<插件名>] 键 = 值。不能开关插件。
	Config map[string]map[string]any `toml:"config"`
}

// Package 一个已加载的插件包。路径均为绝对路径。
type Package struct {
	Manifest
	Dir       string
	File      string   // 清单路径
	Scope     string   // user / project
	SkillsDir string   // 空 = 无
	Prompts   []string // 提示词片段文件
}

// Parse 读并校验一个包目录。错误信息带清单路径。
func Parse(dir, scope string) (*Package, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	file := filepath.Join(dir, ManifestName)
	var m Manifest
	md, err := toml.DecodeFile(file, &m)
	if err != nil {
		return nil, fmt.Errorf("插件包 %s：%w", file, err)
	}
	fail := func(format string, a ...any) error {
		return fmt.Errorf("插件包 %s：%s", file, fmt.Sprintf(format, a...))
	}
	if und := md.Undecoded(); len(und) > 0 {
		var ks []string
		for _, k := range und {
			if len(k) > 0 && k[0] == "config" {
				continue
			}
			ks = append(ks, k.String())
		}
		if len(ks) > 0 {
			return nil, fail("未知字段 %s", strings.Join(ks, "、"))
		}
	}
	if !nameRe.MatchString(m.Name) {
		return nil, fail("name %q 不合法（小写字母、数字与连字符）", m.Name)
	}
	if base := filepath.Base(dir); base != m.Name {
		return nil, fail("name %q 须与目录名 %q 相同", m.Name, base)
	}
	p := &Package{Manifest: m, Dir: dir, File: file, Scope: scope}
	if m.Skills != "" {
		if p.SkillsDir, err = within(dir, m.Skills); err != nil {
			return nil, fail("skills：%v", err)
		}
		if st, err := os.Stat(p.SkillsDir); err != nil || !st.IsDir() {
			return nil, fail("skills 目录 %s 不存在", m.Skills)
		}
	}
	for _, f := range m.Prompt {
		abs, err := within(dir, f)
		if err != nil {
			return nil, fail("prompt：%v", err)
		}
		if st, err := os.Stat(abs); err != nil || !st.Mode().IsRegular() {
			return nil, fail("prompt 文件 %s 不存在", f)
		}
		p.Prompts = append(p.Prompts, abs)
	}
	seen := map[string]bool{}
	for _, s := range m.Server {
		if !serverRe.MatchString(s.Name) {
			return nil, fail("服务名 %q 不合法（[A-Za-z0-9_-]{1,32}）", s.Name)
		}
		if seen[s.Name] {
			return nil, fail("服务名 %q 重复", s.Name)
		}
		seen[s.Name] = true
		if (s.Command == "") == (s.URL == "") {
			return nil, fail("服务 %s 须且只能配置 command 或 url 之一", s.Name)
		}
		switch s.Trust {
		case "", "ask", "readonly", "trusted":
		default:
			return nil, fail("服务 %s 的 trust %q 不合法（ask / readonly / trusted）", s.Name, s.Trust)
		}
		if s.Sandbox != "" {
			if _, err := toolhub.ParseSandboxMode(s.Sandbox); err != nil {
				return nil, fail("服务 %s：%v", s.Name, err)
			}
		}
		if s.Cwd != "" && !strings.Contains(s.Cwd, "${"+DirVar+"}") {
			if _, err := within(dir, s.Cwd); err != nil {
				return nil, fail("服务 %s 的 cwd：%v", s.Name, err)
			}
		}
	}
	for i, h := range m.Hook {
		switch h.Event {
		case "pre_tool_use", "post_tool_use", "stop":
		default:
			return nil, fail("第 %d 条钩子 event %q 不支持（pre_tool_use / post_tool_use / stop）", i+1, h.Event)
		}
		if strings.TrimSpace(h.Command) == "" {
			return nil, fail("第 %d 条钩子缺 command", i+1)
		}
		if h.Sandbox != "" {
			if _, err := toolhub.ParseSandboxMode(h.Sandbox); err != nil {
				return nil, fail("第 %d 条钩子 sandbox：%v", i+1, err)
			}
		}
		if h.Matcher != "" {
			if _, err := regexp.Compile("^(?:" + h.Matcher + ")$"); err != nil {
				return nil, fail("第 %d 条钩子 matcher：%v", i+1, err)
			}
		}
	}
	for name := range m.Config {
		meta, ok := component.MetaOf(name)
		if !ok {
			return nil, fail("[config.%s]：没有这个插件", name)
		}
		known := map[string]bool{}
		for _, f := range component.SchemaOf(meta.Config) {
			known[f.Key] = true
		}
		for key := range m.Config[name] {
			if !known[key] {
				return nil, fail("[config.%s]：未知配置项 %q", name, key)
			}
		}
	}
	return p, nil
}

// within 把相对路径解析到包目录下；绝对路径或跳出包目录报错。
func within(dir, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", fmt.Errorf("%q 须是包目录内的相对路径", rel)
	}
	abs := filepath.Join(dir, rel)
	r, err := filepath.Rel(dir, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q 跳出了包目录", rel)
	}
	return abs, nil
}

// Discover 列出 root 下每个带清单的子目录并解析（按目录名排序）。root 不存在返回空；
// 没有清单的子目录跳过并记一条提示；清单有错直接返回错误。
func Discover(root, scope string) ([]*Package, []string, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("读插件包目录 %s：%w", root, err)
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	var out []*Package
	var notes []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
			notes = append(notes, fmt.Sprintf("跳过 %s：没有 %s", dir, ManifestName))
			continue
		}
		p, err := Parse(dir, scope)
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

// Load 用户级 <home>/plugins 与项目级 <dir>/.ybase/plugins（只在受信时）。
// 同名包项目级覆盖用户级（记一条提示）；不同包的 MCP 服务重名报错。顺序：用户级在前、项目级在后，各按名字排序。
func Load(home, root string, trusted bool) ([]*Package, []string, error) {
	var all []*Package
	var notes []string
	if home != "" {
		ps, ns, err := Discover(filepath.Join(home, "plugins"), ScopeUser)
		if err != nil {
			return nil, nil, err
		}
		all, notes = append(all, ps...), append(notes, ns...)
	}
	projDir := filepath.Join(root, ".ybase", "plugins")
	if trusted {
		ps, ns, err := Discover(projDir, ScopeProject)
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
		notes = append(notes, "项目未受信，跳过项目插件包 "+projDir+"（ybase trust 可信任）")
	}
	owner := map[string]string{}
	for _, p := range all {
		for _, s := range p.Server {
			if prev, dup := owner[s.Name]; dup {
				return nil, nil, fmt.Errorf("插件包 %s：MCP 服务名 %q 与插件包 %s 重复", p.File, s.Name, prev)
			}
			owner[s.Name] = p.Name
		}
	}
	return all, notes, nil
}

// Profile 包的缺省配置作为一层 profile（只含 config，不开关插件），来源记清单路径。
func (p *Package) Profile() *component.Profile {
	q := &component.Profile{Sources: map[string]string{}}
	names := make([]string, 0, len(p.Config))
	for n := range p.Config {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := map[string]any{}
		for k, v := range p.Config[n] {
			c[k] = v
		}
		q.Plugins = append(q.Plugins, component.Entry{Name: n, Config: c})
		q.Sources[n] = p.File
	}
	return q
}

// Contents 包里有哪些内容（展示用），如 "MCP 服务 fs、技能、提示词 1 段"。
func (p *Package) Contents() string {
	var out []string
	if len(p.Server) > 0 {
		var ns []string
		for _, s := range p.Server {
			ns = append(ns, s.Name)
		}
		out = append(out, "MCP 服务 "+strings.Join(ns, "、"))
	}
	if len(p.Hook) > 0 {
		out = append(out, fmt.Sprintf("钩子 %d 条", len(p.Hook)))
	}
	if p.SkillsDir != "" {
		out = append(out, "技能")
	}
	if len(p.Prompts) > 0 {
		out = append(out, fmt.Sprintf("提示词 %d 段", len(p.Prompts)))
	}
	if len(p.Config) > 0 {
		var ns []string
		for n := range p.Config {
			ns = append(ns, n)
		}
		sort.Strings(ns)
		out = append(out, "缺省配置 "+strings.Join(ns, "、"))
	}
	if len(out) == 0 {
		return "（空）"
	}
	return strings.Join(out, "；")
}

// Expand 把字符串里的 ${YBASE_PLUGIN_DIR} 换成包目录。
func Expand(s, dir string) string { return strings.ReplaceAll(s, "${"+DirVar+"}", dir) }

// Service 应用把已加载的插件包（[]*Package）以这个键预置给插件；mcp / hooks / skills / prompt 插件在
// Requires 里写 "?service:packages" 取用。未预置 = 没有插件包。
const Service = ext.ServicePackages

// From 取预置的插件包；插件须在 Requires 里写 "?service:packages"。
func From(cx *component.Registrar) ([]*Package, error) {
	v, ok, err := component.UseOptional[[]*Package](cx, Service)
	if err != nil || !ok {
		return nil, err
	}
	return v, nil
}

// PromptText 按清单顺序读提示词片段并拼接（片段之间空一行）；读失败的错误带文件路径。
func (p *Package) PromptText() (string, error) {
	var parts []string
	for _, f := range p.Prompts {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("插件包 %s：读提示词 %s：%w", p.Name, f, err)
		}
		if t := strings.TrimSpace(string(b)); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// scaffoldManifest 骨架清单（注释说明各段，可直接删改）。
const scaffoldManifest = `# 插件包清单（见 docs/90 - 归档/10 - 框架模块/15 - 插件包.md）。改完重启生效。
name = "%s"
version = "0.1.0"
description = ""

# 技能目录（相对包目录），格式同 ~/.ybase/skills：<名字>/SKILL.md 或 <名字>.md。需开 skills 插件。
skills = "skills"

# 提示词片段（相对包目录），按顺序拼成一个系统块。需开 prompt 插件（缺省开）。
prompt = ["prompt.md"]

# MCP 服务，字段同 mcp.toml 的 [[server]]。需开 mcp 插件。${YBASE_PLUGIN_DIR} = 包目录。
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

# 内置插件的缺省配置（低于 ~/.ybase/profile.toml）。不能开关插件。
# [config.compact-basic]
# keep_ratio = 0.2
`

// Scaffold 在 root/<name> 生成插件包骨架（清单 + 空技能目录 + 提示词片段）。目录已存在报错。
func Scaffold(root, name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("插件包名 %q 不合法（小写字母、数字与连字符）", name)
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("%s 已存在", dir)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(fmt.Sprintf(scaffoldManifest, name)), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), nil, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}
