package workgroup

// 插件登记（thin-kernel-plan §2.2）：工作组。顶层会话是领导（team_spawn + 看板），
// 成员会话（id 含 /m-）只有 team_send + 看板；成员经 service:host 的 Child 构造（可空 = New）。
// 数据库在 store-sqlite 启动后才打开，所以 Group 在 Start 里建；会话构造器就绪后（ready 点位）补投上次未送达的结果。

import (
	"context"
	"fmt"
	"strings"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/host"
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store/sqlite"
	"ybase-kernel/core/toolhub"
)

type plug struct {
	h     *host.Host
	store kr.DBHandle
	ui    kr.UI
	g     *Group
}

func init() { plugin.Register("workgroup", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "workgroup", Version: "0.1.0", Effects: []string{plugin.EffectTools},
		Requires: []string{kr.ServiceHost, kr.ServiceDB, kr.ServiceUI}}
}

func (p *plug) Setup(cx *plugin.SetupCx) error {
	var err error
	if p.h, err = plugin.Use[*host.Host](cx, kr.ServiceHost); err != nil {
		return err
	}
	if p.store, err = plugin.Use[kr.DBHandle](cx, kr.ServiceDB); err != nil {
		return err
	}
	if p.ui, err = plugin.Use[kr.UI](cx, kr.ServiceUI); err != nil {
		return err
	}
	for _, it := range Items() {
		cx.Register(kr.PointPermItems, it.ID, it)
	}
	cx.Register(kr.PointSessionTools, "workgroup", kr.ToolsEntry{Order: 70,
		Func: func(sc *kr.SessionCx) ([]*toolhub.Tool, error) {
			switch {
			case p.g == nil:
				return nil, nil
			case strings.Contains(sc.ID, "/m-"):
				return p.g.MemberTools(), nil
			case !sc.Child:
				return p.g.LeaderTools(), nil
			}
			return nil, nil
		}})
	cx.Register(kr.PointReady, "workgroup", kr.ReadyFunc(func(ctx context.Context) error {
		if p.g == nil {
			return nil
		}
		n, err := p.g.Recover(ctx) // 投递会打开领导会话，所以等构造器就绪
		if err != nil {
			return fmt.Errorf("工作组补投失败：%w", err)
		}
		if n > 0 {
			p.ui.Print(fmt.Sprintf("[工作组] 补投 %d 条上次未送达的成员结果\n", n))
		}
		return nil
	}))
	return nil
}

func (p *plug) Start(sc *plugin.Scope) error {
	member := func(id string) (*agent.Agent, *session.Config, error) {
		f := p.h.Child
		if f == nil {
			f = p.h.New
		}
		if f == nil {
			return nil, nil, fmt.Errorf("workgroup: 宿主未配置会话构造")
		}
		return f(id)
	}
	db, ok := p.store.DB().(*sqlite.DB) // 自有 p_workgroup_ 表要 SQL（T1b-1b 改声明 Requires{SQL: true}）
	if !ok {
		return fmt.Errorf("workgroup: 需要 SQLite 存储")
	}
	g, err := New(context.Background(), p.h, db, member)
	if err != nil {
		return err
	}
	p.g = g
	sc.OnClose(func(context.Context) error { g.Close(); return nil }) // 先于宿主关闭：取消后台成员回合
	return nil
}
