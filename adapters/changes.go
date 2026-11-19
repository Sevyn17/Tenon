package adapters

// 改动审阅与还原（客户端改进 M9）：把 checkpoint 插件的引擎接成 appapi.Changes。
// 内容哈希一律从本会话的记录里取：changes.diff 只收路径（查找键），不收哈希，
// 别的会话的记录不会被查到（Engine.Content 本身不按会话过滤，所以不能把哈希交给客户端选）。

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/modules/tools/checkpoint"
	"github.com/Sevyn17/tenon/runtime/store"
)

// diff 上限（spike 第五节）：超过只给前一部分。
const (
	MaxDiffLines = 5000
	MaxDiffBytes = 512 << 10
)

// Changes appapi.Changes 的 checkpoint 实现。
type Changes struct{ E *checkpoint.Engine }

// net 一个文件在范围内的净改动（全部来自本会话的记录）。
type net struct {
	path, key       string
	before, after   string // 内容哈希；"" = 不存在
	bKept, aKept    bool
	sources         []string
	turns           []uint32
	rewound         bool
	firstRevertible bool
	firstWhy        string
	binary          bool
}

func scopeOf(s string) string {
	if s == "" {
		return wire.ChangeScopeSession
	}
	return s
}

func lookupKey(p string) string {
	return checkpoint.PathKey(path.Clean(strings.ReplaceAll(p, "\\", "/")))
}

// group 本会话记录按文件分组（保持发生顺序），文件按路径排序。
func group(cs []checkpoint.Change) ([]string, map[string][]checkpoint.Change) {
	by := map[string][]checkpoint.Change{}
	var keys []string
	for _, c := range cs {
		if by[c.PathKey] == nil {
			keys = append(keys, c.PathKey)
		}
		by[c.PathKey] = append(by[c.PathKey], c)
	}
	sort.Strings(keys)
	return keys, by
}

// netOf 算一个文件在范围内的净改动；范围内没有记录或净改动为空时 ok = false。
// 范围内的记录：非 revert、回合在范围内、fs 记录的调用还在日志里。session 范围里只剩已回退的调用时
// 按已回退显示（rewound）。终点：turn 范围是该回合最后一条的 after，其余是本文件最后一条记录的 after。
func netOf(cs []checkpoint.Change, scope string, turn int, live func(string) bool) (net, bool) {
	in := func(c checkpoint.Change) bool {
		switch scope {
		case wire.ChangeScopeTurn:
			return c.Turn == turn
		case wire.ChangeScopeFromTurn:
			return c.Turn >= turn
		}
		return true
	}
	var picked, dead []checkpoint.Change
	for _, c := range cs {
		if c.Source == checkpoint.SourceRevert || !in(c) {
			continue
		}
		if c.ByCall() && live != nil && !live(c.CallID) {
			dead = append(dead, c)
			continue
		}
		picked = append(picked, c)
	}
	rewound := false
	if len(picked) == 0 {
		if scope != wire.ChangeScopeSession || len(dead) == 0 {
			return net{}, false
		}
		picked, rewound = dead, true
	}
	first, last := picked[0], cs[len(cs)-1]
	if scope == wire.ChangeScopeTurn {
		last = picked[len(picked)-1]
	}
	n := net{path: last.Path, key: last.PathKey, before: first.Before, after: last.After,
		bKept: first.BeforeKept, aKept: last.AfterKept, rewound: rewound,
		firstRevertible: first.Revertible, firstWhy: first.Why}
	for _, c := range picked {
		if !slices.Contains(n.sources, c.Source) {
			n.sources = append(n.sources, c.Source)
		}
		if t := uint32(c.Turn); !slices.Contains(n.turns, t) {
			n.turns = append(n.turns, t)
		}
		if c.Binary {
			n.binary = true
		}
	}
	slices.Sort(n.turns)
	if n.before == n.after {
		return net{}, false
	}
	return n, true
}

// content 一侧的内容：哈希来自本会话记录。"" = 不存在（nil）；没存下返回 ok = false。
func (a Changes) content(hash string, kept bool) ([]byte, bool) {
	if hash == "" {
		return nil, true
	}
	if !kept {
		return nil, false
	}
	b, err := a.E.Content(hash)
	if err != nil {
		return nil, false
	}
	if b == nil {
		b = []byte{}
	}
	return b, true
}

func status(n net) string {
	switch {
	case n.before == "":
		return "added"
	case n.after == "":
		return "deleted"
	}
	return "modified"
}

func wireConflict(c checkpoint.Conflict) wire.ChangeConflict {
	return wire.ChangeConflict{Path: c.Path, Reason: c.Kind, Message: c.Reason, By: c.Session, Forceable: c.Forceable}
}

// GitAvailable appapi.GitChanges：本机有 git 且工作区在仓库里（能力 changes.git）。
func (a Changes) GitAvailable() bool { return a.E.GitAvailable() }

// gitList changes.list {scope:"git"}：工作区相对 HEAD 的未提交改动，只读（Revertible 恒为 false）。
func (a Changes) gitList(ctx context.Context) (wire.ChangesResult, error) {
	fs, err := a.E.GitList(ctx)
	if err != nil {
		return wire.ChangesResult{}, err
	}
	out := wire.ChangesResult{Files: []wire.ChangeFile{}}
	for _, f := range fs {
		out.Files = append(out.Files, wire.ChangeFile{Path: f.Path, Status: f.Status, Added: f.Added, Removed: f.Removed,
			Binary: f.Binary, Untracked: f.Untracked, Why: "git 范围只读"})
	}
	return out, nil
}

// gitDiff changes.diff {scope:"git"}：只接受当前未提交改动列表里的路径。
func (a Changes) gitDiff(ctx context.Context, p string) (wire.ChangesDiffResult, error) {
	d, err := a.E.GitDiff(ctx, path.Clean(strings.ReplaceAll(p, "\\", "/")))
	switch {
	case errors.Is(err, checkpoint.ErrNotInGit):
		return wire.ChangesDiffResult{}, fmt.Errorf("%w: %v", store.ErrNotFound, err)
	case errors.Is(err, checkpoint.ErrTooLarge):
		return wire.ChangesDiffResult{Path: d.Path, Binary: d.Binary, NewSize: d.NewSize,
			Unavailable: "文件太大，给不出 diff"}, nil
	case err != nil:
		return wire.ChangesDiffResult{}, err
	}
	out := wire.ChangesDiffResult{Path: d.Path, Binary: d.Binary, OldSize: d.OldSize, NewSize: d.NewSize}
	out.Diff, out.Truncated = truncate(d.Unified)
	return out, nil
}

// List changes.list。
func (a Changes) List(ctx context.Context, in wire.ChangesArgs, live func(string) bool) (wire.ChangesResult, error) {
	if in.Scope == wire.ChangeScopeGit {
		return a.gitList(ctx)
	}
	scope, turn := scopeOf(in.Scope), 0
	if in.Turn != nil {
		turn = int(*in.Turn)
	}
	cs, err := a.E.List(ctx, in.Session)
	if err != nil {
		return wire.ChangesResult{}, err
	}
	// 预检：演练一次整个范围的还原，冲突按文件挂上去。
	pre, err := a.E.Revert(ctx, checkpoint.RevertOptions{Session: in.Session, Scope: scope, Turn: turn, Live: live, DryRun: true})
	if err != nil && !errors.Is(err, checkpoint.ErrConflict) {
		return wire.ChangesResult{}, err
	}
	conf := map[string]checkpoint.Conflict{}
	for _, c := range pre.Conflicts {
		conf[lookupKey(c.Path)] = c
	}
	keys, by := group(cs)
	out := wire.ChangesResult{Files: []wire.ChangeFile{}}
	for _, k := range keys {
		n, ok := netOf(by[k], scope, turn, live)
		if !ok {
			continue
		}
		f := wire.ChangeFile{Path: n.path, Status: status(n), Binary: n.binary, Sources: n.sources, Turns: n.turns,
			Rewound: n.rewound, Revertible: true}
		if old, ok1 := a.content(n.before, n.bKept); ok1 {
			if cur, ok2 := a.content(n.after, n.aKept); ok2 {
				d := checkpoint.Diff(n.path, old, cur)
				f.Added, f.Removed, f.Binary = d.Added, d.Removed, f.Binary || d.Binary
			}
		}
		switch c, has := conf[k]; {
		case n.rewound:
			f.Revertible, f.Why = false, "所在回合已被回退"
		case !n.firstRevertible:
			f.Revertible, f.Why = false, n.firstWhy
			if f.Why == "" {
				f.Why = "没有保存原文"
			}
		case has && !c.Forceable:
			f.Revertible, f.Why = false, c.Reason
		case has:
			f.Conflict = c.Kind
		}
		out.Files = append(out.Files, f)
	}
	out.ScanSkipped = a.E.ScanSkipped(in.Session)
	return out, nil
}

// Diff changes.diff：只按本会话的记录找文件与内容。
func (a Changes) Diff(ctx context.Context, in wire.ChangesDiffArgs, live func(string) bool) (wire.ChangesDiffResult, error) {
	if in.Scope == wire.ChangeScopeGit {
		return a.gitDiff(ctx, in.Path)
	}
	scope, turn := scopeOf(in.Scope), 0
	if in.Turn != nil {
		turn = int(*in.Turn)
	}
	cs, err := a.E.List(ctx, in.Session)
	if err != nil {
		return wire.ChangesDiffResult{}, err
	}
	key := lookupKey(in.Path)
	var mine []checkpoint.Change
	for _, c := range cs {
		if c.Session == in.Session && c.PathKey == key {
			mine = append(mine, c)
		}
	}
	if len(mine) == 0 {
		return wire.ChangesDiffResult{}, fmt.Errorf("%w: 本会话没有改过 %s", store.ErrNotFound, in.Path)
	}
	n, ok := netOf(mine, scope, turn, live)
	if !ok {
		return wire.ChangesDiffResult{Path: mine[len(mine)-1].Path}, nil
	}
	out := wire.ChangesDiffResult{Path: n.path}
	old, ok1 := a.content(n.before, n.bKept)
	cur, ok2 := a.content(n.after, n.aKept)
	if !ok1 || !ok2 {
		out.Unavailable = "有一侧的内容没有保存（太大或配额已满），给不出 diff"
		out.Binary = n.binary
		return out, nil
	}
	d := checkpoint.Diff(n.path, old, cur)
	out.Binary, out.OldSize, out.NewSize = d.Binary, d.OldSize, d.NewSize
	out.Diff, out.Truncated = truncate(d.Unified)
	return out, nil
}

// truncate 超过 MaxDiffLines 行或 MaxDiffBytes 字节时在行边界截断。
func truncate(s string) (string, bool) {
	cut, lines := -1, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines++
			if lines >= MaxDiffLines {
				cut = i + 1
				break
			}
		}
	}
	if cut < 0 && len(s) <= MaxDiffBytes {
		return s, false
	}
	if cut < 0 || cut > MaxDiffBytes {
		cut = MaxDiffBytes
		if j := strings.LastIndexByte(s[:cut], '\n'); j >= 0 {
			cut = j + 1
		}
	}
	if cut >= len(s) {
		return s, false
	}
	return s[:cut], true
}

// Revert changes.revert 与 session.rewind {restore_files}。
func (a Changes) Revert(ctx context.Context, r appapi.ChangeRevert) (appapi.ChangeReverted, error) {
	res, err := a.E.Revert(ctx, checkpoint.RevertOptions{Session: r.Session, Scope: scopeOf(r.Scope), Turn: int(r.Turn),
		Paths: r.Paths, Live: r.Live, Force: r.Force, DryRun: r.DryRun})
	out := appapi.ChangeReverted{}
	for _, f := range res.Files {
		out.Files = append(out.Files, f.Path)
	}
	for _, c := range res.Conflicts {
		out.Conflicts = append(out.Conflicts, wireConflict(c))
	}
	if errors.Is(err, checkpoint.ErrConflict) {
		out.Blocked = true
		return out, nil
	}
	if res.Applied {
		e, sid := a.E, r.Session
		out.Undo = func(ctx context.Context) error { return e.Undo(ctx, sid, res) }
	}
	if err != nil {
		return out, err
	}
	return out, nil
}
