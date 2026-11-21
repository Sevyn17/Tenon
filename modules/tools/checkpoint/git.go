package checkpoint

// git 支持（spike 第二节 shell 基线、第六节「未提交改动」范围）：只读调用本机 git。
// 没有 git、工作目录不在仓库里或 git 报错时一律当作「没有 git」，不报错。
//
// 所有调用：工作目录 = e.Root，10 秒超时，-c core.quotepath=off（中文路径原样）、
// --no-optional-locks（不和用户自己的 git 抢 index 锁）、--literal-pathspecs（路径不当通配）。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GitTimeout 每次 git 调用的超时。
const GitTimeout = 10 * time.Second

// gitInfo 探测结果（每个引擎探测一次）。
type gitInfo struct {
	ok     bool
	prefix string // 工作目录相对仓库根的前缀（"" 或 "sub/dir/"）
}

func (e *Engine) gitProbe() gitInfo {
	e.gitOnce.Do(func() {
		if e.NoGit {
			return
		}
		if _, err := exec.LookPath("git"); err != nil {
			return
		}
		out, err := e.runGit(context.Background(), "rev-parse", "--is-inside-work-tree", "--show-prefix")
		if err != nil {
			return
		}
		ls := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
		if len(ls) < 1 || strings.TrimSpace(ls[0]) != "true" {
			return
		}
		p := ""
		if len(ls) > 1 {
			p = strings.TrimSpace(ls[1])
		}
		e.git = gitInfo{ok: true, prefix: p}
	})
	return e.git
}

// GitAvailable 本机有 git 且工作目录在一个 git 工作树里（能力 changes.git）。
func (e *Engine) GitAvailable() bool { return e.gitProbe().ok }

func (e *Engine) runGit(ctx context.Context, args ...string) ([]byte, error) {
	return e.runGitIn(ctx, nil, args...)
}

func (e *Engine) runGitIn(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, GitTimeout)
	defer cancel()
	full := append([]string{"-c", "core.quotepath=off", "--no-optional-locks", "--literal-pathspecs"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = e.Root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// gitEntry git status 里的一个路径（已合并同一路径的多条）。
type gitEntry struct {
	path      string // 相对工作目录，正斜杠
	inHead    bool   // HEAD 里有（按状态字母推断）
	untracked bool   // 只以 ?? 出现
	unmerged  bool
}

// gitStatus git status --porcelain=v1 -z，限定在工作目录内，路径转成相对工作目录。
func (e *Engine) gitStatus(ctx context.Context, untracked bool) (map[string]*gitEntry, error) {
	g := e.gitProbe()
	if !g.ok {
		return nil, errors.New("没有 git")
	}
	u := "--untracked-files=no"
	if untracked {
		u = "--untracked-files=all"
	}
	e.statusRuns.Add(1)
	out, err := e.runGit(ctx, "status", "--porcelain=v1", "-z", "--no-renames", u, "--", ".")
	if err != nil {
		return nil, err
	}
	m := map[string]*gitEntry{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) < 4 || rec[2] != ' ' {
			continue
		}
		x, y, p := rec[0], rec[1], string(rec[3:])
		// porcelain 的路径总是相对仓库根；工作目录是子目录时去掉前缀。
		if g.prefix != "" {
			if !strings.HasPrefix(p, g.prefix) {
				continue
			}
			p = p[len(g.prefix):]
		}
		if p == "" || strings.HasSuffix(p, "/") {
			continue
		}
		en := m[PathKey(p)]
		if en == nil {
			en = &gitEntry{path: p, untracked: true}
			m[PathKey(p)] = en
		}
		if x == '?' {
			continue
		}
		en.untracked = false
		if x != 'A' {
			en.inHead = true
		}
		if x == 'U' || y == 'U' || x == 'D' && y == 'D' || x == 'A' && y == 'A' {
			en.unmerged = true
		}
	}
	return m, nil
}

// gitHead 已跟踪文件在 HEAD 里、大小等于 size（调用前扫描得到的大小）的内容：先试原始 blob（git show HEAD:<path>），
// 再试经本工作树过滤器（行尾、smudge）转换后的字节。git status 干净只说明 stat 和索引对得上，
// 磁盘字节可能是二者之一（例如 core.autocrlf=true 时提交前就在磁盘上的 LF 文件），所以按大小挑；
// 两个都对不上、文件不在 HEAD 里或 git 失败返回 ok = false（记为不可还原，不猜）。
func (e *Engine) gitHead(ctx context.Context, rel string, size int64) ([]byte, bool) {
	for _, args := range [][]string{{"cat-file", "blob", "HEAD:./" + rel}, {"cat-file", "--filters", "HEAD:./" + rel}} {
		out, err := e.runGit(ctx, args...)
		if err != nil {
			return nil, false
		}
		if int64(len(out)) == size {
			return out, true
		}
	}
	return nil, false
}

// gitBase 对比基线：HEAD；还没有提交时用空树。
func (e *Engine) gitBase(ctx context.Context) (string, error) {
	if _, err := e.runGit(ctx, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		return "HEAD", nil
	}
	out, err := e.runGitIn(ctx, []byte{}, "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GitFile git 范围里的一个文件（与 HEAD 比的未提交改动）。
type GitFile struct {
	Path      string // 相对工作目录，正斜杠
	Status    string // added | modified | deleted
	Added     int
	Removed   int
	Binary    bool
	Untracked bool
}

// GitList 工作目录内与 HEAD 不同的文件（git status + git diff --numstat HEAD；未跟踪文件自己数行），按路径排序。
func (e *Engine) GitList(ctx context.Context) ([]GitFile, error) {
	st, err := e.gitStatus(ctx, true)
	if err != nil {
		return nil, err
	}
	base, err := e.gitBase(ctx)
	if err != nil {
		return nil, err
	}
	out, err := e.runGit(ctx, "diff", "--numstat", "-z", "--no-renames", "--relative", "--no-ext-diff", "--no-textconv", base, "--", ".")
	if err != nil {
		return nil, err
	}
	type ns struct {
		a, r int
		bin  bool
	}
	nums := map[string]ns{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		f := strings.SplitN(string(rec), "\t", 3)
		if len(f) != 3 || f[2] == "" {
			continue
		}
		if f[0] == "-" {
			nums[PathKey(f[2])] = ns{bin: true}
			continue
		}
		a, _ := strconv.Atoi(f[0])
		r, _ := strconv.Atoi(f[1])
		nums[PathKey(f[2])] = ns{a: a, r: r}
	}
	var files []GitFile
	for key, en := range st {
		_, lerr := os.Lstat(filepath.Join(e.Root, filepath.FromSlash(en.path)))
		inWT := lerr == nil
		gf := GitFile{Path: en.path, Untracked: en.untracked && !en.inHead}
		switch {
		case en.inHead && inWT, en.unmerged:
			gf.Status = "modified"
		case !en.inHead && inWT:
			gf.Status = "added"
		case en.inHead && !inWT:
			gf.Status = "deleted"
		default:
			continue // 暂存后又删了：和 HEAD 一样
		}
		if n, ok := nums[key]; ok {
			gf.Added, gf.Removed, gf.Binary = n.a, n.r, n.bin
		} else if gf.Untracked {
			if s, err := readState(filepath.Join(e.Root, filepath.FromSlash(en.path)), e.maxFile()); err == nil && s.data != nil {
				d := Diff(en.path, nil, s.data)
				gf.Added, gf.Binary = d.Added, d.Binary
			} else if err == nil {
				gf.Binary = s.binary
			}
		}
		files = append(files, gf)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// ErrNotInGit changes.diff 的路径不在 git 范围的列表里。
var ErrNotInGit = errors.New("不是未提交的改动")

// GitDiff git 范围里一个文件的 diff：已跟踪文件 git diff HEAD；未跟踪文件和空文件比。
// 只接受当前 GitList 里有的路径（rel 只用作查找键）。
func (e *Engine) GitDiff(ctx context.Context, rel string) (FileDiff, error) {
	files, err := e.GitList(ctx)
	if err != nil {
		return FileDiff{}, err
	}
	var f *GitFile
	for i := range files {
		if PathKey(files[i].Path) == PathKey(rel) {
			f = &files[i]
			break
		}
	}
	if f == nil {
		return FileDiff{}, fmt.Errorf("%w: %s", ErrNotInGit, rel)
	}
	abs := filepath.Join(e.Root, filepath.FromSlash(f.Path))
	if f.Untracked {
		s, err := readState(abs, e.maxFile())
		if err != nil {
			return FileDiff{}, err
		}
		if s.data == nil {
			return FileDiff{Path: f.Path, Binary: s.binary, NewSize: s.size}, ErrTooLarge
		}
		return Diff(f.Path, nil, s.data), nil
	}
	d := FileDiff{Path: f.Path, Binary: f.Binary, Added: f.Added, Removed: f.Removed}
	if fi, err := os.Lstat(abs); err == nil {
		d.NewSize = fi.Size()
	}
	base, err := e.gitBase(ctx)
	if err != nil {
		return FileDiff{}, err
	}
	if f.Status != "added" {
		if out, err := e.runGit(ctx, "cat-file", "-s", base+":./"+f.Path); err == nil {
			d.OldSize, _ = strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		}
	}
	if f.Binary {
		return d, nil
	}
	out, err := e.runGit(ctx, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--relative",
		"--src-prefix=a/", "--dst-prefix=b/", base, "--", f.Path)
	if err != nil {
		return FileDiff{}, err
	}
	// 去掉 diff --git / index / mode 行，只留 ---/+++/@@（与 changes.diff 其他范围一致）。
	s := string(out)
	if i := strings.Index(s, "\n--- "); i >= 0 {
		s = s[i+1:]
	} else if !strings.HasPrefix(s, "--- ") {
		s = ""
	}
	d.Unified = s
	return d, nil
}

// ErrTooLarge 文件超过单文件上限，没读内容。
var ErrTooLarge = errors.New("文件太大")
