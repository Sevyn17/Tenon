package permission

// 档位（06 - 工具权限设计方案 §9.4）：内置档位 read-only / ask / workspace / full 不是文件，
// 各权限项的取值由插件建议值生成（封顶表约束，见 caps.go）；自定义档位放在用户级 ~/.ybase/modes/
// 与受信项目的 <项目>/.ybase/modes/，只写覆盖项（[items]），可用 base 继承。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"ybase-kernel/core/toolhub"
)

// MaxBaseDepth base 继承链上限。
const MaxBaseDepth = 8

// ItemSandbox 沙箱级别权限项（level 型，由本插件登记）：会话里起子进程的工具（shell 等）用的沙箱。
const ItemSandbox = "sandbox.level"

// builtinMeta 内置档位的显示名与说明。
var builtinMeta = map[Mode][2]string{
	ReadOnly:  {"仅可查看", "读文件、搜索直接执行；写工作区、协作写拒绝；命令、联网、MCP 询问"},
	Ask:       {"每次询问", "读文件、搜索直接执行；写文件、执行命令、联网、MCP 每次询问"},
	Workspace: {"工作区允许", "工作区内读写、命令直接执行；联网、MCP 询问"},
	Full:      {"完全权限", "全部直接执行，命令不套沙箱"},
}

// builtinSandbox 内置档位的沙箱级别建议（sandbox.level 项的 SuggestLevel）。
var builtinSandbox = map[Mode]toolhub.SandboxMode{
	ReadOnly:  toolhub.SandboxReadOnly,
	Ask:       toolhub.SandboxWorkspaceWrite,
	Workspace: toolhub.SandboxWorkspaceWrite,
	Full:      toolhub.SandboxFull,
}

// SandboxItem 沙箱级别权限项（本插件登记到注册表）。
func SandboxItem() toolhub.Item {
	sl := map[string]string{}
	for m, v := range builtinSandbox {
		sl[string(m)] = string(v)
	}
	return toolhub.Item{ID: ItemSandbox, Title: "命令沙箱级别", Type: toolhub.ItemLevel, Class: toolhub.EffectExec,
		Levels:       []string{string(toolhub.SandboxReadOnly), string(toolhub.SandboxWorkspaceWrite), string(toolhub.SandboxFull)},
		SuggestLevel: sl, Owner: "permission"}
}

// ModeFile 档位文件格式。只认 name / base / description / [items]，其余键报错。
type ModeFile struct {
	Name        string `toml:"name"` // 显示名
	Base        string `toml:"base"`
	Description string `toml:"description"`
	// Items 按权限项取值（§9.4）：键是注册表里的项 id。
	Items map[string]ItemFile `toml:"items"`
}

// ItemFile 档位文件 [items] 段里一项的写法：
//
//	[items]
//	"shell.exec"    = { default = "ask", allow = ["go test:*"], deny = ["rm -rf:*"] }
//	"sandbox.level" = { level = "workspace-write" }
type ItemFile struct {
	Default string   `toml:"default"` // 未命中任何模式时的取值（switch 型就是整项取值）
	Level   string   `toml:"level"`   // 仅 level 型
	Allow   []string `toml:"allow"`
	Ask     []string `toml:"ask"`
	Deny    []string `toml:"deny"`
}

// ItemSetting 一个权限项在某档位的取值（含继承合并结果）。
type ItemSetting struct {
	Default toolhub.ItemAction
	Level   string
	Allow   []string
	Ask     []string
	Deny    []string
}

func (s ItemSetting) clone() ItemSetting {
	c := s
	c.Allow = append([]string(nil), s.Allow...)
	c.Ask = append([]string(nil), s.Ask...)
	c.Deny = append([]string(nil), s.Deny...)
	return c
}

// ModeDef 解析完（含继承）的档位。
type ModeDef struct {
	ID          Mode   // 文件名（不含 .toml），会话里存的就是它
	Title       string // 显示名
	Description string
	Source      string // builtin / 文件路径
	Builtin     bool
	// Project 来自受信项目的 .ybase/modes（放宽时首次选用须用户确认，§9.6）。
	Project bool
	// Items 按权限项取值（§9.4）：键是项 id。取不到的项由判定方回落到建议值。
	Items map[string]ItemSetting
	// Root 继承链上最近的内置档位（内置档位就是自己）；链上没有内置档位时为空，
	// 取不到值的项回落到“询问”（§9.4：不回落 allow）。
	Root Mode
	File ModeFile // 本文件原文（未合并 base）
}

// ItemOf 档位里某权限项的取值；档位没写该项时 ok=false（判定方回落建议值）。
func (d *ModeDef) ItemOf(id string) (ItemSetting, bool) {
	s, ok := d.Items[id]
	return s, ok
}

// SandboxLevel 本档的命令沙箱级别：[items] "sandbox.level" → 最近内置档位的建议 → workspace-write。
func (d *ModeDef) SandboxLevel() toolhub.SandboxMode {
	if s, ok := d.Items[ItemSandbox]; ok && s.Level != "" {
		return toolhub.SandboxMode(s.Level)
	}
	if m, ok := builtinSandbox[d.Root]; ok {
		return m
	}
	return toolhub.SandboxWorkspaceWrite
}

// Registry 全部档位。Dirs 依次为用户级、项目级目录（后者同名覆盖前者）。
type Registry struct {
	Dirs []string

	mu     sync.Mutex
	defs   map[Mode]*ModeDef
	stamps map[string]time.Time // 自定义档位文件 → mtime
}

// NewRegistry 读内置档位与 dirs 下的自定义档位。
func NewRegistry(dirs ...string) (*Registry, error) {
	r := &Registry{Dirs: dirs}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) load() error {
	files := map[Mode]ModeFile{}
	src := map[Mode]string{}
	builtin := map[Mode]bool{}
	for _, id := range Builtins { // 内置档位没有文件：取值全部回落插件建议值
		files[id], src[id], builtin[id] = ModeFile{Name: builtinMeta[id][0], Description: builtinMeta[id][1]}, "builtin", true
	}
	stamps := map[string]time.Time{}
	project := map[Mode]bool{}
	for di, dir := range r.Dirs {
		if dir == "" {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("permission: %s：%w", dir, err)
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			id := Mode(strings.TrimSuffix(e.Name(), ".toml"))
			if builtin[id] {
				return fmt.Errorf("permission: %s：不能覆盖内置档位 %s，请改名", p, id)
			}
			if err := checkID(string(id)); err != nil {
				return fmt.Errorf("permission: %s：%w", p, err)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			var f ModeFile
			md, err := toml.Decode(string(b), &f)
			if err != nil {
				return fmt.Errorf("permission: %s：%w", p, err)
			}
			if un := md.Undecoded(); len(un) > 0 {
				return fmt.Errorf("permission: %s：不认识的键 %s（档位文件只有 name / base / description / [items]）", p, un[0])
			}
			if fi, err := e.Info(); err == nil {
				stamps[p] = fi.ModTime()
			}
			files[id], src[id] = f, p
			project[id] = di > 0 // Dirs[0] 用户级，其后是项目级（见 ModeDirs）
		}
	}
	defs := map[Mode]*ModeDef{}
	for id := range files {
		d, err := resolve(id, files, 0)
		if err != nil {
			return fmt.Errorf("permission: 档位 %s（%s）：%w", id, src[id], err)
		}
		d.Source, d.Builtin, d.File, d.Project = src[id], builtin[id], files[id], project[id]
		defs[id] = d
	}
	r.mu.Lock()
	r.defs, r.stamps = defs, stamps
	r.mu.Unlock()
	return nil
}

func checkID(s string) error {
	if s == "" || strings.ContainsAny(s, `/\:*?"<>| `) {
		return fmt.Errorf("档位名 %q 不合法（不能含空格和 /\\:*?\"<>|）", s)
	}
	return nil
}

func resolve(id Mode, files map[Mode]ModeFile, depth int) (*ModeDef, error) {
	if depth >= MaxBaseDepth {
		return nil, fmt.Errorf("base 继承超过 %d 层或有环", MaxBaseDepth)
	}
	f, ok := files[id]
	if !ok {
		return nil, fmt.Errorf("base 档位 %s 不存在", id)
	}
	d := &ModeDef{ID: id, Items: map[string]ItemSetting{}}
	if f.Base != "" {
		b, err := resolve(Mode(f.Base), files, depth+1)
		if err != nil {
			return nil, err
		}
		for k, v := range b.Items {
			d.Items[k] = v.clone()
		}
		d.Title, d.Description = b.Title, b.Description
		d.Root = b.Root
	} else if _, ok := Caps[id]; ok {
		d.Root = id
	}
	if f.Name != "" || f.Base != "" {
		d.Title = f.Name
	}
	if f.Description != "" {
		d.Description = f.Description
	}
	if d.Title == "" {
		d.Title = string(id)
	}
	for k, v := range f.Items {
		s := d.Items[k] // base 的取值；没有就是零值
		if v.Default != "" {
			a, err := toolhub.ParseAction(v.Default)
			if err != nil {
				return nil, fmt.Errorf("[items] %s default：%w", k, err)
			}
			s.Default = a
		}
		if v.Level != "" {
			s.Level = v.Level
		}
		// 模式清单是追加：base 收紧过的条目不会被子档位悄悄丢掉。
		s.Allow = append(s.Allow, v.Allow...)
		s.Ask = append(s.Ask, v.Ask...)
		s.Deny = append(s.Deny, v.Deny...)
		d.Items[k] = s
	}
	return d, nil
}

// Get 取档位。
func (r *Registry) Get(id Mode) (*ModeDef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.defs[id]
	return d, ok
}

// List 全部档位：内置按固定顺序在前，自定义按名字排序。
func (r *Registry) List() []*ModeDef {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*ModeDef
	for _, id := range Builtins {
		if d := r.defs[id]; d != nil {
			out = append(out, d)
		}
	}
	var custom []*ModeDef
	for _, d := range r.defs {
		if !d.Builtin {
			custom = append(custom, d)
		}
	}
	sort.Slice(custom, func(i, j int) bool { return custom[i].ID < custom[j].ID })
	return append(out, custom...)
}

// Refresh 自定义档位文件有增删改时重读，返回变化的档位名。重读失败保留旧定义并返回错误。
func (r *Registry) Refresh() ([]Mode, error) {
	r.mu.Lock()
	old, oldStamps := r.defs, r.stamps
	r.mu.Unlock()
	cur := map[string]time.Time{}
	for _, dir := range r.Dirs {
		if dir == "" {
			continue
		}
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
				if fi, err := e.Info(); err == nil {
					cur[filepath.Join(dir, e.Name())] = fi.ModTime()
				}
			}
		}
	}
	same := len(cur) == len(oldStamps)
	for p, t := range cur {
		if !oldStamps[p].Equal(t) {
			same = false
		}
	}
	if same {
		return nil, nil
	}
	if err := r.load(); err != nil {
		r.mu.Lock()
		r.stamps = cur // 同一份坏文件不重复报错
		r.mu.Unlock()
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var changed []Mode
	for id, d := range r.defs {
		if o := old[id]; o == nil || o.Source != d.Source || !oldStamps[d.Source].Equal(r.stamps[d.Source]) {
			changed = append(changed, id)
		}
	}
	for id := range old {
		if r.defs[id] == nil {
			changed = append(changed, id)
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i] < changed[j] })
	return changed, nil
}

// Copy 把档位 src 复制成 dir 下的 <dst>.toml：写 base = src 与完整的 [effects]、[sandbox] 表。返回文件路径。
func (r *Registry) Copy(src, dst Mode, dir string) (string, error) {
	d, ok := r.Get(src)
	if !ok {
		return "", fmt.Errorf("permission: 档位 %s 不存在", src)
	}
	if err := checkID(string(dst)); err != nil {
		return "", fmt.Errorf("permission: %w", err)
	}
	if _, ok := r.Get(dst); ok {
		return "", fmt.Errorf("permission: 档位 %s 已存在", dst)
	}
	if dir == "" {
		return "", fmt.Errorf("permission: 没有可写的档位目录")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "name = %q\nbase = %q\ndescription = %q\n\n", string(dst), string(src), "复制自 "+d.Title)
	b.WriteString("# 只写要覆盖的权限项；没写的项沿 base 继承（ybase mode show " + string(src) + " 看完整取值表）。\n[items]\n")
	ids := make([]string, 0, len(d.Items))
	for id := range d.Items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(&b, "# %q = %s\n", id, itemLine(d.Items[id]))
	}
	b.WriteString("# \"shell.exec\"    = { default = \"ask\", allow = [\"go test:*\"], deny = [\"rm -rf:*\"] }\n")
	b.WriteString("# \"web.fetch\"     = { default = \"ask\", allow = [\"pkg.go.dev\"] }\n")
	b.WriteString("# \"sandbox.level\" = { level = \"workspace-write\" }\n")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, string(dst)+".toml")
	if _, err := os.Stat(p); err == nil {
		return "", fmt.Errorf("permission: %s 已存在", p)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	if err := r.load(); err != nil {
		return p, err
	}
	return p, nil
}

// builtinReg 只含内置档位的注册表（Policy.Def 为空时兜底）。
var builtinReg = sync.OnceValue(func() *Registry {
	r, err := NewRegistry()
	if err != nil {
		panic("permission: 内置档位无效: " + err.Error())
	}
	return r
})

func builtinDef(id Mode) *ModeDef {
	d, _ := builtinReg().Get(id)
	return d
}

// itemLine 一项取值的 TOML 内联表写法（Copy 模板注释用）。
func itemLine(s ItemSetting) string {
	var parts []string
	if s.Default != "" {
		parts = append(parts, fmt.Sprintf("default = %q", string(s.Default)))
	}
	if s.Level != "" {
		parts = append(parts, fmt.Sprintf("level = %q", s.Level))
	}
	for _, g := range []struct {
		k string
		v []string
	}{{"allow", s.Allow}, {"ask", s.Ask}, {"deny", s.Deny}} {
		if len(g.v) > 0 {
			q := make([]string, len(g.v))
			for i, x := range g.v {
				q[i] = fmt.Sprintf("%q", x)
			}
			parts = append(parts, g.k+" = ["+strings.Join(q, ", ")+"]")
		}
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
