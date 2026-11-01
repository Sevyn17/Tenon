package session

// Restore：从快照恢复内核实例（M4 §3.1 / §5.3 R14 / R15 / S5）。
//
// 校验失败返回 error（宿主转 rt.degraded 只读）；未识别条目不报错、使会话只读（R15）；
// 超出头部 LogLen 的条目视为未提交、截断丢弃（S5）。

import "fmt"

// Restore 校验并恢复会话内核。
func Restore(snap Snapshot) (*Core, error) {
	h := snap.Header
	if h.Version > 1 {
		// R15：主版本变化旧版本只读打开（当前内核主版本 = 1）。
		c := &Core{hdr: h, base: snap.Base, items: snap.Entries, readOnly: true,
			now:   defaultClock(),
			genID: defaultIDs()}
		return c, nil
	}
	// base 规则：必须是 0 或某次压缩的 upto（当前已知压缩点）。
	if snap.Base != 0 && snap.Base != h.CompactionBase {
		return nil, fmt.Errorf("%w: base=%d 既不是 0 也不是压缩边界 %d", ErrState, snap.Base, h.CompactionBase)
	}
	// S5：超出头部 log_len 的条目视为未提交，截断丢弃。
	entries := snap.Entries
	if over := snap.Base + len(entries) - h.LogLen; over > 0 {
		entries = entries[:len(entries)-over]
	}
	if snap.Base+len(entries) < h.LogLen {
		return nil, fmt.Errorf("%w: 条目窗口不完整（LogLen=%d，实得 %d）", ErrState, h.LogLen, snap.Base+len(entries))
	}

	readOnly := false
	// 条目结构校验（R14）。
	var open []string // 未闭合调用队列（队首 = 期望的下一个结果）
	lastUpto, sawCompaction := 0, false
	seenIDs := map[string]bool{}
	for i, e := range entries {
		if e == nil {
			return nil, fmt.Errorf("%w: 条目 %d 为空", ErrState, i)
		}
		if seenIDs[e.ID] {
			return nil, fmt.Errorf("%w: 条目 id 重复 %s", ErrState, e.ID)
		}
		seenIDs[e.ID] = true
		if e.V > 1 {
			readOnly = true
		}
		it := e.Item
		switch it.Kind {
		case KindUser:
			if it.User == nil {
				return nil, fmt.Errorf("%w: user 条目 %d 缺体", ErrState, i)
			}
			if len(open) > 0 {
				return nil, fmt.Errorf("%w: 条目 %d：调用结果未闭合就出现新输入", ErrState, i)
			}
		case KindAssistant:
			if it.Assistant == nil {
				return nil, fmt.Errorf("%w: assistant 条目 %d 缺体", ErrState, i)
			}
			if len(open) > 0 {
				return nil, fmt.Errorf("%w: 条目 %d：调用结果未闭合就出现新回复", ErrState, i)
			}
			wires := map[string]bool{}
			for _, call := range it.Assistant.ToolCalls {
				if call.WireID != "" {
					if wires[call.WireID] {
						return nil, fmt.Errorf("%w: 条目 %d：wire_id 重复 %s", ErrState, i, call.WireID)
					}
					wires[call.WireID] = true
				}
				if call.ID == "" {
					return nil, fmt.Errorf("%w: 条目 %d：调用缺内核 id", ErrState, i)
				}
				open = append(open, call.ID)
			}
		case KindToolResult:
			if it.ToolResult == nil {
				return nil, fmt.Errorf("%w: tool_result 条目 %d 缺体", ErrState, i)
			}
			if len(open) == 0 || open[0] != it.ToolResult.CallID {
				return nil, fmt.Errorf("%w: 条目 %d：结果 %s 不按调用顺序", ErrState, i, it.ToolResult.CallID)
			}
			open = open[1:]
		case KindCompaction:
			if it.Compaction == nil {
				return nil, fmt.Errorf("%w: compaction 条目 %d 缺体", ErrState, i)
			}
			if len(open) > 0 {
				return nil, fmt.Errorf("%w: 条目 %d：压缩条目拆开调用与结果", ErrState, i)
			}
			// 压缩边界在窗口内只允许前进（回退属于 rewind 的宿主职责）。
			if it.Compaction.Upto < lastUpto {
				return nil, fmt.Errorf("%w: 条目 %d：压缩边界回退", ErrState, i)
			}
			lastUpto = it.Compaction.Upto
			sawCompaction = true
		case KindUnknown:
			readOnly = true // R15：未识别条目使会话只读（可查看、可同步）
		default:
			return nil, fmt.Errorf("%w: 条目 %d 未知种类 %q", ErrState, i, it.Kind)
		}
	}
	// 窗口内最后一次压缩必须就是头部记录的压缩点。
	if sawCompaction && lastUpto != h.CompactionBase {
		return nil, fmt.Errorf("%w: 最后一次压缩 upto=%d 与头部 compaction_base=%d 不一致", ErrState, lastUpto, h.CompactionBase)
	}
	// WaitingTools（挂起 / 崩溃恢复的中间态）允许尾部未闭合调用——但必须与
	// PendingCalls 完全一致（T27：恢复后按原 id 续答）；其余状态尾部开调用即损坏。
	if len(open) > 0 {
		if h.Status != StatusWaitingTools || len(open) != len(h.PendingCalls) {
			return nil, fmt.Errorf("%w: 恢复窗口尾部存在未闭合调用 %v", ErrState, open)
		}
		for i, id := range open {
			if h.PendingCalls[i].ID != id {
				return nil, fmt.Errorf("%w: 未闭合调用与 PendingCalls 不一致（%d）", ErrState, i)
			}
		}
	}

	// 状态一致性。
	switch h.Status {
	case StatusWaitingModel:
		if h.Turn == nil || h.PendingModelRequestID == "" {
			return nil, fmt.Errorf("%w: WaitingModel 缺回合或请求 id", ErrState)
		}
	case StatusWaitingTools:
		if h.Turn == nil || len(h.PendingCalls) == 0 {
			return nil, fmt.Errorf("%w: WaitingTools 缺回合或待回调用", ErrState)
		}
	case StatusIdle:
		if h.PendingModelRequestID != "" {
			return nil, fmt.Errorf("%w: Idle 不应残留请求 id", ErrState)
		}
	default:
		return nil, fmt.Errorf("%w: 未知内核状态", ErrState)
	}

	return &Core{hdr: h, base: snap.Base, items: entries, readOnly: readOnly,
		now:   defaultClock(),
		genID: defaultIDs()}, nil
}

// Resumed 返回恢复后待满足的需求（03 §8.3 Restored.need）：
// Idle → nil；WaitingModel → 新 need_id、沿用头部的 request_id（S7）；
// WaitingTools → 原调用 id 的全部未回调用（S6），校验提示按当前配置重算。
// 只在 Restore 之后调用一次；只读会话返回 nil。
func (c *Core) Resumed() *Need {
	if c.readOnly {
		return nil
	}
	switch c.hdr.Status {
	case StatusWaitingModel:
		return c.emitNeed(&Need{Kind: NeedModel, Model: &ModelNeed{
			RequestID: c.hdr.PendingModelRequestID, Turn: c.hdr.Turn.Turn, Step: c.hdr.Turn.Step}})
	case StatusWaitingTools:
		hints, _ := c.validateCalls(c.hdr.PendingCalls)
		return c.emitNeed(&Need{Kind: NeedTools, Tools: &ToolsNeed{
			Turn: c.hdr.Turn.Turn, Calls: append([]ToolCall(nil), c.hdr.PendingCalls...), Hints: hints}})
	default:
		return nil
	}
}
