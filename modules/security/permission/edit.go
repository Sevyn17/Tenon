package permission

// 在界面里编辑档位（Mortise docs/51 第 6 节）：保存 / 删除用户级自定义档位（<home>/modes/<id>.toml，
// 格式同 mode copy 写出的文件），改用户级 permissions.toml 的 mode（新会话缺省档位）。
// 内置档位、项目档位只读。并发校验按文件内容版本（Revision，同 mcp.save）。
// 本包不依赖 appapi：错误用 *EditError 带种类，由适配层换成协议错误码。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// EditError 的种类。
const (
	EditInvalid     = "invalid"
	EditNotFound    = "not_found"
	EditStale       = "stale"  // revision 与文件当前版本不同
	EditExists      = "exists" // create 时同名档位已存在
	EditUnsupported = "unsupported"
)

// EditError 编辑档位失败。Revision 是 stale / exists 时文件的当前版本。
type EditError struct {
	Kind     string
	Msg      string
	Revision string
}

func (e *EditError) Error() string { return e.Msg }

func editErr(kind, format string, args ...any) *EditError {
	return &EditError{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// ModeEdit 一份要保存的自定义档位（字段同 ModeFile）。
type ModeEdit struct {
	ID          Mode
	Name        string
	Base        string
	Description string
	Items       map[string]ItemFile
}

// editMu 串行化对用户级档位目录与 permissions.toml 的写入（进程内）。
var editMu sync.Mutex

// FileRevision 文件内容的版本号（sha256 前 8 字节十六进制）；文件不存在为空串。
func FileRevision(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return revisionOf(b)
}

func revisionOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Editable 能否在界面里编辑档位：有用户级目录且注册表是本服务自己的。
func (s *Sessions) Editable() bool { return s.UserHome != "" && s.Reg != nil }

// UserModeDir 用户级自定义档位目录；不可编辑时为空。
func (s *Sessions) UserModeDir() string {
	if !s.Editable() {
		return ""
	}
	return filepath.Join(s.UserHome, "modes")
}

// UserConfigPath 用户级 permissions.toml；不可编辑时为空。
func (s *Sessions) UserConfigPath() string {
	if !s.Editable() {
		return ""
	}
	return filepath.Join(s.UserHome, "permissions.toml")
}

// ModePath 用户级档位文件的路径（不论存在与否）。
func (s *Sessions) ModePath(id Mode) string {
	if d := s.UserModeDir(); d != "" {
		return filepath.Join(d, string(id)+".toml")
	}
	return ""
}

// ModeRevision 自定义档位文件的版本号（内置档位、文件不存在为空）。
func (s *Sessions) ModeRevision(id Mode) string {
	d, ok := s.reg().Get(id)
	if !ok || d.Builtin {
		return ""
	}
	return FileRevision(d.Source)
}

// UserEditable 档位 id 是不是用户级自定义档位（能改、能删）。
func (s *Sessions) UserEditable(id Mode) bool {
	d, ok := s.reg().Get(id)
	return ok && !d.Builtin && !d.Project && s.Editable() && filepath.Clean(d.Source) == filepath.Clean(s.ModePath(id))
}

func isBuiltin(id Mode) bool {
	for _, b := range Builtins {
		if b == id {
			return true
		}
	}
	return false
}

func checkStale(path, revision string) error {
	if revision == "" {
		return nil
	}
	if cur := FileRevision(path); cur != revision {
		e := editErr(EditStale, "档位文件在别处改过了（版本 %q，你看到的是 %q）：重新打开再改", cur, revision)
		e.Revision = cur
		return e
	}
	return nil
}

// bump 档位定义变了：用着它的会话下一回合收到“配置已更新”。
func (s *Sessions) bump(ids ...Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen == nil {
		s.gen = map[Mode]int{}
	}
	for _, id := range ids {
		s.gen[id]++
	}
}

func validText(what, v string) error {
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return editErr(EditInvalid, "%s 不能含控制字符", what)
		}
	}
	return nil
}

// checkEdit 字段校验（不读文件）。
func (s *Sessions) checkEdit(e ModeEdit) error {
	if isBuiltin(e.ID) {
		return editErr(EditInvalid, "%s 是内置档位，不能修改（先复制成自定义档位）", e.ID)
	}
	if err := checkID(string(e.ID)); err != nil {
		return editErr(EditInvalid, "%v", err)
	}
	for what, v := range map[string]string{"name": e.Name, "description": e.Description, "base": e.Base} {
		if err := validText(what, v); err != nil {
			return err
		}
	}
	if e.Base != "" {
		if Mode(e.Base) == e.ID {
			return editErr(EditInvalid, "base 不能是自己")
		}
		if _, ok := s.reg().Get(Mode(e.Base)); !ok {
			return editErr(EditInvalid, "base 档位 %s 不存在", e.Base)
		}
	}
	known := map[string]toolhub.Item{}
	for _, it := range s.itemsSnapshot() {
		known[it.ID] = it
	}
	for id, v := range e.Items {
		if id == "" || strings.ContainsAny(id, "\"\\") {
			return editErr(EditInvalid, "权限项 id %q 不合法", id)
		}
		if err := validText("权限项 id", id); err != nil {
			return err
		}
		if v.Default != "" {
			if _, err := toolhub.ParseAction(v.Default); err != nil {
				return editErr(EditInvalid, "权限项 %s：%v", id, err)
			}
		}
		it, isKnown := known[id]
		if v.Level != "" {
			if isKnown && !hasLevel(it.Levels, v.Level) {
				return editErr(EditInvalid, "权限项 %s 的级别 %q 不在 %v 内", id, v.Level, it.Levels)
			}
			if err := validText("级别", v.Level); err != nil {
				return err
			}
		}
		if isKnown && it.Type == toolhub.ItemSwitch && len(v.Allow)+len(v.Ask)+len(v.Deny) > 0 {
			return editErr(EditInvalid, "权限项 %s 是开关型，不能带规则", id)
		}
		for _, xs := range [][]string{v.Allow, v.Ask, v.Deny} {
			for _, p := range xs {
				if strings.TrimSpace(p) == "" {
					return editErr(EditInvalid, "权限项 %s 有空规则", id)
				}
				if err := validText("规则", p); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func hasLevel(levels []string, v string) bool {
	for _, l := range levels {
		if l == v {
			return true
		}
	}
	return false
}

// encodeMode 档位文件正文（格式同 Copy：name / base / description + [items] 内联表）。
func encodeMode(e ModeEdit) string {
	var b strings.Builder
	name := e.Name
	if name == "" {
		name = string(e.ID)
	}
	fmt.Fprintf(&b, "name = %q\n", name)
	if e.Base != "" {
		fmt.Fprintf(&b, "base = %q\n", e.Base)
	}
	if e.Description != "" {
		fmt.Fprintf(&b, "description = %q\n", e.Description)
	}
	b.WriteString("\n# 由设置页保存。只写要覆盖的权限项；没写的项沿 base 继承。\n[items]\n")
	ids := make([]string, 0, len(e.Items))
	for id := range e.Items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		v := e.Items[id]
		if v.Default == "" && v.Level == "" && len(v.Allow)+len(v.Ask)+len(v.Deny) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%q = %s\n", id, itemLine(ItemSetting{Default: toolhub.ItemAction(v.Default), Level: v.Level,
			Allow: v.Allow, Ask: v.Ask, Deny: v.Deny}))
	}
	return b.String()
}

// SaveMode 新建或整份替换用户级自定义档位 e.ID。create 为真时同名档位已存在 → exists；
// revision 非空且与文件当前版本不同 → stale。写完重读注册表，重读失败（如 base 成环）时改回原文件。
// 返回新版本号。
func (s *Sessions) SaveMode(e ModeEdit, revision string, create bool) (string, error) {
	if !s.Editable() {
		return "", editErr(EditUnsupported, "没有用户级目录，不能保存档位")
	}
	if err := s.checkEdit(e); err != nil {
		return "", err
	}
	editMu.Lock()
	defer editMu.Unlock()
	p := s.ModePath(e.ID)
	if d, ok := s.reg().Get(e.ID); ok {
		if create {
			ee := editErr(EditExists, "档位 %s 已存在，换个名字", e.ID)
			ee.Revision = s.ModeRevision(e.ID)
			return "", ee
		}
		if !s.UserEditable(e.ID) {
			return "", editErr(EditInvalid, "档位 %s 来自 %s，只读", e.ID, d.Source)
		}
	}
	if err := checkStale(p, revision); err != nil {
		return "", err
	}
	old, rerr := os.ReadFile(p)
	existed := rerr == nil
	text := []byte(encodeMode(e))
	if err := writeAtomic(p, text, 0o644); err != nil {
		return "", err
	}
	if err := s.Reg.load(); err != nil {
		if existed {
			_ = writeAtomic(p, old, 0o644)
		} else {
			_ = os.Remove(p)
		}
		_ = s.Reg.load()
		return "", editErr(EditInvalid, "%v", err)
	}
	s.bump(e.ID)
	return revisionOf(text), nil
}

// RemoveMode 删除用户级自定义档位。内置 / 项目档位 → invalid，不存在 → not_found。
// 有别的档位以它为 base 时重读失败，文件改回去并返回 invalid。
// 用户级 permissions.toml 的 mode 正是它时改回内置缺省档位，fellBack 为真。
func (s *Sessions) RemoveMode(id Mode, revision string) (fellBack bool, err error) {
	if !s.Editable() {
		return false, editErr(EditUnsupported, "没有用户级目录，不能删除档位")
	}
	if isBuiltin(id) {
		return false, editErr(EditInvalid, "%s 是内置档位，不能删除", id)
	}
	editMu.Lock()
	defer editMu.Unlock()
	d, ok := s.reg().Get(id)
	if !ok {
		return false, editErr(EditNotFound, "没有档位 %q", id)
	}
	if !s.UserEditable(id) {
		return false, editErr(EditInvalid, "档位 %s 来自 %s，只读", id, d.Source)
	}
	p := d.Source
	if err := checkStale(p, revision); err != nil {
		return false, err
	}
	old, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	if err := os.Remove(p); err != nil {
		return false, err
	}
	if err := s.Reg.load(); err != nil {
		_ = writeAtomic(p, old, 0o644)
		_ = s.Reg.load()
		return false, editErr(EditInvalid, "删不了：%v", err)
	}
	s.bump(id)
	cp := s.UserConfigPath()
	c, _ := LoadConfig(cp)
	if Mode(c.Mode) == id {
		if err := writeConfigMode(cp, Default); err != nil {
			return false, err
		}
		fellBack = true
	}
	s.mu.Lock()
	if s.Default == id {
		s.Default = Default
		fellBack = true
	}
	s.mu.Unlock()
	return fellBack, nil
}

// SetDefaultMode 把用户级 permissions.toml 的 mode 设为 id（文件里别的内容原样保留），新会话用它。
// 档位不存在 → not_found。启动参数强制了档位、或受信项目的 permissions.toml 设了 mode 时，写入照常，
// 但当前进程的缺省档位不变，pinned 说明原因。
func (s *Sessions) SetDefaultMode(id Mode) (pinned string, err error) {
	if !s.Editable() {
		return "", editErr(EditUnsupported, "没有用户级目录，不能设缺省档位")
	}
	if _, ok := s.reg().Get(id); !ok {
		return "", editErr(EditNotFound, "没有档位 %q", id)
	}
	editMu.Lock()
	defer editMu.Unlock()
	if err := writeConfigMode(s.UserConfigPath(), id); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Pinned != "" {
		return s.Pinned, nil
	}
	s.Default = id
	return "", nil
}

// topMode permissions.toml 顶层的 mode = 行（第一个表头之前）。
var topMode = regexp.MustCompile(`^\s*mode\s*=`)

// writeConfigMode 改写（或插入）permissions.toml 顶层的 mode 行；改完校验整份文件仍合法再写。
func writeConfigMode(path string, id Mode) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	line := fmt.Sprintf("mode = %q", string(id))
	lines := strings.Split(string(b), "\n")
	done := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			break
		}
		if topMode.MatchString(l) {
			lines[i], done = line, true
			break
		}
	}
	text := strings.Join(lines, "\n")
	if !done {
		if len(b) == 0 {
			text = line + "\n"
		} else {
			text = line + "\n" + text
		}
	}
	var c Config
	if _, err := toml.Decode(text, &c); err != nil {
		return editErr(EditInvalid, "%s 无法解析，没有改动：%v", path, err)
	}
	if err := checkConfig(c, path); err != nil {
		return editErr(EditInvalid, "%v", err)
	}
	return writeAtomic(path, []byte(text), 0o644)
}

// writeAtomic 写临时文件再改名；已有文件保留原权限。
func writeAtomic(p string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if st, err := os.Stat(p); err == nil {
		perm = st.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".perm-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, perm)
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
