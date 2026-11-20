package apphost

// MCP 管理页写定义（客户端改进 M11，配置T14）：WorkspaceManager 实现 appapi.MCPEditor，改用户级 <home>/mcp.toml。
// service:mcp 只读；写文件放在应用宿主这一层。重建由 appapi 调 Reload（全局范围、定义没变的实例不重启）。
//
// 写法：按 [[server]] 段做文本替换，别的段（含 [server.env] 这类子表）、段外的注释与空行原样保留；被替换的那一段
// 整段重写，段内注释会丢。改完后把整个文件重新解码，核对服务列表与预期一致；对不上（例如文件里有本实现认不出的写法）
// 时退回整个文件重新生成（注释全丢，文件头写一行说明）。写入 = 同目录临时文件 + rename。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/adapters"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/modules/tools/mcp"
)

var _ appapi.MCPEditor = (*WorkspaceManager)(nil)

var mcpFileMu sync.Mutex // 同一进程里读改写用户级 mcp.toml 串行

func mcpUserFile() (string, error) {
	p := mcp.UserConfigPath()
	if p == "" {
		return "", errors.New("取不到用户目录，无法保存 MCP 定义")
	}
	return p, nil
}

// readMCPFile 读文件内容与解析出的服务（不存在 = 空）。解析失败返回 invalid：不覆盖坏文件。
func readMCPFile(p string) ([]byte, []mcp.Server, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var c mcp.Config
	if _, err := toml.Decode(string(b), &c); err != nil {
		return nil, nil, appapi.Errorf(wire.CodeInvalid, "%s 解析失败，先手工修好再在界面里改：%v", p, err)
	}
	return b, c.Server, nil
}

func findServer(ss []mcp.Server, name string) (mcp.Server, bool) {
	for _, s := range ss {
		if s.Name == name {
			return s, true
		}
	}
	return mcp.Server{}, false
}

// mergeValues 按 wire.MCPServerDraft 的规则合并 env / headers：字符串 = 新值，null = 保留原值，没提到的保留，
// remove 里的删除。结果每个键都有值。
func mergeValues(name, field string, old map[string]string, in map[string]*string, remove []string) (map[string]*string, error) {
	out := map[string]*string{}
	for k, v := range old {
		out[k] = &v
	}
	for _, k := range remove {
		if _, dup := in[k]; dup {
			return nil, appapi.Errorf(wire.CodeInvalid, "服务 %s 的 %s.%s 同时给了值又要删除", name, field, k)
		}
		delete(out, k)
	}
	for k, v := range in {
		if v == nil {
			if _, ok := old[k]; !ok {
				return nil, appapi.Errorf(wire.CodeInvalid, "服务 %s 的 %s.%s 为 null（保留原值），但已保存的定义里没有这个键", name, field, k)
			}
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (m *WorkspaceManager) resolveDraft(d wire.MCPServerDraft) (wire.MCPServerDraft, mcp.Server, error) {
	p, err := mcpUserFile()
	if err != nil {
		return d, mcp.Server{}, err
	}
	_, ss, err := readMCPFile(p)
	if err != nil {
		return d, mcp.Server{}, err
	}
	d.Name = strings.TrimSpace(d.Name)
	old, _ := findServer(ss, d.Name)
	if d.Env, err = mergeValues(d.Name, "env", old.Env, d.Env, d.EnvRemove); err != nil {
		return d, mcp.Server{}, err
	}
	if d.Headers, err = mergeValues(d.Name, "headers", old.Headers, d.Headers, d.HeaderRemove); err != nil {
		return d, mcp.Server{}, err
	}
	d.EnvRemove, d.HeaderRemove = nil, nil
	s, err := adapters.DraftServer(d)
	if err != nil {
		return d, mcp.Server{}, err
	}
	return d, s, nil
}

// ResolveMCPDraft 见 appapi.MCPEditor。
func (m *WorkspaceManager) ResolveMCPDraft(_ context.Context, d wire.MCPServerDraft) (wire.MCPServerDraft, error) {
	mcpFileMu.Lock()
	defer mcpFileMu.Unlock()
	out, _, err := m.resolveDraft(d)
	return out, err
}

// checkRevision revision 非空且与 b 的版本不同 → conflict（details.reason = stale，details.revision = 当前版本）。
func checkRevision(b []byte, revision string) error {
	cur := mcp.Revision(b)
	if revision != "" && revision != cur {
		e := appapi.Errorf(wire.CodeConflict, "mcp.toml 在别处改过了（版本 %s，你看到的是 %s）：重新打开列表再改", cur, revision)
		e.Details = map[string]any{"reason": wire.ConflictStale, "revision": cur}
		return e
	}
	return nil
}

// SaveMCPServer 见 appapi.MCPEditor。
func (m *WorkspaceManager) SaveMCPServer(_ context.Context, d wire.MCPServerDraft, revision string) (appapi.MCPWrite, error) {
	mcpFileMu.Lock()
	defer mcpFileMu.Unlock()
	p, err := mcpUserFile()
	if err != nil {
		return appapi.MCPWrite{}, err
	}
	b, ss, err := readMCPFile(p)
	if err != nil {
		return appapi.MCPWrite{}, err
	}
	if err := checkRevision(b, revision); err != nil {
		return appapi.MCPWrite{}, err
	}
	_, s, err := m.resolveDraft(d)
	if err != nil {
		return appapi.MCPWrite{}, err
	}
	want := slices.Clone(ss)
	if i := slices.IndexFunc(want, func(x mcp.Server) bool { return x.Name == s.Name }); i >= 0 {
		want[i] = s
	} else {
		want = append(want, s)
	}
	return writeMCPFile(p, b, editServers(b, s.Name, encodeServer(s)), want)
}

// RemoveMCPServer 见 appapi.MCPEditor。
func (m *WorkspaceManager) RemoveMCPServer(_ context.Context, name, revision string) (appapi.MCPWrite, error) {
	mcpFileMu.Lock()
	defer mcpFileMu.Unlock()
	p, err := mcpUserFile()
	if err != nil {
		return appapi.MCPWrite{}, err
	}
	b, ss, err := readMCPFile(p)
	if err != nil {
		return appapi.MCPWrite{}, err
	}
	if err := checkRevision(b, revision); err != nil {
		return appapi.MCPWrite{}, err
	}
	if _, ok := findServer(ss, name); !ok {
		return appapi.MCPWrite{}, appapi.Errorf(wire.CodeNotFound, "用户级 mcp.toml 里没有服务 %s", name)
	}
	want := slices.DeleteFunc(slices.Clone(ss), func(x mcp.Server) bool { return x.Name == name })
	return writeMCPFile(p, b, editServers(b, name, ""), want)
}

// writeMCPFile 核对 text 解码出的服务与 want 一致（不一致就整份重新生成），原子写入，返回带 Undo 的结果。
func writeMCPFile(p string, old, text []byte, want []mcp.Server) (appapi.MCPWrite, error) {
	var c mcp.Config
	if _, err := toml.Decode(string(text), &c); err != nil || !sameServers(c.Server, want) {
		var buf bytes.Buffer
		buf.WriteString("# MCP 服务定义（mcp 管理页重新生成：原文件的写法无法逐段替换，注释没有保留）。\n")
		for _, s := range want {
			buf.WriteString("\n" + encodeServer(s))
		}
		text = buf.Bytes()
	}
	existed := old != nil
	if !existed {
		if _, err := os.Stat(p); err == nil {
			existed = true
		}
	}
	if err := writeFileAtomic(p, text); err != nil {
		return appapi.MCPWrite{}, err
	}
	undo := func() error {
		mcpFileMu.Lock()
		defer mcpFileMu.Unlock()
		if !existed {
			return os.Remove(p)
		}
		return writeFileAtomic(p, old)
	}
	return appapi.MCPWrite{Revision: mcp.Revision(text), Undo: undo}, nil
}

func sameServers(a, b []mcp.Server) bool {
	if len(a) != len(b) {
		return false
	}
	norm := func(s mcp.Server) mcp.Server {
		s.Source = ""
		if len(s.Args) == 0 {
			s.Args = nil
		}
		if len(s.Env) == 0 {
			s.Env = nil
		}
		if len(s.Headers) == 0 {
			s.Headers = nil
		}
		return s
	}
	for i := range a {
		if !reflect.DeepEqual(norm(a[i]), norm(b[i])) {
			return false
		}
	}
	return true
}

// writeFileAtomic 同目录临时文件 + rename；新文件权限 0600（可能有密钥），已有文件保留原权限。
func writeFileAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".mcp-*.tmp")
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
	_ = os.Chmod(tmp, mode)
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// —— 逐段替换 ——

// tomlHeader 表头行：[name] 或 [[name]]，可带行尾注释。
var tomlHeader = regexp.MustCompile(`^\s*(\[\[?)\s*([^\[\]]+?)\s*\]\]?\s*(#.*)?$`)

// editServers 把 text 里名为 name 的 [[server]] 段（含它的 [server.xxx] 子表）换成 repl（空 = 删掉）；
// 没有这一段时把 repl 追加到文件末尾。段尾紧挨下一个表头的注释与空行算段外内容，保留。
func editServers(text []byte, name, repl string) []byte {
	lines := strings.SplitAfter(string(text), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	type span struct{ from, to int } // [from, to)
	var target *span
	cur := -1 // 当前 [[server]] 段的起始行；-1 = 不在 server 段里
	end := func(at int) {
		if cur < 0 {
			return
		}
		to := at
		for to > cur+1 { // 段尾的注释、空行留在段外
			t := strings.TrimSpace(lines[to-1])
			if t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			to--
		}
		if target == nil && sectionName(strings.Join(lines[cur:to], "")) == name {
			target = &span{cur, to}
		}
		cur = -1
	}
	for i, l := range lines {
		mm := tomlHeader.FindStringSubmatch(strings.TrimRight(l, "\r\n"))
		if mm == nil {
			continue
		}
		key := strings.TrimSpace(mm[2])
		if mm[1] == "[[" && key == "server" {
			end(i)
			cur = i
			continue
		}
		if strings.HasPrefix(key, "server.") && cur >= 0 { // 本段的子表
			continue
		}
		end(i)
	}
	end(len(lines))
	if target == nil {
		if repl == "" {
			return text
		}
		out := string(text)
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if out != "" && !strings.HasSuffix(out, "\n\n") {
			out += "\n"
		}
		return []byte(out + repl)
	}
	var b strings.Builder
	for _, l := range lines[:target.from] {
		b.WriteString(l)
	}
	b.WriteString(repl)
	rest := lines[target.to:]
	if repl == "" { // 删掉的段后面多出来的空行收一下
		for len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
			rest = rest[1:]
		}
	} else if len(rest) > 0 && strings.TrimSpace(rest[0]) != "" {
		b.WriteString("\n")
	}
	for _, l := range rest {
		b.WriteString(l)
	}
	return []byte(b.String())
}

// sectionName 一个 [[server]] 段的 name；解析不了返回空。
func sectionName(sec string) string {
	var c mcp.Config
	if _, err := toml.Decode(sec, &c); err != nil || len(c.Server) != 1 {
		return ""
	}
	return c.Server[0].Name
}

// encodeServer 一个 [[server]] 段（以换行结尾）。env / headers 写成行内表。
func encodeServer(s mcp.Server) string {
	var b strings.Builder
	b.WriteString("[[server]]\n")
	kv := func(k, v string) { fmt.Fprintf(&b, "%s = %s\n", k, v) }
	kv("name", tomlString(s.Name))
	if s.Command != "" {
		kv("command", tomlString(s.Command))
	}
	if len(s.Args) > 0 {
		qs := make([]string, len(s.Args))
		for i, a := range s.Args {
			qs[i] = tomlString(a)
		}
		kv("args", "["+strings.Join(qs, ", ")+"]")
	}
	if s.URL != "" {
		kv("url", tomlString(s.URL))
	}
	if s.Cwd != "" {
		kv("cwd", tomlString(s.Cwd))
	}
	if s.Timeout > 0 {
		kv("timeout", strconv.Itoa(s.Timeout))
	}
	for _, f := range [][2]string{{"trust", s.Trust}, {"sandbox", s.Sandbox}, {"scope", s.Scope}} {
		if f[1] != "" {
			kv(f[0], tomlString(f[1]))
		}
	}
	if len(s.Env) > 0 {
		kv("env", inlineTable(s.Env))
	}
	if len(s.Headers) > 0 {
		kv("headers", inlineTable(s.Headers))
	}
	return b.String()
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func inlineTable(m map[string]string) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	parts := make([]string, len(ks))
	for i, k := range ks {
		key := k
		if !bareKey.MatchString(k) {
			key = tomlString(k)
		}
		parts[i] = key + " = " + tomlString(m[k])
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// tomlString TOML 基本字符串（双引号；反斜杠、引号、控制字符转义）。
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
