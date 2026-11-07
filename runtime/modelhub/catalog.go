package modelhub

// 目录（model-hub D3）：内置快照 < 插件提供 < 本地发现 < 用户 models.toml。
// 键 provider/model（模型）、provider（提供方）、别名名；整条覆盖，不做字段级合并；explain 显示来源。

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// 来源层级（数值越大越优先）。
const (
	LayerBuiltin  = 0
	LayerPlugin   = 1
	LayerDiscover = 2
	LayerUser     = 3
)

// CatalogFile 一份目录声明（TOML / 插件数据同构）。
type CatalogFile struct {
	Provider map[string]Provider `toml:"provider"`
	Model    map[string]Model    `toml:"model"` // 键：provider/model 或 model（后者用条目里的 provider）
	Alias    map[string]struct {
		Plan Plan `toml:"plan"`
	} `toml:"alias"`
}

// ParseCatalog 解析 TOML 目录。
func ParseCatalog(data string) (CatalogFile, error) {
	var f CatalogFile
	_, err := toml.Decode(data, &f)
	return f, err
}

type layered[T any] struct {
	v     T
	layer int
	src   string
}

// Catalog 合并后的目录。并发安全。
type Catalog struct {
	mu        sync.RWMutex
	providers map[string]layered[Provider]
	models    map[string]layered[Model] // 键 provider/model
	aliases   map[string]layered[Plan]
}

// NewCatalog 空目录。
func NewCatalog() *Catalog {
	return &Catalog{providers: map[string]layered[Provider]{}, models: map[string]layered[Model]{},
		aliases: map[string]layered[Plan]{}}
}

// Add 合入一份声明；同键时层级高者胜，同层后写胜（整条覆盖）。
func (c *Catalog) Add(layer int, src string, f CatalogFile) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, p := range f.Provider {
		if cur, ok := c.providers[k]; !ok || layer >= cur.layer {
			c.providers[k] = layered[Provider]{p, layer, src}
		}
	}
	for k, m := range f.Model {
		key := k
		if !strings.Contains(k, "/") {
			if m.Provider == "" {
				return fmt.Errorf("modelhub: 模型 %q 缺 provider（%s）", k, src)
			}
			key = m.Provider + "/" + k
		} else if m.Provider == "" {
			m.Provider = strings.SplitN(k, "/", 2)[0]
		}
		if m.Name == "" {
			m.Name = strings.SplitN(key, "/", 2)[1]
		}
		if cur, ok := c.models[key]; !ok || layer >= cur.layer {
			c.models[key] = layered[Model]{m, layer, src}
		}
	}
	for k, a := range f.Alias {
		if cur, ok := c.aliases[k]; !ok || layer >= cur.layer {
			c.aliases[k] = layered[Plan]{a.Plan, layer, src}
		}
	}
	return nil
}

// Resolve 别名 → 计划。没有别名时，"provider/model" 或唯一匹配的模型名视作单项计划（每个模型自带同名别名）。
func (c *Catalog) Resolve(alias string) (Plan, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if a, ok := c.aliases[alias]; ok {
		return a.v, nil
	}
	if _, ok := c.models[alias]; ok {
		return Plan{{Model: alias}}, nil
	}
	var hit []string
	for k, m := range c.models {
		if m.v.Name == alias || strings.HasSuffix(k, "/"+alias) {
			hit = append(hit, k)
		}
	}
	if len(hit) == 1 {
		return Plan{{Model: hit[0]}}, nil
	}
	return nil, ErrUnknownAlias{alias}
}

// Lookup 计划项 → 模型 + 提供方。
func (c *Catalog) Lookup(ref string) (Model, Provider, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := ref
	if _, ok := c.models[key]; !ok {
		for k, m := range c.models {
			if m.v.Name == ref || strings.HasSuffix(k, "/"+ref) {
				key = k
				break
			}
		}
	}
	m, ok := c.models[key]
	if !ok {
		return Model{}, Provider{}, ErrUnknownAlias{ref}
	}
	p, ok := c.providers[m.v.Provider]
	if !ok {
		return Model{}, Provider{}, fmt.Errorf("modelhub: 模型 %s 的提供方 %s 未声明", key, m.v.Provider)
	}
	return m.v, p.v, nil
}

// MinWindow 计划内最小上下文窗口（压缩按它估算，plugin-manager §5.2）；未知返回 0。
func (c *Catalog) MinWindow(p Plan) int {
	min := 0
	for _, s := range p {
		if m, _, err := c.Lookup(s.Model); err == nil && m.ContextWindow > 0 && (min == 0 || m.ContextWindow < min) {
			min = m.ContextWindow
		}
	}
	return min
}

// Sources explain 用：每个键的来源。
func (c *Catalog) Sources() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for k, v := range c.providers {
		out = append(out, fmt.Sprintf("provider %s ← %s", k, v.src))
	}
	for k, v := range c.models {
		out = append(out, fmt.Sprintf("model %s ← %s", k, v.src))
	}
	for k, v := range c.aliases {
		out = append(out, fmt.Sprintf("alias %s ← %s", k, v.src))
	}
	sort.Strings(out)
	return out
}

// Entry 目录里可选的一项（界面切换模型用）：别名或 provider/model。
type Entry struct {
	ID            string // 设到会话上的值（别名名或 provider/model）
	Alias         bool
	Provider      string // 别名：计划第一项的提供方
	Name          string // 厂商模型名
	ContextWindow int
	Images        bool
	Reasoning     bool
}

// Entries 别名在前（按名排序），模型在后（按 provider/model 排序）。
func (c *Catalog) Entries() []Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry := func(id string, m Model, alias bool) Entry {
		e := Entry{ID: id, Alias: alias, Provider: m.Provider, Name: m.Name, ContextWindow: m.ContextWindow}
		if e.Name == "" {
			if _, n, ok := strings.Cut(id, "/"); ok && !alias {
				e.Name = n
			}
		}
		e.Images = m.Caps.Images != nil && *m.Caps.Images
		e.Reasoning = m.Caps.Reasoning != nil && *m.Caps.Reasoning
		return e
	}
	var al, ms []Entry
	for k, a := range c.aliases {
		var m Model
		if len(a.v) > 0 {
			if x, ok := c.models[a.v[0].Model]; ok {
				m = x.v
			}
		}
		al = append(al, entry(k, m, true))
	}
	for k, m := range c.models {
		ms = append(ms, entry(k, m.v, false))
	}
	sort.Slice(al, func(i, j int) bool { return al[i].ID < al[j].ID })
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
	return append(al, ms...)
}

// ProviderKey 提供方的凭证引用（不存在返回空串与 false）。
func (c *Catalog) ProviderKey(name string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.providers[name]
	return p.v.Key, ok
}
