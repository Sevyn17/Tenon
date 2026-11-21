package wsfs

// 增量快照（Tree）的正确性：每一步都和 WalkSkip 的全量结果逐项比对。

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

var testSkip = map[string]bool{"dist": true}

func tput(t *testing.T, root, rel, s string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// same 增量快照必须与全量遍历完全相同。
func same(t *testing.T, tr *Tree, step string) uint64 {
	t.Helper()
	got, gen, err := tr.Snapshot(context.Background(), false)
	if err != nil {
		t.Fatalf("%s：%v", step, err)
	}
	want, err := WalkSkip(context.Background(), tr.Root(), 0, testSkip)
	if err != nil {
		t.Fatal(err)
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok || g.Size != w.Size || !g.ModTime.Equal(w.ModTime) {
			t.Fatalf("%s：%s 增量=%+v(%v) 全量=%+v", step, k, g, ok, w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Fatalf("%s：增量多出 %s", step, k)
		}
	}
	return gen
}

func TestTreeIncremental(t *testing.T) {
	root := t.TempDir()
	tput(t, root, "a/1.txt", "1")
	tput(t, root, "a/b/2.txt", "22")
	tput(t, root, "c/3.txt", "333")
	tr := NewTree(root, 0, testSkip)
	defer tr.Close()
	g0 := same(t, tr, "首次")
	if !tr.Watching() {
		t.Skip("本机文件系统不支持变更通知（" + runtime.GOOS + "）")
	}
	if g := same(t, tr, "无改动"); g != g0 || tr.Stats().Reused != 1 {
		t.Fatalf("无改动应复用、代号不变：%d→%d %+v", g0, g, tr.Stats())
	}
	steps := []struct {
		name string
		do   func()
	}{
		{"新建文件", func() { tput(t, root, "a/new.txt", "n") }},
		{"改大小", func() { tput(t, root, "a/b/2.txt", "2222") }},
		{"只改 mtime", func() {
			at := time.Now().Add(time.Hour)
			_ = os.Chtimes(filepath.Join(root, "c", "3.txt"), at, at)
		}},
		{"删文件", func() { _ = os.Remove(filepath.Join(root, "a", "1.txt")) }},
		{"新建多层目录", func() { tput(t, root, "d/e/f/4.txt", "4"); tput(t, root, "d/e/5.txt", "5") }},
		{"目录改名", func() { _ = os.Rename(filepath.Join(root, "d", "e"), filepath.Join(root, "d", "g")) }},
		{"删整棵", func() { _ = os.RemoveAll(filepath.Join(root, "d")) }},
		{"跳过的目录", func() { tput(t, root, "dist/x.js", "x"); tput(t, root, "node_modules/y.js", "y") }},
		{"加 .gitignore", func() { tput(t, root, "a/.gitignore", "*.txt\n") }},
		{"改 .gitignore", func() { tput(t, root, "a/.gitignore", "b/\n") }},
		{"根目录 .gitignore", func() { tput(t, root, ".gitignore", "c/\n") }},
		{"删 .gitignore", func() { _ = os.Remove(filepath.Join(root, ".gitignore")) }},
		{"目录换成文件", func() { _ = os.RemoveAll(filepath.Join(root, "c")); tput(t, root, "c", "file") }},
		{"文件换成目录", func() { _ = os.Remove(filepath.Join(root, "c")); tput(t, root, "c/z.txt", "z") }},
		{"同目录先删后建同名", func() { _ = os.RemoveAll(filepath.Join(root, "c")); tput(t, root, "c/w.txt", "w") }},
	}
	for _, s := range steps {
		s.do()
		same(t, tr, s.name)
	}
	if st := tr.Stats(); st.Incremental == 0 {
		t.Fatalf("应走过增量：%+v", st)
	}
}

// 随机改动，每批之后比对。
func TestTreeRandom(t *testing.T) {
	root := t.TempDir()
	tr := NewTree(root, 0, testSkip)
	defer tr.Close()
	same(t, tr, "空")
	if !tr.Watching() {
		t.Skip("不支持变更通知")
	}
	r := rand.New(rand.NewSource(1))
	dirs := []string{"", "p", "p/q", "r", "r/s/t", "dist", "u"}
	for round := range 60 {
		for range 1 + r.Intn(6) {
			d := dirs[r.Intn(len(dirs))]
			rel := filepath.ToSlash(filepath.Join(d, fmt.Sprintf("f%d.txt", r.Intn(5))))
			switch r.Intn(5) {
			case 0, 1:
				tput(t, root, rel, fmt.Sprint(r.Int()))
			case 2:
				_ = os.Remove(filepath.Join(root, filepath.FromSlash(rel)))
			case 3:
				if d != "" {
					_ = os.RemoveAll(filepath.Join(root, filepath.FromSlash(d)))
				}
			case 4:
				if d != "" {
					to := filepath.Join(root, fmt.Sprintf("m%d", r.Intn(3)))
					_ = os.RemoveAll(to)
					_ = os.Rename(filepath.Join(root, filepath.FromSlash(d)), to)
				}
			}
		}
		same(t, tr, fmt.Sprintf("第 %d 批", round))
	}
}

// fakeWatcher 测试替身：按设定返回通知 / 溢出 / 错误。
type fakeWatcher struct {
	cs     []change
	over   bool
	err    error
	closed int
}

func (w *fakeWatcher) recursive() bool          { return true }
func (w *fakeWatcher) add(string, string) error { return nil }
func (w *fakeWatcher) drain() ([]change, bool, error) {
	cs, over, err := w.cs, w.over, w.err
	w.cs, w.over, w.err = nil, false, nil
	return cs, over, err
}
func (w *fakeWatcher) close() error { w.closed++; return nil }

// 溢出、监视器报错一律退回全量；起不来时每次全量。
func TestTreeFallback(t *testing.T) {
	root := t.TempDir()
	tput(t, root, "a/1.txt", "1")
	fw := &fakeWatcher{}
	tr := NewTree(root, 0, testSkip)
	tr.open = func(string) (watcher, error) { return fw, nil }
	same(t, tr, "首次")
	// 溢出：通知丢了，监视器不知道 a/2.txt；必须全量才能发现
	tput(t, root, "a/2.txt", "2")
	fw.over = true
	same(t, tr, "溢出")
	if st := tr.Stats(); st.Overflow != 1 || st.Full != 2 {
		t.Fatalf("溢出应退回全量：%+v", st)
	}
	// 监视器报错：关掉，全量；之后重新打开
	tput(t, root, "a/3.txt", "3")
	fw.err = errors.New("boom")
	same(t, tr, "报错")
	if fw.closed != 1 || tr.Stats().Full != 3 || !tr.Watching() {
		t.Fatalf("报错应关掉监视器并全量后重开：closed=%d %+v", fw.closed, tr.Stats())
	}
	// 根目录本身的通知：全量
	tput(t, root, "4.txt", "4")
	fw.cs = []change{{path: "", replaced: true}}
	same(t, tr, "根目录")
	if tr.Stats().Full != 4 {
		t.Fatalf("根目录变动应全量：%+v", tr.Stats())
	}
	if err := tr.Close(); err != nil || fw.closed != 2 || tr.Watching() {
		t.Fatalf("Close 应释放监视器：%v %d", err, fw.closed)
	}
	// 关闭后仍能用，但每次全量、不再打开监视器
	same(t, tr, "关闭后")
	if tr.Watching() {
		t.Fatal("关闭后不应再启动监视器")
	}

	tr2 := NewTree(root, 0, testSkip)
	opened := 0
	tr2.open = func(string) (watcher, error) { opened++; return nil, errNoWatch }
	same(t, tr2, "起不来 1")
	tput(t, root, "5.txt", "5")
	same(t, tr2, "起不来 2")
	if st := tr2.Stats(); st.Full != 2 || opened != 1 {
		t.Fatalf("起不来时每次全量、只试一次：%+v opened=%d", st, opened)
	}
}

// 根目录被整个换掉（删掉重建）：旧监视器盯的不是它，全量。
func TestTreeRootReplaced(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	tput(t, root, "a.txt", "a")
	tr := NewTree(root, 0, testSkip)
	defer tr.Close()
	same(t, tr, "首次")
	if !tr.Watching() {
		t.Skip("不支持变更通知")
	}
	if err := tr.Close(); err != nil { // Windows 上目录句柄会挡住改名，先放开
		t.Fatal(err)
	}
	tr = NewTree(root, 0, testSkip)
	same(t, tr, "重建")
	full := tr.Stats().Full
	if runtime.GOOS != "windows" {
		if err := os.Rename(root, filepath.Join(base, "old")); err != nil {
			t.Fatal(err)
		}
		tput(t, root, "b.txt", "b")
		same(t, tr, "换根")
		if tr.Stats().Full != full+1 {
			t.Fatalf("根目录换了应全量：%+v", tr.Stats())
		}
	}
	tr.Close()
}

func TestTreeTooMany(t *testing.T) {
	root := t.TempDir()
	tput(t, root, "a/1.txt", "1")
	tput(t, root, "a/2.txt", "2")
	tr := NewTree(root, 2, testSkip)
	defer tr.Close()
	if _, _, err := tr.Snapshot(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	tput(t, root, "a/3.txt", "3")
	if _, _, err := tr.Snapshot(context.Background(), false); !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("增量超上限应报 ErrTooManyFiles：%v", err)
	}
	_ = os.Remove(filepath.Join(root, "a", "3.txt"))
	if m, _, err := tr.Snapshot(context.Background(), false); err != nil || len(m) != 2 {
		t.Fatalf("回到上限内应恢复：%d %v", len(m), err)
	}
}

// 关闭后没有残留 goroutine，Linux 上 inotify fd 也已关闭。
func TestTreeCloseNoLeak(t *testing.T) {
	root := t.TempDir()
	tput(t, root, "a/1.txt", "1")
	g0, fd0 := runtime.NumGoroutine(), openFDs()
	for i := range 5 {
		tr := NewTree(root, 0, testSkip)
		same(t, tr, "开")
		if i == 0 && !tr.Watching() {
			t.Log("不支持变更通知，只查 goroutine")
		}
		tput(t, root, "a/1.txt", fmt.Sprint(i))
		same(t, tr, "改")
		if err := tr.Close(); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if g := runtime.NumGoroutine(); g > g0 {
		t.Fatalf("goroutine %d → %d", g0, g)
	}
	if fd := openFDs(); fd0 >= 0 && fd > fd0 {
		t.Fatalf("文件描述符 %d → %d", fd0, fd)
	}
}

// openFDs 本进程打开的 fd 数（Linux；其他平台 -1）。
func openFDs() int {
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(es)
}
