package pkgs

// 清单段（2026-11-04 审查 P1-56）：插件包里交给某个组件的内容（如 mcp 的 [[server]]、hooks 的 [[hook]]）
// 由那个组件在 init 里登记段的解码与校验，本包只认段名、不认识段的形状。
// 没有组件登记的段按未知字段报错（装配里没编进处理它的组件时，包里的这段内容不会悄悄被忽略）。

import (
	"fmt"
	"sort"
	"sync"

	"github.com/BurntSushi/toml"
)

// Section 一个清单段的处理方。
type Section struct {
	Key   string // 清单里的顶层键，如 "server"
	Owner string // 处理它的组件名（内容需要这个组件开着才生效）
	Label string // 展示名，如 "MCP 服务"
	// Decode 解码并校验这一段；dir 为包目录（校验相对路径用）。返回值存进 Package.Sections[Key]。
	Decode func(dir string, md toml.MetaData, prim toml.Primitive) (any, error)
	// Summary 可空：Contents 里这一段的描述；空 = 只写 Label。
	Summary func(v any) string
}

var (
	secMu    sync.Mutex
	sections = map[string]Section{}
)

// RegisterSection 登记一个清单段（组件 init 里调用）。重复登记 panic——属编程错误。
func RegisterSection(s Section) {
	secMu.Lock()
	defer secMu.Unlock()
	if s.Key == "" || s.Decode == nil {
		panic("pkgs: 清单段缺 Key 或 Decode")
	}
	if _, dup := sections[s.Key]; dup || builtinKeys[s.Key] {
		panic("pkgs: 重复登记清单段 " + s.Key)
	}
	sections[s.Key] = s
}

// SectionOf 已登记的段（没有返回 false）。
func SectionOf(key string) (Section, bool) {
	secMu.Lock()
	defer secMu.Unlock()
	s, ok := sections[key]
	return s, ok
}

// SectionValue 取包里某段解码后的值（没有这段返回零值与 false）。
func SectionValue[T any](p *Package, key string) (T, bool) {
	var zero T
	v, ok := p.Sections[key]
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	return t, ok
}

// SectionKeys 包里出现的段名（排序）。
func (p *Package) SectionKeys() []string {
	ks := make([]string, 0, len(p.Sections))
	for k := range p.Sections {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// builtinKeys 本包自己解析的顶层键（不能被组件登记成段）。
var builtinKeys = map[string]bool{"name": true, "version": true, "description": true, "skills": true, "prompt": true, "config": true}

// decodeSections 按登记解码清单里的段；未登记的未知顶层键报错。
func decodeSections(file, dir string) (map[string]any, error) {
	var raw map[string]toml.Primitive
	md, err := toml.DecodeFile(file, &raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if builtinKeys[k] {
			continue
		}
		s, ok := SectionOf(k)
		if !ok {
			return nil, fmt.Errorf("未知字段 %s（没有组件处理这一段）", k)
		}
		v, err := s.Decode(dir, md, raw[k])
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	// 段里写错的字段（如 [[server]] 里的拼写错误）：段解码之后仍未被读到的键。
	for _, k := range md.Undecoded() {
		if len(k) > 1 && !builtinKeys[k[0]] {
			return nil, fmt.Errorf("未知字段 %s", k.String())
		}
	}
	return out, nil
}
