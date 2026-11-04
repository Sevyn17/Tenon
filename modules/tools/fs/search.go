package fs

// 5.2：read_file（行范围）/ grep / glob。纯 Go，不依赖 rg / fd（docs/90 - 归档/20 - 插件/09 - 搜索与读取.md）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ybase/runtime/toolhub"
)

// 读取与搜索的输出上限。
const (
	ReadMaxLines   = 2000
	ReadMaxBytes   = 50 << 10
	GrepMaxLine    = 500
	GrepLimit      = 100
	GlobLimit      = 200
	searchMaxBytes = 50 << 10
)

// skipDirs 无论 .gitignore 如何都跳过的目录。
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// ---------- read_file ----------

// ReadFile 带行范围的读文件。
func ReadFile(root string) *toolhub.Tool {
	root, _ = filepath.Abs(root)
	return toolhub.Simple("read_file",
		fmt.Sprintf("读取文本文件（路径相对工作目录）。offset 为起始行（1 起），limit 为行数；默认最多 %d 行 / %dKB，截断时按提示用 offset 继续。", ReadMaxLines, ReadMaxBytes>>10),
		`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"起始行号，1 起"},"limit":{"type":"integer","description":"最多读取的行数"}},"required":["path"]}`,
		func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path          string
				Offset, Limit int
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("参数不合法：%v", err)
			}
			p, err := resolve(root, a.Path)
			if err != nil {
				return "", err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			return readRange(a.Path, b, a.Offset, a.Limit)
		})
}

func readRange(display string, b []byte, offset, limit int) (string, error) {
	if isBinary(b) {
		return "", fmt.Errorf("%s 是二进制文件，不能按文本读取", display)
	}
	b = bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	if text == "" {
		return "（空文件）", nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	total := len(lines)
	if offset < 1 {
		offset = 1
	}
	if offset > total {
		return "", fmt.Errorf("offset %d 超出文件末尾（共 %d 行）", offset, total)
	}
	end := total
	if limit > 0 && offset-1+limit < end {
		end = offset - 1 + limit
	}
	if end-(offset-1) > ReadMaxLines {
		end = offset - 1 + ReadMaxLines
	}
	var sb strings.Builder
	last := offset - 1
	for i := offset - 1; i < end; i++ {
		if sb.Len()+len(lines[i])+1 > ReadMaxBytes {
			if i == offset-1 {
				return "", fmt.Errorf("第 %d 行长 %d 字节，超过 %dKB 上限；请用 shell 截取", i+1, len(lines[i]), ReadMaxBytes>>10)
			}
			break
		}
		sb.WriteString(lines[i])
		sb.WriteByte('\n')
		last = i + 1
	}
	out := sb.String()
	if last < total {
		out += fmt.Sprintf("\n[已显示第 %d–%d 行（共 %d 行），用 offset=%d 继续]", offset, last, total, last+1)
	}
	return out, nil
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

// ---------- 遍历与 .gitignore ----------

type ignoreRule struct {
	base    string // 规则所在目录（相对 root，"" 为根）
	pattern string
	neg     bool
	dirOnly bool
	rooted  bool
}

func parseIgnore(base string, data []byte) []ignoreRule {
	var rs []ignoreRule
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), " \r")
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		r := ignoreRule{base: base}
		if strings.HasPrefix(l, "!") {
			r.neg, l = true, l[1:]
		}
		if strings.HasSuffix(l, "/") {
			r.dirOnly, l = true, strings.TrimSuffix(l, "/")
		}
		if strings.HasPrefix(l, "/") {
			r.rooted, l = true, l[1:]
		} else if strings.Contains(l, "/") {
			r.rooted = true // 含中间斜杠的模式相对所在目录
		}
		if l == "" {
			continue
		}
		r.pattern = l
		rs = append(rs, r)
	}
	return rs
}

// ignored 按 gitignore 语义判断（后出现的规则优先）。rel 为相对 root 的 / 路径。
func ignored(rules []ignoreRule, rel string, dir bool) bool {
	res := false
	for _, r := range rules {
		if r.dirOnly && !dir {
			continue
		}
		sub := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			sub = rel[len(r.base)+1:]
		}
		var hit bool
		if r.rooted {
			hit = globMatch(r.pattern, sub)
		} else {
			hit = globMatch(r.pattern, path.Base(sub))
		}
		if hit {
			res = !r.neg
		}
	}
	return res
}

// globMatch 支持 * ? [..] 与跨目录的 **。
func globMatch(pattern, name string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(ps, ns []string) bool {
	for len(ps) > 0 {
		if ps[0] == "**" {
			ps = ps[1:]
			if len(ps) == 0 {
				return true
			}
			for i := 0; i <= len(ns); i++ {
				if matchSegs(ps, ns[i:]) {
					return true
				}
			}
			return false
		}
		if len(ns) == 0 {
			return false
		}
		if ok, _ := path.Match(ps[0], ns[0]); !ok {
			return false
		}
		ps, ns = ps[1:], ns[1:]
	}
	return len(ns) == 0
}

// walk 遍历 dir（绝对路径）下的普通文件，跳过忽略项与符号链接；fn 返回 false 停止。
func walk(ctx context.Context, root, dir string, fn func(abs, rel string, info fs.FileInfo) bool) error {
	var rules []ignoreRule
	// 加载 root 到 dir 沿途各级 .gitignore。
	relDir, _ := filepath.Rel(root, dir)
	relDir = filepath.ToSlash(relDir)
	if relDir == "." {
		relDir = ""
	}
	cur := ""
	load := func(rel string) {
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), ".gitignore")); err == nil {
			rules = append(rules, parseIgnore(rel, b)...)
		}
	}
	load("")
	if relDir != "" {
		for _, seg := range strings.Split(relDir, "/") {
			if cur == "" {
				cur = seg
			} else {
				cur += "/" + seg
			}
			load(cur)
		}
	}
	stop := errors.New("stop")
	var visit func(abs, rel string, rules []ignoreRule) error
	visit = func(abs, rel string, rules []ignoreRule) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		ents, err := os.ReadDir(abs)
		if err != nil {
			return nil // 不可读目录跳过
		}
		for _, e := range ents {
			name := e.Name()
			r := name
			if rel != "" {
				r = rel + "/" + name
			}
			if e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			if e.IsDir() {
				if skipDirs[name] || ignored(rules, r, true) {
					continue
				}
				sub := rules
				if b, err := os.ReadFile(filepath.Join(abs, name, ".gitignore")); err == nil {
					sub = append(append([]ignoreRule{}, rules...), parseIgnore(r, b)...)
				}
				if err := visit(filepath.Join(abs, name), r, sub); err != nil {
					return err
				}
				continue
			}
			if !e.Type().IsRegular() || ignored(rules, r, false) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if !fn(filepath.Join(abs, name), r, info) {
				return stop
			}
		}
		return nil
	}
	err := visit(dir, relDir, rules)
	if errors.Is(err, stop) {
		return nil
	}
	return err
}

// ---------- grep ----------

// Grep 内容搜索。
func Grep(root string) *toolhub.Tool {
	root, _ = filepath.Abs(root)
	return toolhub.Simple("grep",
		fmt.Sprintf("在文件内容中搜索（正则或字面量），返回 路径:行号: 内容。遵守 .gitignore，跳过二进制文件。默认最多 %d 条匹配。", GrepLimit),
		`{"type":"object","properties":{
"pattern":{"type":"string","description":"正则（RE2 语法）或字面量"},
"path":{"type":"string","description":"目录或文件（默认工作目录）"},
"glob":{"type":"string","description":"文件过滤，如 *.go 或 **/*_test.go"},
"ignore_case":{"type":"boolean"},
"literal":{"type":"boolean","description":"把 pattern 当字面量"},
"context":{"type":"integer","description":"匹配前后各显示几行"},
"limit":{"type":"integer","description":"最多返回的匹配数"}},"required":["pattern"]}`,
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Pattern    string `json:"pattern"`
				Path       string `json:"path"`
				Glob       string `json:"glob"`
				IgnoreCase bool   `json:"ignore_case"`
				Literal    bool   `json:"literal"`
				Context    int    `json:"context"`
				Limit      int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("参数不合法：%v", err)
			}
			return grep(ctx, root, a.Pattern, a.Path, a.Glob, a.IgnoreCase, a.Literal, a.Context, a.Limit)
		})
}

func grep(ctx context.Context, root, pattern, p, glob string, icase, literal bool, ctxLines, limit int) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern 不能为空")
	}
	if literal {
		pattern = regexp.QuoteMeta(pattern)
	}
	if icase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("正则不合法：%v（字面量搜索请设 literal=true）", err)
	}
	if limit <= 0 {
		limit = GrepLimit
	}
	if ctxLines < 0 {
		ctxLines = 0
	}
	if ctxLines > 10 {
		ctxLines = 10
	}
	start, err := resolve(root, p)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(start)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	matches, files := 0, 0
	full := false
	scan := func(abs, rel string) bool {
		b, err := os.ReadFile(abs)
		if err != nil || isBinary(b) {
			return true
		}
		lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
		lastShown := -1
		hit := false
		for i, l := range lines {
			if !re.MatchString(l) {
				continue
			}
			if matches >= limit || sb.Len() >= searchMaxBytes {
				full = true
				return false
			}
			if !hit {
				hit = true
				files++
			}
			from := i - ctxLines
			if from <= lastShown {
				from = lastShown + 1
			}
			if from < 0 {
				from = 0
			}
			if ctxLines > 0 && lastShown >= 0 && from > lastShown+1 {
				sb.WriteString("--\n")
			}
			to := i + ctxLines
			if to >= len(lines) {
				to = len(lines) - 1
			}
			for j := from; j <= to; j++ {
				sep := "-"
				if re.MatchString(lines[j]) {
					sep = ":"
				}
				if j > i && sep == ":" {
					to = j - 1 // 后续匹配行留给下一轮
					break
				}
				fmt.Fprintf(&sb, "%s%s%d%s %s\n", rel, sep, j+1, sep, clip(lines[j]))
				lastShown = j
			}
			matches++
		}
		return true
	}
	if st.IsDir() {
		if err := walk(ctx, root, start, func(abs, rel string, _ fs.FileInfo) bool {
			if glob != "" && !matchGlob(glob, rel, start, root) {
				return true
			}
			return scan(abs, rel)
		}); err != nil {
			return "", err
		}
	} else {
		rel, _ := filepath.Rel(root, start)
		scan(start, filepath.ToSlash(rel))
	}
	if matches == 0 {
		return "没有匹配", nil
	}
	out := sb.String()
	if full {
		out += fmt.Sprintf("\n[结果不完整：已达 %d 条或 %dKB 上限，请缩小 path / glob 或细化 pattern]", limit, searchMaxBytes>>10)
	} else {
		out += fmt.Sprintf("\n[%d 条匹配，%d 个文件]", matches, files)
	}
	return out, nil
}

func clip(l string) string {
	if len(l) <= GrepMaxLine {
		return l
	}
	cut := GrepMaxLine
	for cut > 0 && (l[cut]&0xC0) == 0x80 {
		cut--
	}
	return l[:cut] + "…"
}

// matchGlob：不含 / 的模式匹配文件名，含 / 的匹配相对搜索起点的路径。
func matchGlob(glob, rel, start, root string) bool {
	if !strings.Contains(glob, "/") {
		return globMatch(glob, path.Base(rel))
	}
	r := rel
	if sr, err := filepath.Rel(root, start); err == nil && sr != "." {
		r = strings.TrimPrefix(rel, filepath.ToSlash(sr)+"/")
	}
	return globMatch(glob, r)
}

// ---------- glob ----------

// Glob 按模式找文件。
func Glob(root string) *toolhub.Tool {
	root, _ = filepath.Abs(root)
	return toolhub.Simple("glob",
		fmt.Sprintf("按文件名模式查找文件（如 **/*.go、cmd/**/main.go；不含 / 的模式匹配任意层的文件名），按修改时间由新到旧，遵守 .gitignore。默认最多 %d 条。", GlobLimit),
		`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string","description":"搜索起点目录（默认工作目录）"},"limit":{"type":"integer"}},"required":["pattern"]}`,
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
				Limit   int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("参数不合法：%v", err)
			}
			return globFiles(ctx, root, a.Pattern, a.Path, a.Limit)
		})
}

func globFiles(ctx context.Context, root, pattern, p string, limit int) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern 不能为空")
	}
	if _, err := path.Match(strings.ReplaceAll(pattern, "**", "*"), ""); err != nil {
		return "", fmt.Errorf("模式不合法：%v", err)
	}
	if limit <= 0 {
		limit = GlobLimit
	}
	start, err := resolve(root, p)
	if err != nil {
		return "", err
	}
	type hit struct {
		rel string
		mt  int64
	}
	var hits []hit
	if err := walk(ctx, root, start, func(_, rel string, info fs.FileInfo) bool {
		if matchGlob(pattern, rel, start, root) {
			hits = append(hits, hit{rel, info.ModTime().UnixNano()})
		}
		return true
	}); err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "没有匹配的文件", nil
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].mt != hits[j].mt {
			return hits[i].mt > hits[j].mt
		}
		return hits[i].rel < hits[j].rel
	})
	var sb strings.Builder
	n := len(hits)
	if n > limit {
		hits = hits[:limit]
	}
	for _, h := range hits {
		sb.WriteString(h.rel + "\n")
	}
	if n > limit {
		fmt.Fprintf(&sb, "\n[共 %d 个，已显示最新的 %d 个；请细化模式]", n, limit)
	}
	return sb.String(), nil
}
