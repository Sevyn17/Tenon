package workgroup

// 插件登记（thin-kernel-plan §2.2）：工作组。顶层会话是领导（team_spawn + 看板），
// 成员会话（定义 workgroup-member，会话 id 里的 /m- 只是命名约定）只有 team_send + 看板；
// 成员经 service:host 的 Child 构造（可空 = New）。
// 数据库在 store-sqlite 启动后才打开，所以 Group 在 Start 里建；会话构造器就绪后（ready 点位）补投上次未送达的结果。
// 任务板与投递账都是集合，不依赖 SQL；service:sql 只在把 v0.2.x 的旧表搬进集合时用，可选。

import (
	"context"
	"fmt"

	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 两个内置定义名（09 9.3、计划/01 T1b-2c）：组长与成员的角色由定义判定，不再按会话 id 猜。
const (
	LeaderDefinition = "workgroup-leader"
	MemberDefinition = "workgroup-member"
)

// templateManifest 组长的内置定义模板。行为不变：不带 tools / use / prompt，
// 工具集合仍由 Visible 按定义挑（09 9.3「行为不变」）。
const leaderManifest = `format = "tenon/v1"
id = "workgroup-leader"
description = "工作组组长：派成员（team_spawn）、给成员发消息、看看板。"
`

// templateManifest 成员的内置定义模板。
const memberManifest = `format = "tenon/v1"
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
	h     *host.Host
	db    assemble.DBHandle
	sql   store.SQL        // 可选：只用于把 v0.2.x 的旧表搬进集合
	tasks store.Collection // 任务板（集合 workgroup.tasks，Assembly.Start 前由框架打开）
	runs  store.Collection // 投递账（集合 workgroup.runs）
	ui    assemble.UI
	g     *Group
}

func init() { component.Register("workgroup", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "workgroup", Version: "0.1.0", Effects: []string{component.EffectTools},
		Requires: []string{assemble.ServiceHost, assemble.ServiceDB, "?" + component.ServiceSQL, assemble.ServiceUI}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	var err error
	if p.h, err = component.Use[*host.Host](cx, assemble.ServiceHost); err != nil {
		return err
	}
	if p.db, err = component.Use[assemble.DBHandle](cx, assemble.ServiceDB); err != nil {
		return err
	}
	// 可选：没有 SQL 出口的存储（如内存实现）装不上 service:sql，旧表搬家就跳过。
	if sq, ok, err := component.UseOptional[store.SQL](cx, component.ServiceSQL); err != nil {
		return err
	} else if ok {
		p.sql = sq
	}
	if p.ui, err = component.Use[assemble.UI](cx, assemble.ServiceUI); err != nil {
		return err
	}
	// 任务板（T4a）。句柄在 Assembly.Start 之前打开，Start 里的 New 才能用。
	if p.tasks, err = cx.Collection("tasks", store.CollectionSpec{
		Version: 1, Indexes: []string{"status", "owner"}, Partition: "team"}); err != nil {
		return err
	}
	// 版本 1 没有集合内的数据要改：旧表 p_workgroup_tasks 的搬运在 Start 里，迁移函数拿不到 SQL。
	if err := cx.CollectionMigration(component.CollectionMigration{Collection: "tasks", From: 0}); err != nil {
		return err
	}
	// 投递账（原 p_workgroup_runs）。同样：集合内没有数据要改，旧表的搬运在 Start 里。
	if p.runs, err = cx.Collection("runs", runsSpec); err != nil {
		return err
	}
	if err := cx.CollectionMigration(component.CollectionMigration{Collection: "runs", From: 0}); err != nil {
		return err
	}
	for _, it := range Items() {
		component.RegisterAt(cx, assemble.PermItems, it.ID, it)
	}
	src, err := TemplateSource()
	if err != nil {
		return err
	}
	cx.Template(src)
	// 组长与成员是两批工具，靠 Visible 按会话定义挑（工作组 = 两个内置定义模板）。
	isMember := func(sc *assemble.SessionCx) bool { return sc.Def != nil && sc.Def.ID == MemberDefinition }
	component.RegisterAt(cx, assemble.SessionTools, "workgroup", assemble.ToolsEntry{Order: 70,
		Tools: func() []*toolhub.Tool {
			if p.g == nil {
				return nil
			}
			return p.g.LeaderTools()
		},
		Visible: func(sc *assemble.SessionCx) bool { return !sc.Child && !isMember(sc) }})
	component.RegisterAt(cx, assemble.SessionTools, "workgroup-member", assemble.ToolsEntry{Order: 70,
		Tools: func() []*toolhub.Tool {
			if p.g == nil {
				return nil
			}
			return p.g.MemberTools()
		},
		Visible: isMember})
	component.RegisterAt(cx, assemble.Ready, "workgroup", assemble.ReadyFunc(func(ctx context.Context) error {
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

func (p *plug) Start(sc *component.Scope) error {
	g, err := New(context.Background(), p.h, p.sql, p.tasks, p.runs, p.db.DB(), p.h.ChildFactory())
	if err != nil {
		return err
	}
	p.g = g
	sc.OnClose(func(context.Context) error { g.Close(); return nil }) // 先于宿主关闭：取消后台成员回合
	return nil
}
