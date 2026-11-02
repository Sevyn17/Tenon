package host

// fork / 旁支（session-core D3，M4.5）：存储层复制完整前缀 [0, upto)，新会话自身完整、不依赖来源存活；
// 条目 id 与调用 id 统一重新分配（data-contract §6），保持全局唯一。分叉点必须是回合起点（或日志末尾），
// 来源会话须为 Idle。origin = fork（分支）/ side（旁支，多轮追问、用完即弃）。

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
)

// Fork 从 src 的 [0, upto) 分出新会话 dst；upto < 0 表示整段日志。返回新会话句柄。
func (h *Host) Fork(ctx context.Context, src, dst string, upto int, origin string) (Handle, error) {
	if origin == "" {
		origin = "fork"
	}
	if origin != "fork" && origin != "side" {
		return Handle{}, fmt.Errorf("host: fork 的 origin 只能是 fork / side，得到 %q", origin)
	}
	if ok, err := h.DB.Exists(ctx, dst); err != nil || ok {
		if err == nil {
			err = fmt.Errorf("host: 会话 %s 已存在", dst)
		}
		return Handle{}, err
	}
	zero := 0
	l, err := h.DB.Load(ctx, src, &zero)
	if err != nil {
		return Handle{}, err
	}
	core, err := session.Restore(l.Snapshot)
	if err != nil {
		return Handle{}, err
	}
	if core.Status() != session.StatusIdle {
		return Handle{}, fmt.Errorf("host: 来源会话 %s 有进行中的回合，不能分叉", src)
	}
	cut := false
	if upto >= 0 && upto < l.Snapshot.Header.LogLen {
		// 分叉点 = 某回合首条 User 的下标。Rewind(upto+1) 恰好校验"upto 处是回合起点"并处理压缩边界，
		// 之后再去掉保留下来的那条 User。
		if _, err := core.Rewind(upto + 1); err != nil {
			return Handle{}, fmt.Errorf("host: 分叉点 %d 不是回合起点: %w", upto, err)
		}
		cut = true
	}
	snap := core.Snapshot()
	src0 := snap.Entries
	hdr := snap.Header
	if cut {
		src0 = src0[:len(src0)-1]
		hdr.LogLen--
	}
	entries, last, err := remap(src0)
	if err != nil {
		return Handle{}, err
	}
	hdr.LastEntryID = last
	hdr.PendingModelRequestID = ""
	m := store.SessionMeta{ID: dst, ParentID: src, Origin: origin, ForkUpto: hdr.LogLen, AgentID: l.Meta.AgentID,
		AgentHash: l.Meta.AgentHash, Creator: l.Meta.Creator, Title: l.Meta.Title,
		Meta: l.Meta.Meta, UseConfig: l.Meta.UseConfig} // 会话键不复制：分叉是新会话，键唯一
	if err := h.DB.CreateSession(ctx, m, hdr, entries); err != nil {
		return Handle{}, err
	}
	if err := h.created(ctx, dst, Origin{Kind: origin, ParentID: src, ForkUpto: hdr.LogLen}); err != nil {
		return Handle{}, err
	}
	hd, err := h.open(ctx, dst, l.Meta.AgentID, h.New, nil)
	if err != nil {
		return Handle{}, err
	}
	h.publish(dst, "session.forked", map[string]any{"from": src, "upto": hdr.LogLen, "origin": origin})
	return hd, nil
}

// remap 深拷贝条目并重新分配条目 id 与调用 id（结果的 call_id 同步映射）。
func remap(in []*session.Entry) ([]*session.Entry, string, error) {
	calls := map[string]string{}
	newID := func() string { u, _ := uuid.NewV7(); return u.String() }
	out := make([]*session.Entry, 0, len(in))
	last := ""
	for _, e := range in {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, "", err
		}
		var c session.Entry
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, "", err
		}
		c.ID = newID()
		last = c.ID
		if a := c.Item.Assistant; a != nil {
			for i := range a.ToolCalls {
				nid := newID()
				calls[a.ToolCalls[i].ID] = nid
				a.ToolCalls[i].ID = nid // wire_id 保留：回传厂商用原值
			}
		}
		if r := c.Item.ToolResult; r != nil {
			if nid, ok := calls[r.CallID]; ok {
				r.CallID = nid
			}
		}
		out = append(out, &c)
	}
	return out, last, nil
}
