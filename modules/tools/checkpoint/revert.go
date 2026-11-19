package checkpoint

// 还原引擎（spike 第四、八节）：三种范围、冲突检测、还原前先存当前内容、原子写、路径检查。
// changes.revert 与 session.rewind {restore_files} 共用 Revert（协议接线在下一段）。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/Sevyn17/tenon/modules/internal/wsfs"
)

// 还原范围。
const (
	ScopeSession  = "session"   // 每个文件还原到本会话第一条记录的 before
	ScopeFromTurn = "from_turn" // 还原到第 T 回合及之后第一条记录的 before（rewind 用）
	ScopeTurn     = "turn"      // 只撤第 T 回合；之后的回合也改过该文件 → 冲突
)

// 冲突种类。Forceable 为真的冲突可用 Force 跳过；其余（越界、不可还原、别的工作区）永远拒绝。
const (
	ConflictModified     = "modified"      // 被外部程序或用户改过
	ConflictOtherSession = "other_session" // 别的会话之后改过
	ConflictDrift        = "drift"         // 范围内有 drift 记录（可能是用户手改）
	ConflictLaterTurn    = "later_turn"    // turn 范围：之后的回合也改了这个文件
	ConflictUnsaved      = "unsaved"       // 当前内容存不下（太大、配额满），覆盖后找不回
	ConflictUnrevertible = "unrevertible"  // 记录里没有原文（太大、没有工件存储）
	ConflictOtherRoot    = "other_root"    // 记录属于另一个工作区
	ConflictUnsafe       = "unsafe"        // 路径越界、经链接指向外面、目标成了目录或链接、记录被篡改
	ConflictLost         = "lost"          // 原文在工件存储里找不到了
)

// RevertOptions 一次还原。
type RevertOptions struct {
	Session string
	Scope   string // Scope* 常量；空 = ScopeSession
	Turn    int    // ScopeFromTurn / ScopeTurn 的回合号
	// Paths 只还原这些文件（相对路径，按 PathKey 比对）；空 = 范围内全部。
	Paths []string
	// Live 报告一个工具调用 id 是否还在当前日志里（rewind 截掉的调用返回 false，其记录视为已回退、不参与还原）。
	// nil = 全部有效。drift / revert 记录不经它判断。
	Live func(callID string) bool
	// Force 跳过可强制的冲突（前端二次确认后才设）。
	Force bool
	// DryRun 只演练：返回计划与冲突，不写盘、不写记录。
	DryRun bool
}

// FileAction 一个文件的还原动作。
type FileAction struct {
	Path    string // 相对工作目录，正斜杠
	PathKey string
	Delete  bool   // true = 删除（还原到「不存在」）；false = 写入 Target 内容
	Target  string // 还原到的内容哈希（Delete 时为 ""）
	Prev    string // 还原前磁盘上的内容哈希（"" = 不存在）
	// PrevKept 还原前的内容是否已存进工件存储（Undo 靠它写回）。
	PrevKept bool
}

// Conflict 一个不能直接还原的文件。
type Conflict struct {
	Path      string
	Kind      string // Conflict* 常量
	Reason    string // 给人看的原因
	Session   string // ConflictOtherSession：改过它的会话
	Forceable bool
}

// RevertResult 还原结果。有冲突且没写盘时 Files 是计划（未执行），Applied 为 false。
type RevertResult struct {
	Files     []FileAction
	Conflicts []Conflict
	Applied   bool
}

// ErrConflict 有冲突，整批没写。
var ErrConflict = errors.New("checkpoint: 有冲突，未还原")

type plan struct {
	act    FileAction
	abs    string
	data   []byte    // 目标内容（Delete 时 nil）
	cur    fileState // 还原前磁盘状态
	before fileState // 目标状态（写记录用）
}

// Revert 按范围还原本会话改过的文件。
//
// 规则：任一文件有不可强制的冲突，或有可强制的冲突而没设 Force，整批一个字节都不写，
// 返回 ErrConflict 与冲突列表。写之前先把每个将被覆盖的文件的当前内容存进工件存储，
// 写完补一条 source=revert 的记录（之后的工具调用不会误报 drift，Undo 也有材料）。
// 中途写失败时用存下的内容把已写的文件写回去，返回原错误。
func (e *Engine) Revert(ctx context.Context, o RevertOptions) (RevertResult, error) {
	if o.Scope == "" {
		o.Scope = ScopeSession
	}
	switch o.Scope {
	case ScopeSession, ScopeFromTurn, ScopeTurn:
	default:
		return RevertResult{}, fmt.Errorf("checkpoint: 未知范围 %q", o.Scope)
	}
	all, err := e.List(ctx, o.Session)
	if err != nil {
		return RevertResult{}, err
	}
	want := map[string]bool{}
	for _, p := range o.Paths {
		want[PathKey(path.Clean(strings.ReplaceAll(p, "\\", "/")))] = true
	}
	byFile := map[string][]Change{}
	var keys []string
	for _, c := range all {
		if len(want) > 0 && !want[c.PathKey] {
			continue
		}
		if c.Source == SourceFS && o.Live != nil && !o.Live(c.CallID) {
			continue // 已回退的回合
		}
		if byFile[c.PathKey] == nil {
			keys = append(keys, c.PathKey)
		}
		byFile[c.PathKey] = append(byFile[c.PathKey], c)
	}
	sort.Strings(keys)
	unlock := e.lockPaths(keys...)
	defer unlock()

	var res RevertResult
	var plans []plan
	for _, k := range keys {
		p, conf, ok := e.planFile(ctx, o, byFile[k])
		if conf != nil {
			res.Conflicts = append(res.Conflicts, *conf)
		}
		if ok {
			plans = append(plans, p)
			res.Files = append(res.Files, p.act)
		}
	}
	if blocked(res.Conflicts, o.Force) {
		return res, ErrConflict
	}
	if o.DryRun || len(plans) == 0 {
		return res, nil
	}
	// 先存当前内容（还没写盘）：存不下且没 Force → 整批不写。
	for i := range plans {
		p := &plans[i]
		if !p.cur.exists {
			continue
		}
		kept, why := e.keep(ctx, o.Session, p.cur)
		p.act.PrevKept = kept
		res.Files[i].PrevKept = kept
		if !kept {
			res.Conflicts = append(res.Conflicts, Conflict{Path: p.act.Path, Kind: ConflictUnsaved, Forceable: true,
				Reason: "当前内容无法保存（" + why + "），还原后找不回"})
		}
	}
	if blocked(res.Conflicts, o.Force) {
		return res, ErrConflict
	}
	for i, p := range plans {
		if err := apply(p.abs, p.act.Delete, p.data); err != nil {
			e.rollback(plans[:i])
			return res, fmt.Errorf("checkpoint: 还原 %s：%w", p.act.Path, err)
		}
	}
	res.Applied = true
	for _, p := range plans {
		_ = e.record(ctx, Change{Session: o.Session, CallID: "revert/" + e.nextSeq(), Tool: "revert",
			Source: SourceRevert, Path: p.act.Path, PathKey: p.act.PathKey}, p.cur, p.before)
	}
	return res, nil
}

// blocked 冲突是否挡住这次还原。
func blocked(cs []Conflict, force bool) bool {
	for _, c := range cs {
		if !c.Forceable || !force {
			return true
		}
	}
	return false
}

// planFile 一个文件：选目标、检查路径与冲突。ok 为真表示需要写盘。
func (e *Engine) planFile(ctx context.Context, o RevertOptions, cs []Change) (plan, *Conflict, bool) {
	last := cs[len(cs)-1]
	display := last.Path
	var inScope []Change
	later := false
	for _, c := range cs {
		if c.Source == SourceRevert {
			continue
		}
		switch o.Scope {
		case ScopeSession:
			inScope = append(inScope, c)
		case ScopeFromTurn:
			if c.Turn >= o.Turn {
				inScope = append(inScope, c)
			}
		case ScopeTurn:
			if c.Turn == o.Turn {
				inScope = append(inScope, c)
			} else if c.Turn > o.Turn {
				later = true
			}
		}
	}
	if len(inScope) == 0 {
		return plan{}, nil, false
	}
	first := inScope[0]
	conf := func(kind, why string, force bool) (plan, *Conflict, bool) {
		return plan{}, &Conflict{Path: display, Kind: kind, Reason: why, Forceable: force}, false
	}
	// 路径：只认记录里的相对路径，重新解析（越界、链接、篡改都在这里挡住）。
	for _, c := range cs {
		if !SameRoot(c.Root, e.Root) {
			return conf(ConflictOtherRoot, "属于另一个工作区（"+c.Root+"）", false)
		}
	}
	abs, why := e.safePath(last.Path, last.PathKey)
	if why != "" {
		return conf(ConflictUnsafe, why, false)
	}
	if !first.Revertible {
		w := first.Why
		if w == "" {
			w = "没有保存原文"
		}
		return conf(ConflictUnrevertible, "不可还原："+w, false)
	}
	target := fileState{exists: first.Before != "", hash: first.Before, size: first.BeforeSize}
	if target.exists {
		b, err := e.Content(first.Before)
		if err != nil {
			return conf(ConflictLost, "原文已不在工件存储里："+err.Error(), false)
		}
		target.data, target.binary = b, isBinary(b)
	}
	cur, err := readState(abs, e.maxFile())
	if err != nil {
		return conf(ConflictUnsafe, "读不了当前文件："+err.Error(), false)
	}
	if cur.special {
		return conf(ConflictUnsafe, "目标已变成目录或链接", false)
	}
	act := FileAction{Path: display, PathKey: last.PathKey, Delete: !target.exists, Target: target.key(), Prev: cur.key()}
	p := plan{act: act, abs: abs, data: target.data, cur: cur, before: target}
	// 冲突（可强制）：先报最具体的一条。
	var c *Conflict
	switch {
	case later:
		c = &Conflict{Path: display, Kind: ConflictLaterTurn, Forceable: true,
			Reason: "之后的回合也改了这个文件，请改用从该回合起还原"}
	case cur.key() != last.After:
		c = e.whoChanged(ctx, o.Session, display, last.PathKey, cur.key())
	default:
		for _, x := range inScope {
			if x.Source == SourceDrift {
				c = &Conflict{Path: display, Kind: ConflictDrift, Forceable: true,
					Reason: "范围内有工具调用之外的改动（可能是用户手改或格式化器）"}
				break
			}
		}
	}
	if cur.key() == target.key() {
		return plan{}, nil, false // 已经是目标内容：不写，也不算冲突
	}
	return p, c, true
}

// whoChanged 当前内容和本会话最后一条记录不一致：是别的会话改的，还是外部改的。
func (e *Engine) whoChanged(ctx context.Context, session, display, key, cur string) *Conflict {
	cs, _, _ := e.query(ctx, "path_key", key)
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i].Session != session && cs[i].After == cur {
			return &Conflict{Path: display, Kind: ConflictOtherSession, Session: cs[i].Session, Forceable: true,
				Reason: "会话 " + cs[i].Session + " 之后改过这个文件"}
		}
	}
	return &Conflict{Path: display, Kind: ConflictModified, Forceable: true, Reason: "被外部程序或用户改过"}
}

// safePath 记录里的相对路径 → 绝对路径；不安全时返回原因。
// 拒绝：绝对路径、含 ..、越界、经符号链接 / junction 指向外面、path_key 与路径对不上（记录被篡改）。
func (e *Engine) safePath(rel, key string) (string, string) {
	if rel == "" || strings.Contains(rel, "\\") || path.IsAbs(rel) || len(rel) >= 2 && rel[1] == ':' {
		return "", "记录里的路径不合法：" + rel
	}
	if c := path.Clean(rel); c != rel || c == ".." || strings.HasPrefix(c, "../") {
		return "", "记录里的路径不合法：" + rel
	}
	if PathKey(rel) != key {
		return "", "记录里的路径与比对键不符：" + rel
	}
	abs, err := wsfs.Resolve(e.Root, rel)
	if err != nil {
		return "", err.Error()
	}
	if r, err := e.rel(abs); err != nil || PathKey(r) != key {
		return "", "路径解析后不在工作目录内：" + rel
	}
	return abs, ""
}

// apply 写或删一个文件。
func apply(abs string, del bool, data []byte) error {
	if del {
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if data == nil {
		data = []byte{}
	}
	return wsfs.WriteAtomic(abs, data)
}

// rollback 把已写的文件写回还原前的内容（尽力而为）。
func (e *Engine) rollback(done []plan) {
	for i := len(done) - 1; i >= 0; i-- {
		p := done[i]
		if p.cur.exists && p.cur.data == nil {
			continue // 太大没读进内存（只有 Force 才会走到这里）：写不回，不能用空内容覆盖
		}
		_ = apply(p.abs, !p.cur.exists, p.cur.data)
	}
}

// Undo 撤销一次已执行的还原：把每个文件写回 Revert 之前的内容（rewind 截断失败时用，spike 第四节第 5 步）。
// 不检查冲突；写完补 revert 记录。只写回 PrevKept 或原本不存在的文件，其余报错跳过。
func (e *Engine) Undo(ctx context.Context, session string, res RevertResult) error {
	var errs []error
	keys := make([]string, 0, len(res.Files))
	for _, f := range res.Files {
		keys = append(keys, f.PathKey)
	}
	unlock := e.lockPaths(keys...)
	defer unlock()
	for _, f := range res.Files {
		abs, why := e.safePath(f.Path, f.PathKey)
		if why != "" {
			errs = append(errs, errors.New(why))
			continue
		}
		prev := fileState{exists: f.Prev != "", hash: f.Prev}
		if prev.exists {
			if !f.PrevKept {
				errs = append(errs, fmt.Errorf("%s：还原前的内容没有保存", f.Path))
				continue
			}
			b, err := e.Content(f.Prev)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s：%w", f.Path, err))
				continue
			}
			prev.data, prev.size, prev.binary = b, int64(len(b)), isBinary(b)
		}
		cur, _ := readState(abs, e.maxFile())
		if err := apply(abs, !prev.exists, prev.data); err != nil {
			errs = append(errs, fmt.Errorf("%s：%w", f.Path, err))
			continue
		}
		_ = e.record(ctx, Change{Session: session, CallID: "revert/" + e.nextSeq(), Tool: "undo",
			Source: SourceRevert, Path: f.Path, PathKey: f.PathKey}, cur, prev)
	}
	return errors.Join(errs...)
}
