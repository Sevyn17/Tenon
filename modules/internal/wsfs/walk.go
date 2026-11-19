package wsfs

// 工作区遍历与 .gitignore 规则（原在 modules/tools/fs/search.go，grep / glob 与文件检查点共用）。

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// SkipDirs 无论 .gitignore 如何都跳过的目录。
var SkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

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
			hit = GlobMatch(r.pattern, sub)
		} else {
			hit = GlobMatch(r.pattern, path.Base(sub))
		}
		if hit {
			res = !r.neg
		}
	}
	return res
}

// GlobMatch 支持 * ? [..] 与跨目录的 **。
func GlobMatch(pattern, name string) bool {
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

// WalkFiles 遍历 dir（绝对路径）下的普通文件，跳过忽略项与符号链接；fn 返回 false 停止。
// 返回因 SkipDirs 跳过的 vendor / node_modules 目录（相对路径；.git 不报），调用方据此提示
// （2026-11-04 审查 P2 工具体验：原先静默跳过，模型以为依赖里没有这段代码）。
func WalkFiles(ctx context.Context, root, dir string, fn func(abs, rel string, info fs.FileInfo) bool) ([]string, error) {
	return walkFiles(ctx, root, dir, nil, fn)
}

func walkFiles(ctx context.Context, root, dir string, extra map[string]bool, fn func(abs, rel string, info fs.FileInfo) bool) ([]string, error) {
	var skipped []string
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
				if SkipDirs[name] {
					if name != ".git" {
						skipped = append(skipped, r)
					}
					continue
				}
				if extra[name] {
					continue
				}
				if ignored(rules, r, true) {
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
		err = nil
	}
	return skipped, err
}

// FileStat 一次遍历里一个文件的 mtime 与大小。
type FileStat struct {
	ModTime time.Time
	Size    int64
}

// ErrTooManyFiles Walk 遇到的文件数超过上限。
var ErrTooManyFiles = errors.New("工作区文件数超过上限")

// Walk 遍历 root 下的普通文件（规则与 grep / glob 相同），返回 相对路径（正斜杠）→ mtime/size。
// 不排序、不截断：文件数超过 max（>0 时生效）返回 ErrTooManyFiles 与 nil。
func Walk(ctx context.Context, root string, max int) (map[string]FileStat, error) {
	return WalkSkip(ctx, root, max, nil)
}

// WalkSkip 同 Walk，另外跳过名字在 skip 里的目录（任意层级，如检查点扫描跳过 .venv、dist）。
func WalkSkip(ctx context.Context, root string, max int, skip map[string]bool) (map[string]FileStat, error) {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	out := map[string]FileStat{}
	over := false
	_, err := walkFiles(ctx, root, root, skip, func(_, rel string, info fs.FileInfo) bool {
		if max > 0 && len(out) >= max {
			over = true
			return false
		}
		out[rel] = FileStat{ModTime: info.ModTime(), Size: info.Size()}
		return true
	})
	if err != nil {
		return nil, err
	}
	if over {
		return nil, fmt.Errorf("%w（%d）", ErrTooManyFiles, max)
	}
	return out, nil
}
