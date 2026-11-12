// Package pkgs 插件包（外置插件第一级，见 docs/90 - 归档/10 - 框架模块/15 - 插件包.md）。
//
// 插件包是一个带清单 tenon-plugin.toml 的目录，打包 MCP 服务、用户钩子、技能、提示词片段与内置插件的缺省配置。
// 不引入新协议：包里的内容交给对应的内置插件（mcp / hooks / skills / prompt）按原有规则生效，
// 缺省配置作为一层 profile（内置 < 插件包 < 用户 profile < 项目 profile < -profile）。
//
// 本包只负责发现、解析、校验清单的通用部分；交给组件的段（mcp 的 [[server]]、hooks 的 [[hook]]）由那个组件
// 登记解码与校验（RegisterSection，审查 P1-56），本包不认识段的形状。
package pkgs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/ext"
)

// ManifestName 清单文件名。
const ManifestName = "tenon-plugin.toml"

// DirVar 包目录占位符：MCP 服务的 command / args / cwd / env 与钩子的 command 里出现时替换为包目录绝对路径；
// MCP stdio 子进程还会收到同名环境变量。
const DirVar = "TENON_PLUGIN_DIR"

// 来源。
const (
	ScopeUser    = "user"
	ScopeProject = "project"
)

var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidName 插件包名是否合法（小写字母、数字与连字符）。
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Manifest tenon-plugin.toml 的内容。
type Manifest struct {
	Name        string   `toml:"name"`
	Version     string   `toml:"version"`
	Description string   `toml:"description"`
	Skills      string   `toml:"skills"` // 技能目录（相对包目录），格式同 ~/.tenon/skills
	Prompt      []string `toml:"prompt"` // 提示词片段文件（相对包目录），按顺序拼成一个系统块
	Agents      string   `toml:"agents"` // 定义目录（相对包目录），布局同 agentdef.Dir：platform/<名字>/agent.toml 等
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
	AgentsDir string   // 定义目录（绝对路径），空 = 不带定义
	// Sections 组件登记的段解码后的值（段名 → 值，见 RegisterSection / SectionValue）。
	Sections map[string]any
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
			if len(k) > 0 && !builtinKeys[k[0]] {
				continue // 组件段与未知顶层键交给 decodeSections（报"没有组件处理这一段"）
			}
			ks = append(ks, k.String())
		}
		if len(ks) > 0 {
			return nil, fail("未知字段 %s", strings.Join(ks, "、"))
		}
	}
	if !ValidName(m.Name) {
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
	if m.Agents != "" {
		if p.AgentsDir, err = within(dir, m.Agents); err != nil {
			return nil, fail("agents：%v", err)
		}
		if st, err := os.Stat(p.AgentsDir); err != nil || !st.IsDir() {
			return nil, fail("agents 目录 %s 不存在", m.Agents)
		}
	}
	if p.Sections, err = decodeSections(file, dir); err != nil {
		return nil, fail("%v", err)
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

// Within 把相对路径解析到包目录下；绝对路径或跳出包目录报错（段解码校验路径用）。
func Within(dir, rel string) (string, error) { return within(dir, rel) }

// within 见 Within。
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
	for _, k := range p.SectionKeys() {
		sec, _ := SectionOf(k)
		d := sec.Label
		if sec.Summary != nil {
			d = sec.Summary(p.Sections[k])
		}
		if d == "" {
			d = k
		}
		out = append(out, d)
	}
	if p.SkillsDir != "" {
		out = append(out, "技能")
	}
	if len(p.Prompts) > 0 {
		out = append(out, fmt.Sprintf("提示词 %d 段", len(p.Prompts)))
	}
	if p.AgentsDir != "" {
		out = append(out, "定义")
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

// Expand 把字符串里的 ${TENON_PLUGIN_DIR} 换成包目录。
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
