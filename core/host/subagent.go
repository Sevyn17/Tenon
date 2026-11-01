package host

// 子智能体（M6.5）：子会话是普通会话，会话行记 parent_id + origin=subagent；由父会话的委派工具同步驱动一个回合，
// 回合结束取最后一条助手文本作为结果。子会话持久、可单独打开查看与续聊；确认事件带子会话 id 发到总线，
// 前端按 session 路由 Confirm 命令（确认上交父界面，单一路径不变）。

import (
	"context"
	"errors"
	"fmt"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store/sqlite"
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
// f 为子会话的 agent 工厂（通常不含委派工具：深度 1）。ctx 取消 → 取消子回合。
func (h *Host) Spawn(ctx context.Context, parent, id string, f Factory, task string) (string, error) {
	return h.SpawnAs(ctx, parent, id, "subagent", f, task)
}

// SpawnAs 同 Spawn，子会话 origin 可选（subagent / member）。
func (h *Host) SpawnAs(ctx context.Context, parent, id, origin string, f Factory, task string) (string, error) {
	existed, err := h.DB.Exists(ctx, id)
	if err != nil {
		return "", err
	}
	var hd Handle
	if existed {
		l, err := h.DB.Load(ctx, id, nil)
		if err != nil {
			return "", err
		}
		if l.Meta.ParentID != parent || l.Meta.Origin != origin {
			return "", fmt.Errorf("host: 会话 %s 不是 %s 的 %s 子会话", id, parent, origin)
		}
		hd, err = h.open(ctx, id, f, nil)
		if err != nil {
			return "", err
		}
	} else {
		hd, err = h.open(ctx, id, f, func(ag *agent.Agent, cfg *session.Config) error {
			_, err := ag.OpenAs(ctx, cfg, sqlite.SessionMeta{ParentID: parent, Origin: origin})
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
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: task, Source: "agent"}); err != nil {
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
	return lastAssistant(hd.Agent().Core)
}

// lastAssistant 最后一条助手消息的文本。
func lastAssistant(c *session.Core) (string, error) {
	ms := agent.History(c)
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
