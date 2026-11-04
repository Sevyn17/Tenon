package instructions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ybase/runtime/session"
	"ybase/runtime/toolhub"
)

// AGENTS.md 默认预算（docs/90 - 归档/20 - 插件/01 - AGENTS.md 指令.md）。
const (
	AgentsMaxBytes = 64 << 10
	AgentsMaxFile  = 32 << 10
)

// Instructions 项目指令（AGENTS.md）装载：会话基线 + 工具到达更深目录时追加。
type Instructions struct {
	Workdir  string
	Home     string // 用户全局目录（含 AGENTS.md）；空 = 不读
	MaxBytes int
	MaxFile  int

	mu     sync.Mutex
	loaded map[string]bool // 已装载文件（绝对路径）
	bodies map[string]bool // 已装载内容（去首尾空白），重复跳过
	used   int
}

// NewInstructions 以工作目录构造；Home 取 $YBASE_HOME 或 ~/.ybase。
func NewInstructions(workdir string) *Instructions {
	workdir, _ = filepath.Abs(workdir)
	home := os.Getenv("YBASE_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".ybase")
		}
	}
	return &Instructions{Workdir: workdir, Home: home}
}

func (in *Instructions) init() {
	if in.loaded == nil {
		in.loaded, in.bodies = map[string]bool{}, map[string]bool{}
	}
	if in.MaxBytes <= 0 {
		in.MaxBytes = AgentsMaxBytes
	}
	if in.MaxFile <= 0 {
		in.MaxFile = AgentsMaxFile
	}
}

// projectRoot 向上找 .git；找不到返回工作目录。“不存在”以外的错误直接返回。
func projectRoot(dir string) (string, error) {
	for d := dir; ; {
		_, err := os.Stat(filepath.Join(d, ".git"))
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("探测项目根 %s: %w", d, err)
		}
		p := filepath.Dir(d)
		if p == d {
			return dir, nil
		}
		d = p
	}
}

// chain 从 from（含）到 to（含）的目录序列；to 必须在 from 之下。
func chain(from, to string) []string {
	rel, err := filepath.Rel(from, to)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	out := []string{from}
	if rel == "." {
		return out
	}
	d := from
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		d = filepath.Join(d, seg)
		out = append(out, d)
	}
	return out
}

// dirFiles 一层目录的候选：AGENTS.md（没有则 CLAUDE.md），再叠加 AGENTS.local.md。
func dirFiles(dir string) []string {
	var out []string
	for _, n := range []string{"AGENTS.md", "CLAUDE.md"} {
		p := filepath.Join(dir, n)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			out = append(out, p)
			break
		}
	}
	p := filepath.Join(dir, "AGENTS.local.md")
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		out = append(out, p)
	}
	return out
}

// render 装载尚未装载的文件，返回渲染文本（调用方持锁）。
func (in *Instructions) render(files []string) string {
	var b strings.Builder
	for _, p := range files {
		if in.loaded[p] {
			continue
		}
		in.loaded[p] = true
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		body := strings.TrimSpace(string(data))
		if body == "" || in.bodies[body] {
			continue
		}
		in.bodies[body] = true
		cut := false
		if len(body) > in.MaxFile {
			body, cut = clipUTF8(body, in.MaxFile), true
		}
		label := p
		if r, err := filepath.Rel(in.Workdir, p); err == nil {
			label = filepath.ToSlash(r)
		}
		head := fmt.Sprintf("# 来自 %s\n\n", label)
		left := in.MaxBytes - in.used
		if left <= len(head) {
			b.WriteString(fmt.Sprintf("（%s 等指令文件超出 %dKB 预算，未装载）\n", label, in.MaxBytes>>10))
			in.used = in.MaxBytes
			break
		}
		if len(head)+len(body) > left {
			body, cut = clipUTF8(body, left-len(head)), true
		}
		if cut {
			body += "\n…（已截断）"
		}
		b.WriteString(head + body + "\n\n")
		in.used += len(head) + len(body)
	}
	return strings.TrimSpace(b.String())
}

func clipUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// Baseline 会话基线：用户全局 → 项目根 … 工作目录。无文件返回空串。
func (in *Instructions) Baseline() (string, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.init()
	root, err := projectRoot(in.Workdir)
	if err != nil {
		return "", err
	}
	var files []string
	if in.Home != "" {
		if p := filepath.Join(in.Home, "AGENTS.md"); fileExists(p) {
			files = append(files, p)
		}
	}
	for _, d := range chain(root, in.Workdir) {
		files = append(files, dirFiles(d)...)
	}
	text := in.render(files)
	if text == "" {
		return "", nil
	}
	return "以下是项目指令（AGENTS.md），越靠后越具体，冲突时以靠后的为准：\n\n" + text, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// Block 基线作为系统块（无内容返回 nil）。
func (in *Instructions) Block() (*session.SystemBlock, error) {
	t, err := in.Baseline()
	if err != nil || t == "" {
		return nil, err
	}
	return &session.SystemBlock{Text: t, Source: "agents_md"}, nil
}

// Discover 目标路径所在目录链（工作目录之下）中新出现的指令文件；无则空串。
func (in *Instructions) Discover(target string) string {
	if target == "" {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(in.Workdir, target)
	}
	target = filepath.Clean(target)
	dir := target
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		dir = filepath.Dir(target)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.init()
	ds := chain(in.Workdir, dir)
	if len(ds) <= 1 {
		return ""
	}
	var files []string
	for _, d := range ds[1:] {
		files = append(files, dirFiles(d)...)
	}
	return in.render(files)
}

// agentsTools 触发嵌套发现的工具（按参数 path）。
var agentsTools = map[string]bool{"read_file": true, "edit": true, "write_file": true, "grep": true, "glob": true, "list_dir": true}

// Layer 工具 Transform：成功调用到达更深目录时，把新适用的指令追加到结果末尾（仅追加，前缀缓存不变）。
func (in *Instructions) Layer() toolhub.Layer {
	return toolhub.Layer{Name: "agents_md", Timeout: 2 * time.Second, FailOpen: true,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			out, err := next(ctx, cx, args)
			if err != nil || out.Kind != toolhub.Done || out.Status != session.ToolOK || cx.Tool == nil || !agentsTools[cx.Tool.Spec.Name] {
				return out, err
			}
			var a struct{ Path string }
			_ = json.Unmarshal(args, &a)
			if t := in.Discover(a.Path); t != "" {
				out.Content = append(append(session.Content{}, out.Content...),
					session.NewTextPart("\n\n<project-instructions>\n该目录有新的项目指令，后续在此目录工作时遵守：\n\n"+t+"\n</project-instructions>"))
			}
			return out, nil
		}}
}
