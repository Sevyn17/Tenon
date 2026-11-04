package agent

// 持久化（M1.7–1.9）：每次停顿一个 SQLite 事务（store.Store.Commit）。
//
// 时机（session-host H11 / H17）：
//   - 模型调用前：新条目 + 头部（含 pending_model_request_id）+ 请求记录，同一事务；
//   - 执行工具前：新条目 + 头部（S5）；
//   - 回合结束：新条目 + 头部，run_state = idle。
//
// 恢复（03 §8.3）：WaitingModel 读请求记录重组并校验 final_hash，命中则原样重发；
// WaitingTools 按原调用 id 查幂等记录：done 直接取回，started 回"状态未知"，无记录才执行。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"ybase/runtime/reqlog"
	"ybase/runtime/session"
	"ybase/runtime/store"
	"ybase/runtime/useconf"
)

// Open 打开会话：存在则恢复（返回 true），不存在则用 cfg 新建。
// 恢复出的待满足需求留给 Recover 继续驱动。
func (a *Agent) Open(ctx context.Context, cfg *session.Config) (bool, error) {
	return a.OpenAs(ctx, cfg, store.SessionMeta{ID: a.SessionID})
}

// OpenAs 同 Open；新建时用给定的会话行字段（子智能体：ParentID + Origin=subagent）。
func (a *Agent) OpenAs(ctx context.Context, cfg *session.Config, meta store.SessionMeta) (bool, error) {
	if a.Store == nil || a.SessionID == "" {
		return false, errors.New("agent: 未配置存储或会话 id")
	}
	l, err := a.Store.Load(ctx, a.SessionID, nil)
	if errors.Is(err, store.ErrNotFound) {
		a.Core = session.New(cfg)
		meta.ID = a.SessionID
		a.Origin = meta.Origin
		if err := a.fillUseConfig(ctx, &meta); err != nil {
			return false, err
		}
		if err := a.Store.CreateSession(ctx, meta, a.Core.Snapshot().Header, nil); err != nil {
			return false, err
		}
		a.committed = 0
		a.bindCtx(meta)
		a.publishSnapshot(a.Core.Snapshot())
		return false, nil
	}
	if err != nil {
		return false, err
	}
	c, err := session.Restore(l.Snapshot)
	if err != nil {
		return false, err
	}
	a.Core = c
	a.Origin = l.Meta.Origin
	a.bindCtx(l.Meta)
	a.committed = l.Snapshot.Header.LogLen
	a.publishSnapshot(c.Snapshot())
	a.resume = c.Resumed()
	if a.resume != nil && a.resume.Kind == session.NeedModel {
		a.recovered = a.recoverRequest(ctx, a.resume.Model.RequestID)
	}
	return true, nil
}

// fillUseConfig 新会话落库前按使用配置链算好 use_config（06 6.3、D8）。
// 调用方已经给了 use_config 就尊重它（子会话继承父会话配置的路径）；没有定义就什么都不做。
func (a *Agent) fillUseConfig(ctx context.Context, meta *store.SessionMeta) error {
	if len(meta.UseConfig) > 0 || a.Def == nil || len(a.Def.Use) == 0 {
		return nil
	}
	values, err := useconf.ResolveAll(ctx, a.Schemas, a.Def.Use, a.UseSource, store.Tenant, meta.Creator)
	if err != nil {
		return err
	}
	raw, err := useconf.EncodeAll(values)
	if err != nil {
		return err
	}
	meta.UseConfig = raw
	return nil
}

// Pending 报告是否有恢复出的未完成回合。
func (a *Agent) Pending() bool { return a.resume != nil }

// Recover 继续上次中断的回合；没有则立即返回 Done。
func (a *Agent) Recover(ctx context.Context) (session.EndReason, error) {
	if a.resume == nil {
		return session.DoneReason(), nil
	}
	need := a.resume
	a.resume = nil
	return a.drive(ctx, need)
}

// recoverRequest 按请求记录重组上次发出的请求；记录缺失或哈希不符返回 nil（按当前日志重新构造）。
func (a *Agent) recoverRequest(ctx context.Context, requestID string) *session.Request {
	sr, err := a.Store.GetRequest(ctx, a.SessionID, requestID)
	if err != nil || sr == nil {
		return nil
	}
	r, err := a.fromStore(ctx, sr)
	if err != nil {
		return nil
	}
	req, err := reqlog.Rebuild(r, a.Core.Context())
	if err != nil {
		return nil
	}
	a.recoveredView = r.View // 重发时请求记录保持同一压缩视图
	return &req
}

// checkpoint 把内核自上次提交以来的变化写入一个事务。
func (a *Agent) checkpoint(ctx context.Context, run string, rec *reqlog.Record) error {
	if a.Store == nil {
		return nil
	}
	// 持久化不随回合取消中止：取消后的收尾（cancel 条目、回合结束头部）也必须落库。
	ctx = context.WithoutCancel(ctx)
	s := a.Core.Snapshot()
	cp := store.Checkpoint{SessionID: a.SessionID, Epoch: a.Epoch, ExpectLogLen: a.committed, Header: s.Header, RunState: run}
	from := a.committed
	if s.Header.LogLen < a.committed { // rewind 后立即 checkpoint，截断点即新 log_len
		t := s.Header.LogLen
		cp.TruncateFrom = &t
		from = t
	}
	if from < s.Base {
		from = s.Base
	}
	cp.AppendFrom = from
	if off := from - s.Base; off < len(s.Entries) {
		cp.Append = s.Entries[off:]
	}
	if rec != nil {
		sr, err := toStore(rec)
		if err != nil {
			return err
		}
		cp.Request = sr
	}
	cp.ConsumeInbox = a.consume
	if len(a.outbox) > 0 {
		cp.Outbox = append([]store.OutboxItem(nil), a.outbox...)
	}
	a.spanMu.Lock()
	cp.Spans = append(append([]store.Span(nil), a.pendingSpans...), a.hubSpans...)
	hubN := len(a.hubSpans)
	a.spanMu.Unlock()
	if err := a.Store.Commit(ctx, cp); err != nil {
		return err
	}
	a.pendingSpans = nil
	a.outbox = nil
	a.consume = nil
	a.spanMu.Lock()
	a.hubSpans = a.hubSpans[hubN:]
	a.spanMu.Unlock()
	a.committed = s.Header.LogLen
	a.publishSnapshot(s)
	return nil
}

// publishSnapshot 记下最近一次提交（或打开时恢复）的会话快照，供 actor 之外的读者用（Committed）。
// 条目切片复制一份：Core 截断（Rewind）后再追加会改写原底层数组里的位置。条目本身只读、共享。
func (a *Agent) publishSnapshot(s session.Snapshot) {
	// 不复制条目切片：内核只往末尾追加（读者看不到超出自己长度的部分），Rewind 截掉容量后再追加
	// 会换新底层数组，已发出去的快照不会被改写（2026-11-04 审查 P1-40：原先每次提交复制整个切片，
	// 整条会话累计 O(n²)）。
	a.snapMu.Lock()
	a.snap = &s
	a.snapMu.Unlock()
}

// Committed 最近一次提交的会话快照（只读）。actor 之外读会话状态一律用它，不要直接读 Core——
// Core 只由会话自己的 actor 改，从别的 goroutine 读是数据竞争。会话还没打开时返回零值。
func (a *Agent) Committed() session.Snapshot {
	a.snapMu.RLock()
	defer a.snapMu.RUnlock()
	if a.snap == nil {
		return session.Snapshot{}
	}
	return *a.snap
}

func toStore(r *reqlog.Record) (*store.RequestRecord, error) {
	body, err := json.Marshal(r.Shell)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	sr := &store.RequestRecord{RequestID: r.RequestID, ShellHash: hex.EncodeToString(sum[:]), ShellBody: body,
		PrefixBase: r.PrefixBase, PrefixEnd: r.PrefixEnd, FinalHash: r.FinalHash}
	if r.View != nil {
		if sr.CompactionView, err = json.Marshal(r.View); err != nil {
			return nil, err
		}
	}
	if sr.Composition, err = json.Marshal(r.Composition); err != nil {
		return nil, err
	}
	return sr, nil
}

func (a *Agent) fromStore(ctx context.Context, sr *store.RequestRecord) (*reqlog.Record, error) {
	body, err := a.Store.ShellBody(ctx, sr.ShellHash)
	if err != nil {
		return nil, err
	}
	r := &reqlog.Record{RequestID: sr.RequestID, PrefixBase: sr.PrefixBase, PrefixEnd: sr.PrefixEnd, FinalHash: sr.FinalHash}
	if err := json.Unmarshal(body, &r.Shell); err != nil {
		return nil, err
	}
	if len(sr.CompactionView) > 0 {
		r.View = &reqlog.CompactionView{}
		if err := json.Unmarshal(sr.CompactionView, r.View); err != nil {
			return nil, err
		}
	}
	return r, nil
}
