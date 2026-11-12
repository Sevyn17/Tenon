package profile

// 改写一个 profile 文件里某个插件的开关与配置（plugins.set）：只动这个插件的 [[plugin]] 行，
// include、mandatory、[kernel] 与其他插件原样保留。TOML 库不保留注释，所以整份重写；写之前把原文件存成 .bak。
// 并发：调用方拿 Revision（文件内容的 sha256）做条件写，文件在两次之间被改过返回 component.ErrRevision。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/component"
)

// Revision 文件内容的 sha256（十六进制）；文件不存在返回空串。
func Revision(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hash(b), nil
}

// Edit 按 e 改写 path；ifRevision 非 nil 时文件当前的 Revision 必须等于它（空串 = 文件不存在）。
// 文件不存在时新建。返回新的 Revision。
func Edit(path string, e component.PluginEdit, ifRevision *string) (string, error) {
	if e.Name == "" {
		return "", errors.New("缺少插件名")
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	exists := err == nil
	if ifRevision != nil {
		cur := ""
		if exists {
			cur = hash(old)
		}
		if cur != *ifRevision {
			return "", fmt.Errorf("%w：%s", component.ErrRevision, path)
		}
	}
	var f profileFile
	if exists {
		md, err := toml.Decode(string(old), &f)
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		if und := md.Undecoded(); len(und) > 0 {
			return "", fmt.Errorf("%s: 未知字段 %v", path, und)
		}
	}
	i := -1
	for k := range f.Plugin {
		if f.Plugin[k].Name == e.Name {
			i = k
			break
		}
	}
	if i < 0 {
		f.Plugin = append(f.Plugin, profileItem{Name: e.Name})
		i = len(f.Plugin) - 1
	}
	it := &f.Plugin[i]
	if e.Enabled != nil {
		v := *e.Enabled
		it.Enabled, it.Disabled = &v, false
	}
	for k, v := range e.Config {
		if v == nil {
			delete(it.Config, k)
			continue
		}
		if it.Config == nil {
			it.Config = map[string]any{}
		}
		it.Config[k] = v
	}
	if it.Enabled == nil && !it.Disabled && len(it.Config) == 0 { // 什么都没写了：去掉这一行
		f.Plugin = append(f.Plugin[:i], f.Plugin[i+1:]...)
	}
	var buf bytes.Buffer
	buf.WriteString("# 由 Tenon 设置页改写（plugins.set）：原文件已备份为 profile.toml.bak，注释不保留。\n\n")
	if err := toml.NewEncoder(&buf).Encode(fileOut(f)); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if exists {
		if err := os.WriteFile(path+".bak", old, 0o600); err != nil {
			return "", err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return hash(buf.Bytes()), nil
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// 读写用的形状与 runtime/component 的 profile 文件一致。内核的类型不导出，
// 这里只为改写文件复制一份；字段必须与内核保持一致，否则 plugins.set 会丢字段。
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

// 写出用的形状：空字段不写（omitempty），键名与读取一致。
type profileOut struct {
	Include   []string       `toml:"include,omitempty"`
	Mandatory []string       `toml:"mandatory,omitempty"`
	Kernel    map[string]any `toml:"kernel,omitempty"`
	Plugin    []itemOut      `toml:"plugin,omitempty"`
}

type itemOut struct {
	Name    string         `toml:"name"`
	Enabled *bool          `toml:"enabled,omitempty"`
	Config  map[string]any `toml:"config,omitempty"`
}

func fileOut(f profileFile) profileOut {
	out := profileOut{Include: f.Include, Mandatory: f.Mandatory, Kernel: f.Kernel}
	for _, it := range f.Plugin {
		o := itemOut{Name: it.Name, Enabled: it.Enabled, Config: it.Config}
		if it.Disabled && it.Enabled == nil { // 旧写法 disabled = true 统一成 enabled = false
			off := false
			o.Enabled = &off
		}
		out.Plugin = append(out.Plugin, o)
	}
	return out
}
