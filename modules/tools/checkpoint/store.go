// Package checkpoint 文件检查点（docs/50 M9，spike-checkpoint 方案 A）：工具写文件前后快照，
// 按会话记录改动，给出 diff，并能按会话 / 回合还原。
//
// 记录按工具调用存（一条调用一个文件一条），内容按 sha256 存进工件存储（引用挂在会话上，
// 删会话后由存储的 GC 回收），元数据存进集合 checkpoint.changes（索引 session、path_key）。
package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 缺省上限（docs/50 7.2 第 17 条）。
const (
	DefaultMaxFileBytes    = 2 << 20
	DefaultMaxSessionBytes = 256 << 20
	binarySniff            = 8000 // 与 grep 的二进制判定一致
)

// 记录来源。
const (
	SourceFS     = "fs"
	SourceDrift  = "drift"
	SourceRevert = "revert"
)

// Change 一条改动记录（集合 checkpoint.changes 里的一条文档）。
// Before / After 为内容 sha256；"" = 该时刻文件不存在。
type Change struct {
	Key     string `json:"-"`
	Session string `json:"session"`
	Turn    int    `json:"turn"`
	CallID  string `json:"call_id"` // run_code 子调用为 "<父>#<n>"；drift 为 "turn-<n>/drift"；还原为 "revert/<seq>"
	Tool    string `json:"tool"`
	Source  string `json:"source"` // fs | shell | drift | revert
	Root    string `json:"root"`   // 工作目录（正斜杠）
	Path    string `json:"path"`   // 相对 root，正斜杠，保留原大小写
	PathKey string `json:"path_key"`
	Before  string `json:"before"`
	After   string `json:"after"`
	// BeforeKept / AfterKept 内容是否存进了工件存储（太大、配额满、没有工件存储时为 false）。
	BeforeKept bool   `json:"before_kept"`
	AfterKept  bool   `json:"after_kept"`
	BeforeSize int64  `json:"before_size"`
	AfterSize  int64  `json:"after_size"`
	Binary     bool   `json:"binary"`
	Revertible bool   `json:"revertible"`
	Why        string `json:"why,omitempty"`
	At         int64  `json:"at"`  // Unix 毫秒
	Seq        string `json:"seq"` // 全局递增序号（定宽十进制，按字符串排序即时间顺序）
}

// ByCall 这条记录是否属于一个真实的工具调用（CallID 能在会话日志里找到）：fs 记录，以及调用前后扫描得到的
// shell 记录。drift、回合末补扫（"turn-<n>/scan"）与 revert 记录不是，不经 Live 判断。
func (c Change) ByCall() bool {
	return c.Source == SourceFS || c.Source == SourceShell && !strings.HasPrefix(c.CallID, "turn-")
}

// Engine 检查点引擎：中间件、回合末对账、删会话清理、diff 与还原都在这里。
// 进程内单例；并发安全。
type Engine struct {
	Root string // 工作目录（绝对路径）
	Coll store.Collection
	// Artifacts 取工件存储（存储 Start 之后才可用，所以是函数）；返回 nil = 只记哈希、不可还原。
	Artifacts       func() toolhub.Artifacts
	MaxFileBytes    int64
	MaxSessionBytes int64
	// Notify 会话配额满等提示（可空）。
	Notify func(session, msg string)
	Now    func() time.Time
	// ScanMaxFiles may_write_files 调用前后扫描的文件数上限（0 = DefaultScanMaxFiles）；超过不扫，
	// changes.list 用 ScanSkipped 标注。
	ScanMaxFiles int
	// NoGit 不调用 git（测试模拟没有 git 的机器）。
	NoGit bool

	gitOnce  sync.Once
	git      gitInfo
	snaps    map[string]*scanSnap // 会话 → 最近一次扫描
	tooMany  map[string]bool      // 会话 → 因文件太多跳过过扫描
	mu       sync.Mutex
	lastSeq  int64
	used     map[string]int64           // 会话 → 本进程内新存的内容字节数
	stored   map[string]map[string]bool // 会话 → 已存过的哈希（不重复计配额）
	warned   map[string]bool
	touched  map[string]map[string]string // 会话 → path_key → 相对路径（本回合碰过，回合末对账）
	pathLock map[string]*sync.Mutex
}

// New 构造引擎。
func New(root string, coll store.Collection, art func() toolhub.Artifacts) *Engine {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &Engine{Root: root, Coll: coll, Artifacts: art, MaxFileBytes: DefaultMaxFileBytes,
		MaxSessionBytes: DefaultMaxSessionBytes, Now: time.Now}
}

// FoldCase 本平台的文件系统是否按不区分大小写比较路径（Windows、macOS）。
func FoldCase() bool { return foldFor(runtime.GOOS) }

func foldFor(goos string) bool { return goos == "windows" || goos == "darwin" }

// PathKey 相对路径的比对键：正斜杠；Windows / macOS 上小写。
func PathKey(rel string) string { return pathKeyFor(runtime.GOOS, rel) }

func pathKeyFor(goos, rel string) string {
	rel = strings.ReplaceAll(rel, "\\", "/")
	if foldFor(goos) {
		return strings.ToLower(rel)
	}
	return rel
}

// SameRoot 两个工作目录规范化后是否相同（Windows / macOS 不区分大小写）。
func SameRoot(a, b string) bool { return sameRootFor(runtime.GOOS, a, b) }

func sameRootFor(goos, a, b string) bool {
	n := func(s string) string {
		s = strings.TrimRight(strings.ReplaceAll(filepath.Clean(s), "\\", "/"), "/")
		if foldFor(goos) {
			s = strings.ToLower(s)
		}
		return s
	}
	return n(a) == n(b)
}

func (e *Engine) maxFile() int64 {
	if e.MaxFileBytes <= 0 {
		return DefaultMaxFileBytes
	}
	return e.MaxFileBytes
}

func (e *Engine) maxSession() int64 {
	if e.MaxSessionBytes <= 0 {
		return DefaultMaxSessionBytes
	}
	return e.MaxSessionBytes
}

func (e *Engine) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

func (e *Engine) art() toolhub.Artifacts {
	if e.Artifacts == nil {
		return nil
	}
	return e.Artifacts()
}

// nextSeq 全局递增序号（纳秒时间，同一纳秒内加一）。
func (e *Engine) nextSeq() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := e.now().UnixNano()
	if n <= e.lastSeq {
		n = e.lastSeq + 1
	}
	e.lastSeq = n
	return fmt.Sprintf("%020d", n)
}

// lockPaths 对一组 path_key 加进程内锁（按键排序，防死锁），返回解锁函数。
// 中间件快照与还原互斥。
func (e *Engine) lockPaths(keys ...string) func() {
	keys = append([]string(nil), keys...)
	sort.Strings(keys)
	e.mu.Lock()
	if e.pathLock == nil {
		e.pathLock = map[string]*sync.Mutex{}
	}
	var ls []*sync.Mutex
	prev := "\x00"
	for _, k := range keys {
		if k == prev {
			continue
		}
		prev = k
		l := e.pathLock[k]
		if l == nil {
			l = &sync.Mutex{}
			e.pathLock[k] = l
		}
		ls = append(ls, l)
	}
	e.mu.Unlock()
	for _, l := range ls {
		l.Lock()
	}
	return func() {
		for i := len(ls) - 1; i >= 0; i-- {
			ls[i].Unlock()
		}
	}
}

// —— 文件状态 ——//

// fileState 某一时刻磁盘上的一个文件。
type fileState struct {
	exists  bool
	hash    string
	size    int64
	data    []byte // nil = 太大没读进内存（只算了哈希）
	binary  bool
	special bool // 目录、符号链接或其他非普通文件
	// unknown 内容未知、不能还原的原因（shell 改动且没有基线）；非空时 data 为 nil。
	unknown string
}

// key 状态比对键：不存在为 ""。
func (s fileState) key() string {
	if !s.exists {
		return ""
	}
	return s.hash
}

func readState(abs string, max int64) (fileState, error) {
	fi, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	if !fi.Mode().IsRegular() {
		return fileState{exists: true, special: true}, nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return fileState{}, err
	}
	defer f.Close()
	st := fileState{exists: true, size: fi.Size()}
	if fi.Size() <= max {
		b, err := io.ReadAll(f)
		if err != nil {
			return fileState{}, err
		}
		sum := sha256.Sum256(b)
		st.hash, st.data, st.size, st.binary = hex.EncodeToString(sum[:]), b, int64(len(b)), isBinary(b)
		return st, nil
	}
	h := sha256.New()
	head := make([]byte, binarySniff)
	n, _ := io.ReadFull(f, head)
	st.binary = isBinary(head[:n])
	h.Write(head[:n])
	m, err := io.Copy(h, f)
	if err != nil {
		return fileState{}, err
	}
	st.hash, st.size = hex.EncodeToString(h.Sum(nil)), int64(n)+m
	return st, nil
}

func isBinary(b []byte) bool {
	if len(b) > binarySniff {
		b = b[:binarySniff]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// keep 把状态的内容存进工件存储，返回是否存下与不存的原因。不存在的文件不需要存（还原 = 删除）。
func (e *Engine) keep(ctx context.Context, session string, s fileState) (bool, string) {
	if !s.exists {
		return false, ""
	}
	if s.unknown != "" {
		return false, s.unknown
	}
	if s.data == nil {
		return false, fmt.Sprintf("文件太大（%d 字节，上限 %d）", s.size, e.maxFile())
	}
	a := e.art()
	if a == nil {
		return false, "没有工件存储"
	}
	e.mu.Lock()
	if e.stored == nil {
		e.stored, e.used, e.warned = map[string]map[string]bool{}, map[string]int64{}, map[string]bool{}
	}
	seen := e.stored[session][s.hash]
	full := !seen && e.used[session]+int64(len(s.data)) > e.maxSession()
	notify := full && !e.warned[session]
	if notify {
		e.warned[session] = true
	}
	e.mu.Unlock()
	if full {
		if notify && e.Notify != nil {
			e.Notify(session, fmt.Sprintf("本会话的文件检查点已达 %d 字节上限，之后的改动只记录哈希、不能还原", e.maxSession()))
		}
		return false, "会话检查点配额已满"
	}
	if _, err := a.Put(ctx, session, "application/octet-stream", s.data); err != nil {
		return false, "存内容失败：" + err.Error()
	}
	if !seen {
		e.mu.Lock()
		if e.stored[session] == nil {
			e.stored[session] = map[string]bool{}
		}
		e.stored[session][s.hash] = true
		e.used[session] += int64(len(s.data))
		e.mu.Unlock()
	}
	return true, ""
}

// Content 按哈希取一份内容。"" 表示文件不存在，返回 nil。
func (e *Engine) Content(hash string) ([]byte, error) {
	if hash == "" {
		return nil, nil
	}
	a := e.art()
	if a == nil {
		return nil, fmt.Errorf("checkpoint: 没有工件存储")
	}
	return a.Get(hash)
}

// —— 集合读写 ——//

func (c Change) body() (map[string]any, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["tenant"] = store.Tenant
	return m, nil
}

func decodeChange(d store.Doc) (Change, error) {
	b, err := json.Marshal(d.Body)
	if err != nil {
		return Change{}, err
	}
	var c Change
	if err := json.Unmarshal(b, &c); err != nil {
		return Change{}, err
	}
	c.Key = d.Key
	return c, nil
}

func (e *Engine) put(ctx context.Context, c Change) error {
	if e.Coll == nil {
		return fmt.Errorf("checkpoint: 没有集合")
	}
	body, err := c.body()
	if err != nil {
		return err
	}
	return e.Coll.Put(ctx, store.Tenant, store.Doc{Key: c.Key, Body: body}, 0)
}

// query 按索引查全部（翻页），按 Seq 排序。
func (e *Engine) query(ctx context.Context, index string, eq any) ([]Change, []store.Doc, error) {
	if e.Coll == nil {
		return nil, nil, nil
	}
	var out []Change
	var docs []store.Doc
	page := store.Page{Limit: store.CollectionPageMax}
	for {
		ds, next, err := e.Coll.Query(ctx, store.Tenant, index, eq, page)
		if err != nil {
			return nil, nil, err
		}
		for _, d := range ds {
			c, err := decodeChange(d)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, c)
			docs = append(docs, d)
		}
		if next.After == "" {
			break
		}
		page.After = next.After
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, docs, nil
}

// List 本会话的全部改动记录，按发生顺序。
func (e *Engine) List(ctx context.Context, session string) ([]Change, error) {
	cs, _, err := e.query(ctx, "session", session)
	return cs, err
}

// lastFor 本会话某个文件的最后一条记录（没有返回 nil）。
func (e *Engine) lastFor(ctx context.Context, session, key string) (*Change, error) {
	cs, _, err := e.query(ctx, "path_key", key)
	if err != nil {
		return nil, err
	}
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i].Session == session {
			c := cs[i]
			return &c, nil
		}
	}
	return nil, nil
}

// DeleteSession 删会话时清掉本会话的全部记录（OnSessionDelete 点位，store.Cleaner）。
// 内容的工件引用由存储随会话删除，GC 回收。
func (e *Engine) DeleteSession(ctx context.Context, session string) error {
	_, docs, err := e.query(ctx, "session", session)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := e.Coll.Delete(ctx, store.Tenant, d.Key, d.Version); err != nil && err != store.ErrNotFound {
			return err
		}
	}
	e.mu.Lock()
	delete(e.used, session)
	delete(e.stored, session)
	delete(e.warned, session)
	delete(e.touched, session)
	delete(e.snaps, session)
	delete(e.tooMany, session)
	e.mu.Unlock()
	return nil
}
