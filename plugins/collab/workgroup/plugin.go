package workgroup

// 插件登记（thin-kernel-plan §2.2）：工作组。顶层会话是领导（team_spawn + 看板），
// 成员会话（定义 workgroup-member，会话 id 里的 /m- 只是命名约定）只有 team_send + 看板；
// 成员经 service:host 的 Child 构造（可空 = New）。
// 数据库在 store-sqlite 启动后才打开，所以 Group 在 Start 里建；会话构造器就绪后（ready 点位）补投上次未送达的结果。
// 自有 p_workgroup_* 表要 SQL，所以声明 Requires{SQL: true}，装配期就检查存储是否给得出 SQL。

import (
	"context"
	"fmt"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/agentdef"
	"ybase-kernel/core/host"
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
	"ybase-kernel/core/toolhub"
)

// 两个内置定义名（09 9.3、计划/01 T1b-2c）：组长与成员的角色由定义判定，不再按会话 id 猜。
const (
	LeaderDefinition = "workgroup-leader"
	MemberDefinition = "workgroup-member"
)

// templateManifest 组长的内置定义模板。行为不变：不带 tools / use / prompt，
// 工具集合仍由 Visible 按定义挑（09 9.3「行为不变」）。
const leaderManifest = `format = "ybase/v1"
id = "workgroup-leader"
description = "工作组组长：派成员（team_spawn）、给成员发消息、看看板。"
`

// templateManifest 成员的内置定义模板。
const memberManifest = `format = "ybase/v1"
id = "workgroup-member"
description = "工作组成员：给组长或其他成员发消息、看看板。"
`

// TemplateSource 工作组两个内置定义模板的来源（平台层）；Setup 里交给 Registrar.Template。
func TemplateSource() (agentdef.Source, error) {
	m := agentdef.Mem()
	for name, manifest := range map[string]string{LeaderDefinition: leaderManifest, MemberDefinition: memberManifest} {
		if err := m.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: name},
			agentdef.Files{agentdef.ManifestName: []byte(manifest)}); err != nil {
			return nil, err
		}
	}
	return m, nil
}

type plug struct {
	h   *host.Host
	db  kr.DBHandle
	sql store.SQL
	ui  kr.UI
	g   *Group
}

func init() { plugin.Register("workgroup", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "workgroup", Version: "0.1.0", Effects: []string{plugin.EffectTools},
		Requires: []string{kr.ServiceHost, kr.ServiceDB, plugin.ServiceSQL, kr.ServiceUI}}
}

// Requires 自有表要 SQL（框架设计 05 §5.7）。
func (*plug) Requires() plugin.Requires { return plugin.Requires{SQL: true} }

func (p *plug) Setup(cx *plugin.Registrar) error {
	var err error
	if p.h, err = plugin.Use[*host.Host](cx, kr.ServiceHost); err != nil {
		return err
	}
	if p.db, err = plugin.Use[kr.DBHandle](cx, kr.ServiceDB); err != nil {
		return err
	}
	if p.sql, err = plugin.Use[store.SQL](cx, plugin.ServiceSQL); err != nil {
		return err
	}
	if p.ui, err = plugin.Use[kr.UI](cx, kr.ServiceUI); err != nil {
		return err
	}
	for _, it := range Items() {
		cx.Register(kr.PointPermItems, it.ID, it)
	}
	src, err := TemplateSource()
	if err != nil {
		return err
	}
	cx.Template(src)
	// 组长与成员是两批工具，靠 Visible 按会话定义挑（工作组 = 两个内置定义模板）。
	isMember := func(sc *kr.SessionCx) bool { return sc.Def != nil && sc.Def.ID == MemberDefinition }
	cx.Register(kr.PointSessionTools, "workgroup", kr.ToolsEntry{Order: 70,
		Tools: func() []*toolhub.Tool {
			if p.g == nil {
				return nil
			}
			return p.g.LeaderTools()
		},
		Visible: func(sc *kr.SessionCx) bool { return !sc.Child && !isMember(sc) }})
	cx.Register(kr.PointSessionTools, "workgroup-member", kr.ToolsEntry{Order: 70,
		Tools: func() []*toolhub.Tool {
			if p.g == nil {
				return nil
			}
			return p.g.MemberTools()
		},
		Visible: isMember})
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
	member := func(id, def string) (*agent.Agent, *session.Config, error) {
		f := p.h.Child
		if f == nil {
			f = p.h.New
		}
		if f == nil {
			return nil, nil, fmt.Errorf("workgroup: 宿主未配置会话构造")
		}
		return f(id, def)
	}
	g, err := New(context.Background(), p.h, p.sql, p.db.DB(), member)
	if err != nil {
		return err
	}
	p.g = g
	sc.OnClose(func(context.Context) error { g.Close(); return nil }) // 先于宿主关闭：取消后台成员回合
	return nil
}
