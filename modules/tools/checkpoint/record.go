package checkpoint

// 记录：ToolCall 中间件（write_path 精确快照）、drift 补记、回合末对账。
// may_write_files（shell / MCP / 扩展进程）的扫描与 git 基线是 T9，这一段不做。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Sevyn17/tenon/modules/internal/wsfs"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// TurnOf 从会话历史推导本次调用所属回合：before_tool 检查点先提交再执行工具，
// 所以已提交历史的最后一条就是本回合的助手条目。没有作用域或历史为空返回 0。
func TurnOf(sc component.SessionScope) int {
	if sc == nil {
		return 0
	}
	_, win := sc.History()
	for i := len(win) - 1; i >= 0; i-- {
		if win[i] != nil {
			return int(win[i].Turn)
		}
	}
	return 0
}

// rel 绝对路径相对工作目录（正斜杠）；越界返回错误。
func (e *Engine) rel(abs string) (string, error) {
	r, err := filepath.Rel(e.Root, abs)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", fmt.Errorf("路径不在工作目录内: %s", abs)
	}
	return filepath.ToSlash(r), nil
}

// writePathOf 按 AttrWritePath 从参数里取路径。
func writePathOf(attrs map[string]any, args json.RawMessage) (string, bool) {
	field, _ := attrs[toolhub.AttrWritePath].(string)
	if field == "" {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return "", false
	}
	p, _ := m[field].(string)
	return p, p != ""
}

// Middleware ToolCall 中间件：有 write_path 的调用在前后各读一次目标文件，变了就记一条。
// 必须包在 hooks 外面（plugin.go 用 ?service:hooks 保证），格式化器的改动才算进同一次调用。
func (e *Engine) Middleware() component.ToolCallMw {
	return func(ctx context.Context, req component.ToolCallReq,
		next func(context.Context, component.ToolCallReq) (component.ToolCallResp, error)) (component.ToolCallResp, error) {
		if req.Risk == toolhub.Read {
			return next(ctx, req)
		}
		p, ok := writePathOf(req.Attrs, req.Args)
		if !ok {
			return next(ctx, req)
		}
		abs, err := wsfs.Resolve(e.Root, p)
		if err != nil {
			return next(ctx, req) // 越界由工具自己拒绝；这里不记
		}
		rel, err := e.rel(abs)
		if err != nil {
			return next(ctx, req)
		}
		key := PathKey(rel)
		unlock := e.lockPaths(key)
		defer unlock()
		before, berr := readState(abs, e.maxFile())
		resp, err := next(ctx, req)
		if berr != nil || before.special {
			return resp, err
		}
		if resp.Output.Kind == toolhub.Confirm {
			return resp, err // 批准后整条链重跑，那时再拍
		}
		after, aerr := readState(abs, e.maxFile())
		if aerr != nil || after.special || after.key() == before.key() {
			return resp, err
		}
		turn := TurnOf(req.Scope)
		// 记录失败不影响工具结果。
		_ = e.recordDrift(ctx, req.SessionID, turn, rel, key, before)
		_ = e.record(ctx, Change{Session: req.SessionID, Turn: turn, CallID: req.CallID, Tool: req.Tool,
			Source: SourceFS, Path: rel, PathKey: key}, before, after)
		e.touch(req.SessionID, key, rel)
		return resp, err
	}
}

// record 补全内容与可还原性后写一条记录。
func (e *Engine) record(ctx context.Context, c Change, before, after fileState) error {
	bk, bwhy := e.keep(ctx, c.Session, before)
	ak, awhy := e.keep(ctx, c.Session, after)
	c.Root = filepath.ToSlash(e.Root)
	c.Before, c.After = before.key(), after.key()
	c.BeforeKept, c.AfterKept = bk, ak
	c.BeforeSize, c.AfterSize = before.size, after.size
	c.Binary = before.binary || after.binary
	// 可还原 = before 能写回（不存在也算：还原就是删除）。
	c.Revertible = !before.exists || bk
	if !c.Revertible {
		c.Why = bwhy
	} else if after.exists && !ak {
		c.Why = awhy // 还原不受影响，只是 diff 看不到改后内容
	}
	c.At = e.now().UnixMilli()
	c.Seq = e.nextSeq()
	if c.Key == "" {
		c.Key = fmt.Sprintf("%s/%s/%s", c.Session, c.CallID, c.Seq)
	}
	return e.put(ctx, c)
}

// recordDrift 当前内容和本会话该文件上一条记录的 after 不一致：先补一条 drift
// （钩子格式化、用户在编辑器里改过、别的程序写过）。本会话没记过这个文件则不补。
func (e *Engine) recordDrift(ctx context.Context, session string, turn int, rel, key string, cur fileState) error {
	last, err := e.lastFor(ctx, session, key)
	if err != nil || last == nil || last.After == cur.key() {
		return err
	}
	prev := fileState{exists: last.After != "", hash: last.After, size: last.AfterSize}
	if last.AfterKept {
		if b, err := e.Content(last.After); err == nil {
			prev.data = b
			prev.binary = isBinary(b)
		}
	}
	return e.record(ctx, Change{Session: session, Turn: turn, CallID: fmt.Sprintf("turn-%d/drift", turn),
		Source: SourceDrift, Path: rel, PathKey: key}, prev, cur)
}

func (e *Engine) touch(session, key, rel string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.touched == nil {
		e.touched = map[string]map[string]string{}
	}
	if e.touched[session] == nil {
		e.touched[session] = map[string]string{}
	}
	e.touched[session][key] = rel
}

// AfterTurn 回合末对账（assemble.AfterTurn 点位）：对本回合碰过的文件重读一次，
// 和最后一条记录不一致就补 drift（如后台钩子在调用返回后才改了文件）。
func (e *Engine) AfterTurn(id string, _ session.EndReason) {
	e.Reconcile(context.Background(), id, 0)
}

// Reconcile 见 AfterTurn。turn 为 drift 记录的回合号（0 = 用该文件最后一条记录的回合）。
func (e *Engine) Reconcile(ctx context.Context, session string, turn int) {
	e.mu.Lock()
	files := e.touched[session]
	delete(e.touched, session)
	e.mu.Unlock()
	for key, rel := range files {
		abs, err := wsfs.Resolve(e.Root, rel)
		if err != nil {
			continue
		}
		unlock := e.lockPaths(key)
		cur, err := readState(abs, e.maxFile())
		if err == nil && !cur.special {
			t := turn
			if t == 0 {
				if last, _ := e.lastFor(ctx, session, key); last != nil {
					t = last.Turn
				}
			}
			_ = e.recordDrift(ctx, session, t, rel, key, cur)
		}
		unlock()
	}
}
