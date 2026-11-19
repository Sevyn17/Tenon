package checkpoint

// shell / MCP / 扩展进程的改动发现（spike 第二节 T9）：带 may_write_files 的调用前后各扫一次工作区
// （stat 大小 + mtime），把调用期间变了的文件记成 source=shell。后台 job 在调用返回后写的文件
// 由回合末补扫（AfterTurn）记进该回合。
//
// before 内容来源（按顺序）：
//  1. 调用前不存在 → 空（还原 = 删除）。
//  2. 本会话上一条记录的 after，且调用前的 stat 和那条记录对得上（大小相同、mtime 不晚于记录时间）。
//  3. 工作区在 git 里、调用前 git status 显示该文件干净、且 HEAD 里有 → HEAD 的内容：原始 blob 或经过滤器
//     （行尾、smudge）转换后的字节，取大小等于调用前 stat 的那份；都对不上就走第 4 条。
//  4. 以上都不满足 → 不可还原，why 说明原因。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Sevyn17/tenon/modules/internal/wsfs"
	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// SourceShell 记录来源：may_write_files 工具（shell、扩展进程、非只读 MCP）调用前后扫描发现的改动，
// 以及回合末补扫发现的后台 job 改动（CallID 为 "turn-<n>/scan"）。
const SourceShell = "shell"

// DefaultScanMaxFiles 扫描上限缺省值（与 adapters.MaxScanFiles 一致）。
const DefaultScanMaxFiles = 20000

// ScanSkipDirs 扫描时额外跳过的目录名（任意层级）；wsfs.SkipDirs（.git、node_modules、vendor）与
// .gitignore 里忽略的路径本来就不扫。另外总是跳过本程序的项目数据目录（appid.DirName()，会话库在里面）。
var ScanSkipDirs = []string{".venv", "venv", "__pycache__", ".tox", ".mypy_cache", ".pytest_cache",
	"dist", "target", ".next", ".nuxt", ".gradle", ".cache"}

// ScanSkippedMessage 工作区文件太多、没有扫描时 changes.list 的说明。
const ScanSkippedMessage = "工作区文件太多（超过 %d 个），shell / MCP / 扩展进程的改动未跟踪"

const unknownHash = "unknown"

// scanSnap 一个会话最近一次扫描的结果（回合末补扫与它比）。
type scanSnap struct {
	stats   map[string]wsfs.FileStat
	dirty   map[string]bool // 不能用 HEAD 当基线的文件（path_key）：调用前已改或之后被改过
	gitOK   bool
	pending bool // 本回合有 may_write 或 job_* 调用，回合末要补扫
	turn    int
}

func (e *Engine) scanMax() int {
	if e.ScanMaxFiles <= 0 {
		return DefaultScanMaxFiles
	}
	return e.ScanMaxFiles
}

func (e *Engine) skipDirs() map[string]bool {
	m := map[string]bool{appid.DirName(): true}
	for _, d := range ScanSkipDirs {
		m[d] = true
	}
	return m
}

// ScanSkipped 本会话是否因为工作区文件太多而没有扫描过（进程内状态）。返回给 changes.list 的说明，空 = 没跳过。
func (e *Engine) ScanSkipped(session string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tooMany[session] {
		return fmt.Sprintf(ScanSkippedMessage, e.scanMax())
	}
	return ""
}

func (e *Engine) markTooMany(session string) {
	e.mu.Lock()
	if e.tooMany == nil {
		e.tooMany = map[string]bool{}
	}
	first := !e.tooMany[session]
	e.tooMany[session] = true
	e.mu.Unlock()
	if first && e.Notify != nil {
		e.Notify(session, fmt.Sprintf(ScanSkippedMessage, e.scanMax()))
	}
}

// walk 扫一次工作区；超上限时标注会话并返回 nil。
func (e *Engine) walk(ctx context.Context, session string) map[string]wsfs.FileStat {
	m, err := wsfs.WalkSkip(ctx, e.Root, e.scanMax(), e.skipDirs())
	if err != nil {
		if errors.Is(err, wsfs.ErrTooManyFiles) {
			e.markTooMany(session)
		}
		return nil
	}
	return m
}

// preDirty 调用前 git status（不含未跟踪文件）里出现的文件；不是 git 仓库或失败时 gitOK = false。
func (e *Engine) preDirty(ctx context.Context) (map[string]bool, bool) {
	if !e.gitProbe().ok {
		return nil, false
	}
	st, err := e.gitStatus(ctx, false)
	if err != nil {
		return nil, false
	}
	d := make(map[string]bool, len(st))
	for k := range st {
		d[k] = true
	}
	return d, true
}

// markPending job_* 调用：回合末补扫。
func (e *Engine) markPending(session string, turn int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s := e.snaps[session]; s != nil {
		s.pending, s.turn = true, turn
	}
}

func mayWrite(attrs map[string]any) bool {
	v, _ := attrs[toolhub.AttrMayWrite].(bool)
	return v
}

func isJobTool(name string) bool { return strings.HasPrefix(name, "job_") }

// scanCall may_write_files 调用：前后各扫一次。
func (e *Engine) scanCall(ctx context.Context, req component.ToolCallReq,
	next func(context.Context, component.ToolCallReq) (component.ToolCallResp, error)) (component.ToolCallResp, error) {
	pre := e.walk(ctx, req.SessionID)
	if pre == nil {
		return next(ctx, req)
	}
	dirty, gitOK := e.preDirty(ctx)
	resp, err := next(ctx, req)
	if resp.Output.Kind == toolhub.Confirm {
		return resp, err // 批准后整条链重跑，那时再扫
	}
	post := e.walk(ctx, req.SessionID)
	if post == nil {
		return resp, err
	}
	turn := TurnOf(req.Scope)
	changed := e.recordScan(ctx, req.SessionID, turn, req.CallID, req.Tool, pre, post, dirty, gitOK)
	nd := map[string]bool{}
	for k := range dirty {
		nd[k] = true
	}
	for _, k := range changed {
		nd[k] = true
	}
	e.mu.Lock()
	if e.snaps == nil {
		e.snaps = map[string]*scanSnap{}
	}
	e.snaps[req.SessionID] = &scanSnap{stats: post, dirty: nd, gitOK: gitOK, pending: true, turn: turn}
	e.mu.Unlock()
	return resp, err
}

// diffStats 两次扫描之间变了的文件（新增、删除、大小或 mtime 不同），按路径排序。
func diffStats(pre, post map[string]wsfs.FileStat) []string {
	var out []string
	for rel, b := range pre {
		if a, ok := post[rel]; !ok || a.Size != b.Size || !a.ModTime.Equal(b.ModTime) {
			out = append(out, rel)
		}
	}
	for rel := range post {
		if _, ok := pre[rel]; !ok {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// recordScan 两次扫描之间变了的文件各记一条 shell 记录（内容其实没变的不记）。返回变了的 path_key。
func (e *Engine) recordScan(ctx context.Context, session string, turn int, callID, tool string,
	pre, post map[string]wsfs.FileStat, dirty map[string]bool, gitOK bool) []string {
	var keys []string
	for _, rel := range diffStats(pre, post) {
		key := PathKey(rel)
		keys = append(keys, key)
		unlock := e.lockPaths(key)
		abs := filepath.Join(e.Root, filepath.FromSlash(rel))
		after, err := readState(abs, e.maxFile())
		if err != nil || after.special {
			unlock()
			continue
		}
		ps, existed := pre[rel]
		before := e.shellBefore(ctx, session, rel, key, existed, ps, gitOK && !dirty[key])
		if before.key() != after.key() {
			_ = e.record(ctx, Change{Session: session, Turn: turn, CallID: callID, Tool: tool,
				Source: SourceShell, Path: rel, PathKey: key}, before, after)
			e.touch(session, key, rel)
		}
		unlock()
	}
	return keys
}

// shellBefore 一个被进程外工具改过的文件在调用前的状态（来源顺序见文件头）。
func (e *Engine) shellBefore(ctx context.Context, session, rel, key string, existed bool, ps wsfs.FileStat, clean bool) fileState {
	if !existed {
		return fileState{}
	}
	if last, _ := e.lastFor(ctx, session, key); last != nil && last.After != "" && SameRoot(last.Root, e.Root) &&
		last.AfterSize == ps.Size && !ps.ModTime.After(time.UnixMilli(last.At).Add(time.Millisecond)) {
		st := fileState{exists: true, hash: last.After, size: last.AfterSize}
		if !last.AfterKept {
			st.unknown = "本会话上一次记录这个文件时没有存下内容"
			return st
		}
		b, err := e.Content(last.After)
		if err != nil {
			st.unknown = "上一次记录的内容已不在工件存储里"
			return st
		}
		st.data, st.binary = b, isBinary(b)
		return st
	}
	if clean {
		if b, ok := e.gitHead(ctx, rel, ps.Size); ok {
			return stateOf(b, e.maxFile())
		}
	}
	why := "shell 等进程外工具改的，调用前内容未知（本会话没有记录过这个文件"
	if e.gitProbe().ok {
		why += "，也不是 git 里未改动的已跟踪文件）"
	} else {
		why += "，工作区也不是 git 仓库）"
	}
	return fileState{exists: true, hash: unknownHash, size: ps.Size, unknown: why}
}

// stateOf 内存里一份内容的状态；超过 max 时不留内容（只记哈希）。
func stateOf(b []byte, max int64) fileState {
	sum := sha256.Sum256(b)
	st := fileState{exists: true, hash: hex.EncodeToString(sum[:]), size: int64(len(b)), binary: isBinary(b)}
	if int64(len(b)) <= max {
		st.data = b
	}
	return st
}

// rescan 回合末补扫（本回合有 may_write 或 job_* 调用时）：和最近一次扫描比，后台 job 在调用返回后
// 写的文件记进该回合（CallID "turn-<n>/scan"）。已被别的记录覆盖的（内容和最后一条记录相同）不重复记。
func (e *Engine) rescan(ctx context.Context, session string) {
	e.mu.Lock()
	s := e.snaps[session]
	if s == nil || !s.pending {
		e.mu.Unlock()
		return
	}
	s.pending = false
	turn, gitOK := s.turn, s.gitOK
	pre := make(map[string]wsfs.FileStat, len(s.stats)) // 拷贝：noteWrite 在锁内改 s.stats
	for k, v := range s.stats {
		pre[k] = v
	}
	dirty := make(map[string]bool, len(s.dirty))
	for k := range s.dirty {
		dirty[k] = true
	}
	e.mu.Unlock()
	post := e.walk(ctx, session)
	if post == nil {
		return
	}
	changed := e.recordScan(ctx, session, turn, fmt.Sprintf("turn-%d/scan", turn), "job", pre, post, dirty, gitOK)
	e.mu.Lock()
	for _, k := range changed {
		s.dirty[k] = true
	}
	s.stats = post
	e.mu.Unlock()
}

// noteWrite write_file / edit 记过一条之后：更新本会话最近一次扫描里这个文件的 stat，并把它标成「不能用 HEAD 当基线」，
// 回合末补扫不会把这次写入再记一遍。没有扫描快照时什么都不做。
func (e *Engine) noteWrite(session, rel, key, abs string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.snaps[session]
	if s == nil {
		return
	}
	s.dirty[key] = true
	if fi, err := os.Lstat(abs); err == nil && fi.Mode().IsRegular() {
		s.stats[rel] = wsfs.FileStat{ModTime: fi.ModTime(), Size: fi.Size()}
	} else {
		delete(s.stats, rel)
	}
}
