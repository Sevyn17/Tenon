package component

// profile（plugin-manager D1：TOML）。
//
//	include   = ["base.toml"]          # 相对本文件
//	mandatory = ["permission"]         # 与父 profile 取并集，子 profile 不能移除（P18）
//
//	[kernel]                           # 内核参数（kernel/docs/01 - 设计文档/03 - 约束清单.md「[kernel] 配置节」），按键合并
//	max_tool_calls_per_step = 16
//
//	[[plugin]]
//	name = "compact-basic"
//	enabled = true                     # 省略 = 按插件声明的缺省（Meta.Default）
//	[plugin.config]
//	threshold = 0.8
//
// 合并（plugin-switch-plan §3.1）：父在前、子在后；同名插件 enabled 后者覆盖、config 按键合并；
// disabled = true 等同 enabled = false（兼容）；强制插件不能关。多个文件按 Overlay 依次叠加（内置 < 用户 < 项目 < -profile）。

import (
	"fmt"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type profileFile struct {
	Include   []string       `toml:"include"`
	Mandatory []string       `toml:"mandatory"`
	Kernel    map[string]any `toml:"kernel"`
	Plugin    []profileItem  `toml:"plugin"`
}

type profileItem struct {
	Name     string         `toml:"name"`
	Enabled  *bool          `toml:"enabled"`
	Disabled bool           `toml:"disabled"`
	Config   map[string]any `toml:"config"`
}

// ProfileReader 按绝对路径读一份 profile 文本。内核自己不读文件；
// include 展开由调用方注入（adapters/profile 用 os.ReadFile）。
type ProfileReader func(abs string) ([]byte, error)

// Profile 合并后的结果。Plugins 只含 profile 里写到的插件（Enabled 为 nil = 按缺省）；
// Sources：插件名 → 最后写到它的文件；Kernel：[kernel] 节，KernelSources 键 → 文件。
type Profile struct {
	Plugins       []Entry
	Mandatory     []string
	Sources       map[string]string
	Kernel        map[string]any
	KernelSources map[string]string
}

// ParseProfile 从内存解析。文本里写了 include 时必须给 read，相对路径按 dir 解析；
// 没写 include 时 read 可以为 nil（apphost 的内置 profile 走这条）。
// self 是这份文本的来源标记，写进 Sources；空串记成 "<inline>"。
func ParseProfile(data, dir string, read ProfileReader, self string) (*Profile, error) {
	if self == "" {
		self = "<inline>"
	}
	return parseProfile(data, dir, self, map[string]bool{}, read)
}

func parseProfile(data, dir, self string, seen map[string]bool, read ProfileReader) (*Profile, error) {
	var f profileFile
	md, err := toml.Decode(data, &f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", self, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: 未知字段 %v", self, und)
	}
	return merge(f, dir, self, seen, read)
}

func merge(f profileFile, dir, self string, seen map[string]bool, read ProfileReader) (*Profile, error) {
	out := &Profile{Sources: map[string]string{}}
	for _, inc := range f.Include {
		if read == nil {
			return nil, fmt.Errorf("%s: include 需要调用方提供读文件函数", self)
		}
		p := inc
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		p = filepath.Clean(p)
		if seen[p] {
			return nil, fmt.Errorf("profile 循环 include: %s", p)
		}
		seen[p] = true
		b, err := read(p)
		if err != nil {
			return nil, err
		}
		parent, err := parseProfile(string(b), filepath.Dir(p), p, seen, read)
		if err != nil {
			return nil, err
		}
		if err := out.Overlay(parent); err != nil {
			return nil, err
		}
	}
	own := &Profile{Sources: map[string]string{}, Mandatory: f.Mandatory}
	if len(f.Kernel) > 0 {
		own.Kernel, own.KernelSources = map[string]any{}, map[string]string{}
		for k, v := range f.Kernel {
			own.Kernel[k], own.KernelSources[k] = v, self
		}
	}
	for _, it := range f.Plugin {
		if it.Name == "" {
			return nil, fmt.Errorf("%s: 插件行缺 name", self)
		}
		e := Entry{Name: it.Name, Enabled: it.Enabled, Config: it.Config}
		if it.Disabled {
			off := false
			e.Enabled = &off
		}
		own.Plugins = append(own.Plugins, e)
		own.Sources[it.Name] = self
	}
	if err := out.Overlay(own); err != nil {
		return nil, fmt.Errorf("%s: %w", self, err)
	}
	return out, nil
}

// Overlay 把 q 叠加到 p 上（q 优先）：同名插件 enabled 以 q 为准（q 未写则保留），config 按键合并；
// mandatory 取并集；[kernel] 按键合并。关闭强制插件报错。
func (p *Profile) Overlay(q *Profile) error {
	if q == nil {
		return nil
	}
	if p.Sources == nil {
		p.Sources = map[string]string{}
	}
	mand := map[string]bool{}
	for _, m := range append(append([]string(nil), p.Mandatory...), q.Mandatory...) {
		mand[m] = true
	}
	idx := map[string]int{}
	for i, e := range p.Plugins {
		idx[e.Name] = i
	}
	for _, e := range q.Plugins {
		i, ok := idx[e.Name]
		if !ok {
			idx[e.Name] = len(p.Plugins)
			p.Plugins = append(p.Plugins, Entry{Name: e.Name})
			i = len(p.Plugins) - 1
		}
		cur := &p.Plugins[i]
		if e.Enabled != nil {
			v := *e.Enabled
			cur.Enabled = &v
		}
		if len(e.Config) > 0 {
			c := map[string]any{}
			for k, v := range cur.Config {
				c[k] = v
			}
			for k, v := range e.Config {
				c[k] = v
			}
			cur.Config = c
		}
		if src, ok := q.Sources[e.Name]; ok {
			p.Sources[e.Name] = src
		}
	}
	for k, v := range q.Kernel {
		if p.Kernel == nil {
			p.Kernel, p.KernelSources = map[string]any{}, map[string]string{}
		}
		p.Kernel[k] = v
		p.KernelSources[k] = q.KernelSources[k]
	}
	p.Mandatory = p.Mandatory[:0]
	for m := range mand {
		p.Mandatory = append(p.Mandatory, m)
	}
	sortStrings(p.Mandatory)
	for _, e := range p.Plugins {
		if mand[e.Name] && e.Enabled != nil && !*e.Enabled {
			return fmt.Errorf("不能停用强制插件 %s", e.Name)
		}
	}
	return nil
}

// Strip 删掉某插件的若干配置键，返回实际删掉的键（受信项目也不能设的键，plugin-switch-plan §3.3）。
func (p *Profile) Strip(name string, keys ...string) []string {
	var got []string
	for i := range p.Plugins {
		if p.Plugins[i].Name != name {
			continue
		}
		for _, k := range keys {
			if _, ok := p.Plugins[i].Config[k]; ok {
				delete(p.Plugins[i].Config, k)
				got = append(got, name+"."+k)
			}
		}
	}
	return got
}

// State 插件开关的最终状态与来源（explain 用）。
type State struct {
	Name    string
	On      bool
	Default bool
	Source  string // 决定开关的文件；空 = 插件缺省
	Effects []string
}

// States 列出全部已登记插件的开关状态（强制插件恒开）。
func (p *Profile) States() []State {
	set := map[string]Entry{}
	for _, e := range p.Plugins {
		set[e.Name] = e
	}
	mand := map[string]bool{}
	for _, m := range p.Mandatory {
		mand[m] = true
	}
	var out []State
	for _, n := range Registered() {
		f, _ := lookup(n)
		m := f().Meta()
		st := State{Name: n, On: m.Default, Default: m.Default, Effects: m.Effects}
		if e, ok := set[n]; ok && e.Enabled != nil {
			st.On, st.Source = *e.Enabled, p.Sources[n]
		}
		if mand[n] {
			st.On = true
		}
		out = append(out, st)
	}
	return out
}

// Spec 转成装配输入。
// 开启集 = 声明 Default 的已登记插件 + profile 写 enabled = true 的 − 写 enabled = false 的，强制插件恒开；
// profile 里写到但未登记的插件，只有显式开启时才报未注册（Assemble）。
func (p *Profile) Spec(defaults map[string]any) Spec {
	set := map[string]Entry{}
	for _, e := range p.Plugins {
		set[e.Name] = e
	}
	mand := map[string]bool{}
	for _, m := range p.Mandatory {
		mand[m] = true
	}
	var on []Entry
	seen := map[string]bool{}
	for _, st := range p.States() {
		seen[st.Name] = true
		if st.On {
			on = append(on, Entry{Name: st.Name, Config: set[st.Name].Config})
		}
	}
	for _, e := range p.Plugins {
		if !seen[e.Name] && (mand[e.Name] || (e.Enabled != nil && *e.Enabled)) {
			on = append(on, Entry{Name: e.Name, Config: e.Config})
		}
	}
	return Spec{Plugins: on, Mandatory: p.Mandatory, Defaults: defaults}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
