package appapi

// 改动审阅与还原（客户端改进 M9，spike-checkpoint 第四、五节）：changes.list / changes.diff / changes.revert，
// 以及 session.rewind {restore_files, force}。能力由 checkpoint 插件经 adapters.Changes 注入（Options.Changes）；
// 没装时这些命令返回 unsupported。
//
// 安全：前端只传会话与路径（路径只用作查找键），内容哈希一律从本会话的记录里取，不接受客户端给的哈希；
// 只还原本会话记录里有的文件。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
)

// 本文件的命令（按文件登记，v2.go 的 registerCommands）。
func init() {
	registerCommands(map[string]handler{
		"changes.list":   bind((*App).ChangesList),
		"changes.diff":   bind((*App).ChangesDiff),
		"changes.revert": bind((*App).ChangesRevert),
	})
}

// Changes 文件改动的查询与还原（checkpoint 插件适配，见 adapters.Changes）。
// live 报告一个工具调用 id 是否还在当前日志里（rewind 截掉的调用返回 false）；nil = 全部有效。
type Changes interface {
	List(ctx context.Context, in wire.ChangesArgs, live func(callID string) bool) (wire.ChangesResult, error)
	Diff(ctx context.Context, in wire.ChangesDiffArgs, live func(callID string) bool) (wire.ChangesDiffResult, error)
	Revert(ctx context.Context, r ChangeRevert) (ChangeReverted, error)
}

// GitChanges 可选：Changes 实现了它且 GitAvailable 为真时报能力 changes.git，changes.list / diff 接受 scope=git
// （工作区相对 HEAD 的未提交改动，只读）。List / Diff 收到 scope=git 时按 git 范围处理。
type GitChanges interface {
	GitAvailable() bool
}

func (v *App) gitScope() bool {
	g, ok := v.o.Changes.(GitChanges)
	return ok && g.GitAvailable()
}

// ChangeRevert 一次还原请求。
type ChangeRevert struct {
	Session string
	Scope   string // wire.ChangeScope*；空 = session
	Turn    uint32
	Paths   []string // 空 = 范围内全部；只用作查找键
	Live    func(callID string) bool
	Force   bool
	DryRun  bool
}

// ChangeReverted 还原结果。Blocked = 有挡住还原的冲突，一个字节都没写。
type ChangeReverted struct {
	Files     []string // 还原了（演练或被挡住时：计划还原）的文件
	Conflicts []wire.ChangeConflict
	Blocked   bool
	// Undo 把这次已执行的还原写回去（rewind 截断失败时用）；没写盘时为 nil。
	Undo func(ctx context.Context) error
}

// rewindDo 执行截断命令（测试替换它来模拟截断失败）。
var rewindDo = func(hd host.Handle, c host.Command) error { return hd.Do(c) }

func (v *App) changes() (Changes, error) {
	if v.o.Changes == nil {
		return nil, Errorf(wire.CodeUnsupported, "未装文件检查点插件（checkpoint）")
	}
	return v.o.Changes, nil
}

// checkScope 校验范围与回合号（git 范围由调用方先处理）。
func checkScope(scope string, turn *uint32) error {
	switch scope {
	case "", wire.ChangeScopeSession:
		return nil
	case wire.ChangeScopeGit:
		return invalid("scope=git 只读，不能还原")
	case wire.ChangeScopeTurn, wire.ChangeScopeFromTurn:
		if turn == nil || *turn == 0 {
			return invalid("scope=%s 须带 turn（≥ 1）", scope)
		}
		return nil
	}
	return invalid("未知范围 %q", scope)
}

func turnOf(p *uint32) uint32 {
	if p == nil {
		return 0
	}
	return *p
}

// liveCalls 当前日志里的工具调用 id 集合（会话不存在返回 not_found）。run_code 子调用 "<父>#<n>" 按父调用判断。
func (v *App) liveCalls(ctx context.Context, id string) (func(string) bool, error) {
	ok, err := v.o.DB.Exists(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s", host.ErrSessionNotFound, id)
	}
	es, err := v.o.DB.Entries(ctx, id, 0, -1)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, e := range es {
		if e == nil || e.Item.Kind != session.KindAssistant || e.Item.Assistant == nil {
			continue
		}
		for _, c := range e.Item.Assistant.ToolCalls {
			set[c.ID] = true
		}
	}
	return func(callID string) bool {
		if i := strings.IndexByte(callID, '#'); i >= 0 {
			callID = callID[:i]
		}
		return set[callID]
	}, nil
}

// needGit git 范围的前置检查：会话存在（not_found），有 git 且工作区在仓库里（unsupported）。
func (v *App) needGit(ctx context.Context, id string) error {
	ok, err := v.o.DB.Exists(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", host.ErrSessionNotFound, id)
	}
	if !v.gitScope() {
		return Errorf(wire.CodeUnsupported, "没有 git 或工作区不在 git 仓库里")
	}
	return nil
}

// filesConflict 文件还原有冲突：conflict（details.reason=files，details.conflicts）。
func filesConflict(cs []wire.ChangeConflict) *Error {
	msg := "有文件不能直接还原"
	if len(cs) > 0 {
		msg = fmt.Sprintf("有 %d 个文件不能直接还原（%s：%s）", len(cs), cs[0].Path, cs[0].Message)
	}
	e := conflict(wire.ConflictFiles, "%s", msg)
	e.Details["conflicts"] = cs
	return e
}

// notInTurn 会话没有进行中的回合（已打开且在回合中返回 not_idle）。
func (v *App) notInTurn(id string) error {
	if hd, ok := v.o.Host.Get(id); ok && hd.InTurn() {
		return fmt.Errorf("%w: 会话 %s 有进行中的回合", session.ErrNotIdle, id)
	}
	return nil
}

// ChangesList 本会话经工具改过的文件（按范围聚合成净改动）。
func (v *App) ChangesList(ctx context.Context, in wire.ChangesArgs) (wire.ChangesResult, error) {
	if err := need(in.Session); err != nil {
		return wire.ChangesResult{}, err
	}
	c, err := v.changes()
	if err != nil {
		return wire.ChangesResult{}, err
	}
	if in.Scope == wire.ChangeScopeGit {
		if err := v.needGit(ctx, in.Session); err != nil {
			return wire.ChangesResult{}, err
		}
		r, err := c.List(ctx, in, nil)
		if r.Files == nil {
			r.Files = []wire.ChangeFile{}
		}
		return r, err
	}
	if err := checkScope(in.Scope, in.Turn); err != nil {
		return wire.ChangesResult{}, err
	}
	live, err := v.liveCalls(ctx, in.Session)
	if err != nil {
		return wire.ChangesResult{}, err
	}
	r, err := c.List(ctx, in, live)
	if err != nil {
		return wire.ChangesResult{}, err
	}
	if r.Files == nil {
		r.Files = []wire.ChangeFile{}
	}
	return r, nil
}

// ChangesDiff 一个文件在所选范围内的 unified diff；内容只从本会话的记录里取。
func (v *App) ChangesDiff(ctx context.Context, in wire.ChangesDiffArgs) (wire.ChangesDiffResult, error) {
	if err := need(in.Session); err != nil {
		return wire.ChangesDiffResult{}, err
	}
	c, err := v.changes()
	if err != nil {
		return wire.ChangesDiffResult{}, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return wire.ChangesDiffResult{}, invalid("缺少 path")
	}
	if in.Scope == wire.ChangeScopeGit {
		if err := v.needGit(ctx, in.Session); err != nil {
			return wire.ChangesDiffResult{}, err
		}
		return c.Diff(ctx, in, nil)
	}
	if err := checkScope(in.Scope, in.Turn); err != nil {
		return wire.ChangesDiffResult{}, err
	}
	live, err := v.liveCalls(ctx, in.Session)
	if err != nil {
		return wire.ChangesDiffResult{}, err
	}
	return c.Diff(ctx, in, live)
}

// ChangesRevert 把本会话改过的文件还原到范围起点。有冲突且没 force：一个字节都不写，返回 conflict
// （details.reason=files、details.conflicts）；dry_run 时冲突放在结果里。会话不能在回合中。
func (v *App) ChangesRevert(ctx context.Context, in wire.ChangesRevertArgs) (wire.ChangesRevertResult, error) {
	if err := need(in.Session); err != nil {
		return wire.ChangesRevertResult{}, err
	}
	c, err := v.changes()
	if err != nil {
		return wire.ChangesRevertResult{}, err
	}
	if err := checkScope(in.Scope, in.Turn); err != nil {
		return wire.ChangesRevertResult{}, err
	}
	live, err := v.liveCalls(ctx, in.Session)
	if err != nil {
		return wire.ChangesRevertResult{}, err
	}
	if err := v.notInTurn(in.Session); err != nil {
		return wire.ChangesRevertResult{}, err
	}
	r, err := c.Revert(ctx, ChangeRevert{Session: in.Session, Scope: in.Scope, Turn: turnOf(in.Turn), Paths: in.Paths,
		Live: live, Force: in.Force, DryRun: in.DryRun})
	if err != nil {
		return wire.ChangesRevertResult{}, err
	}
	if r.Blocked && !in.DryRun {
		return wire.ChangesRevertResult{}, filesConflict(r.Conflicts)
	}
	files := r.Files
	if files == nil {
		files = []string{}
	}
	return wire.ChangesRevertResult{Reverted: files, Conflicts: r.Conflicts}, nil
}

// rewindRestore session.rewind {restore_files}（spike 第四节）：记下游标 → 演练 from_turn 还原 → 有挡路的冲突
// 返回 conflict（日志和文件都不动）→ 还原文件（引擎先存当前内容）→ 带游标截断 → 截断失败时把文件写回。
func (v *App) rewindRestore(ctx context.Context, hd host.Handle, in wire.RewindArgs) (wire.RewindResult, error) {
	c := v.o.Changes
	l, err := host.LoadHead(ctx, v.o.DB, in.Session)
	if err != nil {
		return wire.RewindResult{}, err
	}
	cur := l.Cursor
	if in.IfCursor != nil && *in.IfCursor != cur {
		return wire.RewindResult{}, fmt.Errorf("%w: 期望 %d，当前 %d", host.ErrStale, *in.IfCursor, cur)
	}
	// Live 用截断前的日志：第 to_turn 回合及之后的调用还在日志里，要还原；更早 rewind 截掉的调用（回合号可能被重用）不算。
	live, err := v.liveCalls(ctx, in.Session)
	if err != nil {
		return wire.RewindResult{}, err
	}
	req := ChangeRevert{Session: in.Session, Scope: wire.ChangeScopeFromTurn, Turn: in.ToTurn, Live: live,
		Force: in.Force, DryRun: true}
	plan, err := c.Revert(ctx, req)
	if err != nil {
		return wire.RewindResult{}, err
	}
	if plan.Blocked {
		return wire.RewindResult{}, filesConflict(plan.Conflicts)
	}
	req.DryRun = false
	done, err := c.Revert(ctx, req)
	if err != nil {
		return wire.RewindResult{}, err
	}
	if done.Blocked {
		return wire.RewindResult{}, filesConflict(done.Conflicts)
	}
	var to int
	if err := rewindDo(hd, host.Command{Kind: host.Rewind, ToTurn: in.ToTurn, IfCursor: &cur, RewoundTo: &to}); err != nil {
		if done.Undo != nil {
			if uerr := done.Undo(ctx); uerr != nil {
				return wire.RewindResult{}, errors.Join(err, fmt.Errorf("文件写回失败：%w", uerr))
			}
		}
		return wire.RewindResult{}, err
	}
	return wire.RewindResult{To: to, Files: done.Files}, nil
}
