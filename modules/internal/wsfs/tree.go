package wsfs

// 增量工作区快照（2026-11-21 检查点扫描提速）：第一次全量遍历，之后靠操作系统的文件变更通知
// （Windows ReadDirectoryChangesW 递归监视；Linux inotify 逐目录监视）只重扫变过的目录。
//
// 规则与 WalkSkip 完全相同（.gitignore、SkipDirs、额外跳过的目录名、跳过符号链接），结果也相同：
// 相对路径（正斜杠）→ mtime / 大小。没有后台 goroutine：通知在 Snapshot 里同步排空
// （Windows 用完成端口取、超时 0；Linux 用非阻塞 fd 读到 EAGAIN）。两个平台的通知都是在写文件的
// 系统调用里同步入队的，进程写完退出时通知已经在队列里，所以调用返回后的 Snapshot 一定能看到这次调用的改动。
//
// 退回全量的情况：监视器起不来（不认识的文件系统、网络盘、inotify 监视数用完）、通知溢出、
// 监视器报错、根目录换了（不再是同一个目录）、增量中途出错（ctx 取消、超上限）。

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// change 监视器报告的一处变动：相对 root 的路径（正斜杠）。replaced = 这个路径可能被整个换掉了
// （新建 / 移入 / 自身被删或移走），是已知目录时要整棵重新遍历，不能只看上一级目录里还有没有这个名字。
type change struct {
	path     string
	replaced bool
}

// watcher 平台监视器。recursive 为真时一个监视覆盖整棵树（Windows），add 不做事。
type watcher interface {
	recursive() bool
	add(abs, rel string) error
	// drain 取走目前已排队的全部通知；overflow = 有通知丢了（内核队列 / 缓冲区满），须全量。
	drain() (cs []change, overflow bool, err error)
	close() error
}

var errNoWatch = errors.New("本平台或文件系统不支持文件变更通知")

// TreeStats 计数（测试与耗时测量用）。
type TreeStats struct {
	Full        int // 全量遍历次数
	Incremental int // 只重扫了变过的目录
	Reused      int // 没有任何通知，直接复用
	Overflow    int // 因通知溢出退回全量
}

type dnode struct {
	rel   string          // 真实大小写
	files map[string]bool // 直接包含的文件名
	subs  map[string]bool // 直接包含、且在扫描范围内的子目录名
}

// Tree 一个工作区根目录的增量快照。并发安全。
type Tree struct {
	// NoWatch 不用变更通知，每次 Snapshot 都全量遍历（对照测量、测试）。须在第一次 Snapshot 前设。
	NoWatch bool

	root string
	max  int
	skip map[string]bool
	fold bool

	mu      sync.Mutex
	w       watcher
	noWatch bool // 监视器起不来过：之后一直全量
	closed  bool
	valid   bool
	files   map[string]FileStat // 当前快照；交出去后不再原地修改（写时复制）
	dirs    map[string]*dnode   // key(rel) → 目录
	gen     uint64
	rootFI  os.FileInfo
	cowDone bool
	stats   TreeStats
	open    func(root string) (watcher, error) // 测试替换
}

var treeGen atomic.Uint64

// NewTree 构造（不碰文件系统；监视器在第一次 Snapshot 时启动）。max > 0 时文件数超过 max 报 ErrTooManyFiles。
func NewTree(root string, max int, skip map[string]bool) *Tree {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &Tree{root: root, max: max, skip: skip, fold: runtime.GOOS == "windows" || runtime.GOOS == "darwin",
		open: openWatcher}
}

// Root 根目录（绝对路径）。
func (t *Tree) Root() string { return t.root }

// Stats 计数。
func (t *Tree) Stats() TreeStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stats
}

// Watching 监视器是否在工作（false = 每次全量）。
func (t *Tree) Watching() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.w != nil
}

// Close 释放监视器句柄与快照。之后 Snapshot 仍可用，但每次全量，不再启动监视器。
func (t *Tree) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed, t.valid = true, false
	t.files, t.dirs = nil, nil
	return t.closeW()
}

func (t *Tree) closeW() error {
	if t.w == nil {
		return nil
	}
	err := t.w.close()
	t.w = nil
	return err
}

// Snapshot 当前快照与代号：代号相同 = 内容相同（代号全进程唯一，0 表示出错）。返回的 map 只读，调用方不得修改。
// full = 不管有没有通知都全量遍历一次（回合末兜底）。文件数超上限返回 ErrTooManyFiles。
func (t *Tree) Snapshot(ctx context.Context, full bool) (map[string]FileStat, uint64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.w != nil && !t.sameRoot() {
		_ = t.closeW() // 根目录换了：旧监视器盯的不是它
		t.valid = false
	}
	if !full && t.valid && t.w != nil {
		cs, over, err := t.collect()
		cs = t.relevant(cs)
		switch {
		case err != nil:
			_ = t.closeW() // 监视器坏了：之后全量（下一次全量会重新打开）
		case over:
			t.stats.Overflow++
		case len(cs) == 0:
			t.stats.Reused++
			return t.files, t.gen, nil
		default:
			prev, prevGen := t.files, t.gen
			ok, err := t.apply(ctx, cs)
			if ok && err == nil {
				t.stats.Incremental++
				return t.files, t.gen, nil
			}
			t.files, t.gen = prev, prevGen // t.dirs 可能已半改，下面的全量重建
			if err != nil && !errors.Is(err, ErrTooManyFiles) {
				t.valid = false
				return nil, 0, err
			}
		}
	}
	return t.full(ctx)
}

// collect 取走已排队的通知。
func (t *Tree) collect() ([]change, bool, error) { return t.w.drain() }

// relevant 去掉不影响快照的通知：已知目录自身的「修改」（目录的 mtime / 属性变了）。目录里的增删改
// 各有自己的通知（Windows 递归监视按文件报；Linux 每个目录都有监视），不靠目录自身这一条。
// NTFS 的目录 mtime 是延迟落盘的，监视器刚开时常收到一条之前写入留下的目录修改通知。
func (t *Tree) relevant(cs []change) []change {
	out := cs[:0:0]
	for _, c := range cs {
		p := strings.Trim(c.path, "/")
		if !c.replaced && p != "" {
			if n := t.dirs[t.key(p)]; n != nil {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func (t *Tree) sameRoot() bool {
	fi, err := os.Stat(t.root)
	return err == nil && t.rootFI != nil && os.SameFile(fi, t.rootFI)
}

func (t *Tree) key(rel string) string {
	if t.fold {
		return strings.ToLower(rel)
	}
	return rel
}

func join(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func parentOf(rel string) string {
	if d := path.Dir(rel); d != "." {
		return d
	}
	return ""
}

func (t *Tree) full(ctx context.Context) (map[string]FileStat, uint64, error) {
	if t.w == nil && !t.noWatch && !t.closed && !t.NoWatch {
		if w, err := t.open(t.root); err != nil {
			t.noWatch = true
		} else {
			t.w = w
		}
	}
	if t.w != nil { // 先排空：之前排队的通知都被这次全量覆盖；遍历期间的新通知留给下一次
		if _, _, err := t.w.drain(); err != nil {
			_ = t.closeW()
		}
	}
	fi, err := os.Stat(t.root)
	if err != nil {
		t.valid = false
		return nil, 0, err
	}
	os.SameFile(fi, fi) // Windows 上文件号是按路径懒取的：现在就取，之后根目录被换掉才比得出来
	old, oldValid := t.files, t.valid
	t.files, t.dirs, t.cowDone, t.valid = map[string]FileStat{}, map[string]*dnode{}, true, false
	if err := t.walkDir(ctx, t.root, "", t.rulesFor("")); err != nil {
		t.files, t.dirs = nil, nil
		if errors.Is(err, ErrTooManyFiles) {
			return nil, 0, fmt.Errorf("%w（%d）", ErrTooManyFiles, t.max)
		}
		return nil, 0, err
	}
	t.stats.Full++
	t.valid, t.rootFI = true, fi
	if oldValid && sameStats(old, t.files) {
		t.files = old // 没变：保留代号，调用方据此跳过比对与 git status
	} else {
		t.gen = treeGen.Add(1)
	}
	return t.files, t.gen, nil
}

func sameStats(a, b map[string]FileStat) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || x.Size != y.Size || !x.ModTime.Equal(y.ModTime) {
			return false
		}
	}
	return true
}

// mut 本次增量第一次改快照时复制一份（已交出去的 map 不能动）。
func (t *Tree) mut() map[string]FileStat {
	if !t.cowDone {
		t.files = maps.Clone(t.files)
		t.cowDone = true
	}
	return t.files
}

// rulesFor 扫描目录 rel 时生效的 .gitignore 规则：根到 rel（含）沿途各级。
func (t *Tree) rulesFor(rel string) []ignoreRule {
	var rules []ignoreRule
	load := func(r string) {
		if b, err := os.ReadFile(filepath.Join(t.root, filepath.FromSlash(r), ".gitignore")); err == nil {
			rules = append(rules, parseIgnore(r, b)...)
		}
	}
	load("")
	if rel != "" {
		cur := ""
		for _, seg := range strings.Split(rel, "/") {
			cur = join(cur, seg)
			load(cur)
		}
	}
	return rules
}

func (t *Tree) childRules(abs, name, r string, rules []ignoreRule) []ignoreRule {
	if b, err := os.ReadFile(filepath.Join(abs, name, ".gitignore")); err == nil {
		return append(append([]ignoreRule{}, rules...), parseIgnore(r, b)...)
	}
	return rules
}

func (t *Tree) skipDir(name string) bool { return SkipDirs[name] || t.skip[name] }

func (t *Tree) watch(abs, rel string) error {
	if t.w == nil || t.w.recursive() {
		return nil
	}
	if err := t.w.add(abs, rel); err != nil { // inotify 监视数用完等：之后一直全量
		_ = t.closeW()
		t.noWatch = true
		return err
	}
	return nil
}

// putFile 记一个文件；新文件超上限报 ErrTooManyFiles。
func (t *Tree) putFile(r string, st FileStat) error {
	if old, ok := t.files[r]; ok && old.Size == st.Size && old.ModTime.Equal(st.ModTime) {
		return nil
	}
	fs := t.mut()
	if _, ok := fs[r]; !ok && t.max > 0 && len(fs) >= t.max {
		return ErrTooManyFiles
	}
	fs[r] = st
	return nil
}

// walkDir 整棵遍历 abs（相对 rel），写进 t.files / t.dirs；规则与 walkFiles 相同。
// 先加监视再读目录：读之后的改动一定有通知，读之前的改动读得到。
func (t *Tree) walkDir(ctx context.Context, abs, rel string, rules []ignoreRule) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = t.watch(abs, rel)
	n := &dnode{rel: rel, files: map[string]bool{}, subs: map[string]bool{}}
	t.dirs[t.key(rel)] = n
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil // 不可读目录跳过
	}
	for _, e := range ents {
		name := e.Name()
		r := join(rel, name)
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if e.IsDir() {
			if t.skipDir(name) || ignored(rules, r, true) {
				continue
			}
			n.subs[name] = true
			if err := t.walkDir(ctx, filepath.Join(abs, name), r, t.childRules(abs, name, r, rules)); err != nil {
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
		if err := t.putFile(r, FileStat{ModTime: info.ModTime(), Size: info.Size()}); err != nil {
			return err
		}
		n.files[name] = true
	}
	return nil
}

// dropSubtree 从快照里去掉目录 rel 及其下全部内容。
func (t *Tree) dropSubtree(rel string) {
	n := t.dirs[t.key(rel)]
	if n == nil {
		return
	}
	for name := range n.files {
		delete(t.mut(), join(n.rel, name))
	}
	for name := range n.subs {
		t.dropSubtree(join(n.rel, name))
	}
	delete(t.dirs, t.key(rel))
}

var errNeedFull = errors.New("需要全量")

type rescanJob struct {
	n     *dnode
	rec   bool            // 整棵重扫（.gitignore 变了、路径里有拿不准的 8.3 短名）
	force map[string]bool // 只重扫一层，但这些子目录（key）要整棵重新遍历（可能被整个换掉了）
}

// apply 按通知重扫变过的目录。ok = false 表示应退回全量（如根目录本身被报告变动）。
func (t *Tree) apply(ctx context.Context, cs []change) (bool, error) {
	jobs := map[string]*rescanJob{}
	for _, c := range cs {
		p := strings.Trim(c.path, "/")
		if p == "" {
			return false, nil
		}
		segs := strings.Split(p, "/")
		skipped := false
		for i, s := range segs {
			// 目录段在跳过名单里：整棵不扫，不用管。最后一段只有可能是目录时才算（同名普通文件照扫）。
			if t.skipDir(s) && (i < len(segs)-1 || c.replaced) {
				skipped = true
				break
			}
		}
		if skipped {
			continue
		}
		dir, rec := parentOf(p), path.Base(p) == ".gitignore"
		force := ""
		if c.replaced {
			force = path.Base(p)
		}
		// 找最近的已知目录。上级目录不在快照里 = 新建的（一层重扫上级时会整棵遍历它）、被忽略的或已删的。
		// 不认识的目录名带 ~ 可能是 Windows 的 8.3 短名，拿不准对应哪个目录，整棵重扫那个已知上级。
		for t.dirs[t.key(dir)] == nil {
			if strings.Contains(path.Base(dir), "~") {
				rec = true
			}
			force = ""
			if dir == "" {
				return false, nil
			}
			dir = parentOf(dir)
		}
		if rec && dir == "" && path.Base(p) != ".gitignore" {
			return false, nil // 整棵重扫根目录 = 全量
		}
		n := t.dirs[t.key(dir)]
		j := jobs[t.key(n.rel)]
		if j == nil {
			j = &rescanJob{n: n, force: map[string]bool{}}
			jobs[t.key(n.rel)] = j
		}
		j.rec = j.rec || rec
		if force != "" {
			j.force[t.key(force)] = true
		}
	}
	keys := make([]string, 0, len(jobs))
	for k := range jobs {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 上级先于下级
	t.cowDone = false
	for _, k := range keys {
		covered := false // 上级要整棵重扫的，下级的任务已被覆盖
		for a := k; a != ""; {
			a = parentOf(a)
			if j := jobs[a]; j != nil && j.rec {
				covered = true
				break
			}
		}
		j := jobs[k]
		if covered || t.dirs[k] != j.n {
			continue // 或者已在前面的任务里被去掉 / 重建
		}
		if err := t.rescanDir(ctx, j); err != nil {
			if errors.Is(err, errNeedFull) {
				return false, nil
			}
			return false, err
		}
	}
	if t.cowDone {
		t.gen = treeGen.Add(1)
	}
	return true, nil
}

// rescanDir 重扫一个已知目录：rec = 整棵；否则只看直接内容，新出现的（或 force 里的）子目录整棵遍历、消失的整棵去掉。
func (t *Tree) rescanDir(ctx context.Context, j *rescanJob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n := j.n
	abs := filepath.Join(t.root, filepath.FromSlash(n.rel))
	if fi, err := os.Lstat(abs); err != nil || !fi.IsDir() {
		// 目录没了（或换成了文件 / 链接）：一层重扫上一级，它会把这个目录去掉
		if n.rel == "" {
			return errNeedFull
		}
		p := t.dirs[t.key(parentOf(n.rel))]
		if p == nil {
			return errNeedFull
		}
		return t.rescanDir(ctx, &rescanJob{n: p, force: map[string]bool{t.key(path.Base(n.rel)): true}})
	}
	rules := t.rulesFor(n.rel)
	if j.rec {
		t.dropSubtree(n.rel)
		return t.walkDir(ctx, abs, n.rel, rules)
	}
	if err := t.watch(abs, n.rel); err != nil {
		return errNeedFull
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return errNeedFull
	}
	type fent struct {
		name string
		st   FileStat
	}
	var files []fent
	var newDirs []string
	seenF, seenD := map[string]bool{}, map[string]bool{}
	for _, e := range ents {
		name := e.Name()
		r := join(n.rel, name)
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if e.IsDir() {
			if t.skipDir(name) || ignored(rules, r, true) {
				continue
			}
			seenD[name] = true
			if !n.subs[name] || j.force[t.key(name)] {
				newDirs = append(newDirs, name)
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
		seenF[name] = true
		files = append(files, fent{name, FileStat{ModTime: info.ModTime(), Size: info.Size()}})
	}
	// 先删后加：大小写改名（A → a）时 key 相同
	for name := range n.files {
		if !seenF[name] {
			delete(t.mut(), join(n.rel, name))
			delete(n.files, name)
		}
	}
	for name := range n.subs {
		if !seenD[name] || j.force[t.key(name)] {
			t.dropSubtree(join(n.rel, name))
			delete(n.subs, name)
		}
	}
	for _, f := range files {
		if err := t.putFile(join(n.rel, f.name), f.st); err != nil {
			return err
		}
		n.files[f.name] = true
	}
	for _, name := range newDirs {
		n.subs[name] = true
		r := join(n.rel, name)
		if err := t.walkDir(ctx, filepath.Join(abs, name), r, t.childRules(abs, name, r, rules)); err != nil {
			return err
		}
	}
	return nil
}
