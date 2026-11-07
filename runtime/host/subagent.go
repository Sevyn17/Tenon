package host

// 子智能体（M6.5）：子会话是普通会话，会话行记 parent_id + origin=subagent；由父会话的委派工具同步驱动一个回合，
// 回合结束取最后一条助手文本作为结果。子会话持久、可单独打开查看与续聊；确认事件带子会话 id 发到总线，
// 前端按 session 路由 Confirm 命令（确认上交父界面，单一路径不变）。

import (
	"context"
	"errors"
	"fmt"

	"tenon/runtime/agent"
	"tenon/runtime/session"
	"tenon/runtime/store"
)

// Get 返回已打开会话的句柄。
func (h *Host) Get(id string) (Handle, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.actors[id]
	if !ok {
		return Handle{}, false
	}
	return Handle{a}, true
}

// Spawn 新建（或续用）子会话 id，投递 task，等回合结束，返回最后一条助手文本。
// f 为子会话的 agent 工厂（通常不含委派工具：深度 1）；def 是子会话内置定义名（T1b-2c）。
// ctx 取消 → 取消子回合。
// 子回合的可信度跟随父会话当前回合（Spawn 在父回合内同步调用，读到的就是派出时的值）。
func (h *Host) Spawn(ctx context.Context, parent, id, def string, f Factory, task string) (string, error) {
	out, _, err := h.SpawnAs(ctx, parent, id, "subagent", def, f, task, h.Untrusted(parent))
	return out, err
}

// Untrusted 已打开会话的当前回合是否含不可信输入（未打开返回 false）。
func (h *Host) Untrusted(id string) bool {
	hd, ok := h.Get(id)
	return ok && hd.Agent().Ctx.Untrusted()
}

// SpawnAs 同 Spawn，子会话 origin 可选（subagent / member）。untrusted 为派出时父回合的可信度：
// 不可信回合派出的任务按不可信投给子会话，只降不升（2026-11-04 审查 P1-3：原先一律可信，
// 网页内容可借子会话"洗白"成可信回合）。返回子回合结束时是否含不可信输入，供调用方回投结果时沿用。
func (h *Host) SpawnAs(ctx context.Context, parent, id, origin, def string, f Factory, task string, untrusted bool) (string, bool, error) {
	out, err := h.spawn(ctx, parent, id, origin, def, f, task, untrusted)
	return out, untrusted || h.Untrusted(id), err
}

func (h *Host) spawn(ctx context.Context, parent, id, origin, def string, f Factory, task string, untrusted bool) (string, error) {
	existed, err := h.DB.Exists(ctx, id)
	if err != nil {
		return "", err
	}
	var hd Handle
	if existed {
		l, err := h.head(ctx, id)
		if err != nil {
			return "", err
		}
		if l.Meta.ParentID != parent || l.Meta.Origin != origin {
			return "", fmt.Errorf("host: 会话 %s 不是 %s 的 %s 子会话", id, parent, origin)
		}
		if def != "" && l.Meta.AgentID != "" && l.Meta.AgentID != def {
			return "", fmt.Errorf("host: 会话 %s 的定义是 %s，不是 %s", id, l.Meta.AgentID, def)
		}
		hd, err = h.open(ctx, id, def, f, nil)
		if err != nil {
			return "", err
		}
	} else {
		hd, err = h.open(ctx, id, def, f, func(ag *agent.Agent, cfg *session.Config) error {
			meta := store.SessionMeta{ParentID: parent, Origin: origin}
			if ag.Def != nil {
				meta.AgentID, meta.AgentHash = ag.Def.ID, ag.Def.Version
			}
			_, err := ag.OpenAs(ctx, cfg, meta)
			return err
		})
		if err != nil {
			return "", err
		}
		if err := h.created(ctx, id, Origin{Kind: origin, ParentID: parent}); err != nil {
			hd.Do(Command{Kind: Close})
			return "", err
		}
	}
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: task, Source: "agent", Trust: TrustOf(untrusted)}); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- hd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = hd.Do(Command{Kind: Cancel})
		<-done
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return lastAssistant(hd.Agent().CommittedHistory())
}

// lastAssistant 最后一条助手消息的文本（读提交快照：子会话 actor 可能已开下一回合）。
func lastAssistant(ms []session.Message) (string, error) {
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Role != "assistant" {
			continue
		}
		s := ""
		for _, p := range ms[i].Content {
			s += p.Text
		}
		if s != "" {
			return s, nil
		}
	}
	return "", errors.New("子智能体没有给出文本结果")
}

// TrustOf 可信度标记（Command.Trust）：true → "untrusted"，false → ""（缺省可信）。
func TrustOf(untrusted bool) string {
	if untrusted {
		return "untrusted"
	}
	return ""
}

// ChildFactory 按 Child（可空 = New）构造子会话的工厂；调用时才取值，装配后再填 New / Child 也可。
// subagent 与 workgroup 共用（2026-11-04 审查 P2：原先两个组件各写一份）。
func (h *Host) ChildFactory() Factory {
	return func(id, def string) (*agent.Agent, *session.Config, error) {
		f := h.Child
		if f == nil {
			f = h.New
		}
		if f == nil {
			return nil, nil, errors.New("host: 未配置会话构造")
		}
		return f(id, def)
	}
}
