package group

// 小组（群聊）模块（框架设计 13 13.4「群聊（小组）」，T4e；执行方案 docs/02 - 开发管理/09）。
//
// 小组是群聊原始记录的存储，加上投递规则：
//   - 群记录存集合 group.messages，序号由小组自排（单调，key 左补零到 20 位保证字典序=数值序）。
//   - 每机器人在每群一个会话：会话键 key = 群 id，创建者是小组组件（component:group）；
//     投递按 FindSessionByKey(定义, "", 群id) 落到同一会话。
//   - @ 才唤醒：一条群记录写完，对被 @ 的机器人投 wake（agent:<定义名> 带 key=群id）；一封都不重投。
//   - 背景按已读位置补齐：每回合调模型前用 Status 注入点，把本机器人已读位置之后的群记录拼成
//     一段背景注入，再推进已读位置（不逐条投）。
//   - 机器人回复写回群记录（AfterTurn 里读本轮最终回复），若 @ 了别的机器人才唤醒对方，hop 兜底。
//   - 转人工开关 [use.group] manual = true：三方发言先入组记录，再由小组决定投给谁。
//
// 群配置从 [use.group] 读（callctx.Ctx.Config），不内置群定义；验收用两个定义文件 + 一段配置拼出。

import (
	"context"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/inject"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// ServiceName 组件的使用配置键（[use.group] 与 WithConfig("group", ...)）。
const ServiceName = "group"

// Config 小组的组件配置（[plugin.group]）。验收用两个定义文件 + 这段配置拼出群，不写 Go。
type Config struct {
	// Manual 转人工开关，缺省关闭（13 13.4「转人工」）。
	Manual bool `cfg:"manual" default:"false"`
	// Groups 群清单：每个群一个群 id 与它的成员机器人（定义名）和可选人工客服地址。
	Groups []GroupConfig `cfg:"groups"`
}

// GroupConfig 一个群的配置。
type GroupConfig struct {
	ID    string   `cfg:"id"`    // 群 id，作会话键
	Bots  []string `cfg:"bots"`  // 成员机器人的定义名；每个机器人在本群一个会话
	Agent []string `cfg:"agent"` // 人工客服地址（human:<id>），转人工时用；可空
}

type plug struct {
	h     *host.Host
	db    store.Store
	ui    assemble.UI
	msgs  store.Collection
	reads store.Collection
	cfg   Config
	g     *Group
}

func init() { component.Register(ServiceName, func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: ServiceName, Version: "0.1.0",
		Requires: []string{assemble.ServiceHost, assemble.ServiceDB, assemble.ServiceUI},
		Config:   &Config{}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	var err error
	if p.h, err = component.Use[*host.Host](cx, assemble.ServiceHost); err != nil {
		return err
	}
	dbh, err := component.Use[assemble.DBHandle](cx, assemble.ServiceDB)
	if err != nil {
		return err
	}
	p.db = dbh.DB()
	if p.ui, err = component.Use[assemble.UI](cx, assemble.ServiceUI); err != nil {
		return err
	}
	// 群记录：Partition 由框架填租户；idx 恒 "all" 供按 key 升序分页扫（取"已读位置之后"）。
	if p.msgs, err = cx.Collection("messages", store.CollectionSpec{
		Version: 1, Indexes: []string{"idx"}, Partition: "tenant"}); err != nil {
		return err
	}
	// 已读位置：key = <群>:<机器人>；另存 __meta__:<群> 记 next_seq（条件写 CAS）。
	if p.reads, err = cx.Collection("reads", store.CollectionSpec{
		Version: 1, Indexes: []string{"group"}, Partition: "tenant"}); err != nil {
		return err
	}
	// 版本 1 没有旧数据要搬：登记从 0 升到 1 的空迁移（框架要求每个集合有迁移）。
	for _, c := range []string{"messages", "reads"} {
		if err := cx.CollectionMigration(component.CollectionMigration{Collection: c, From: 0}); err != nil {
			return err
		}
	}
	// 组件配置 [plugin.group]（群清单、转人工开关）。
	if c, ok := cx.Config().(*Config); ok && c != nil {
		p.cfg = *c
	}
	// 资源类型 group：供 explain 与将来订阅者；@ 扇出投递由小组自己做（见 group.go wakeMentioned）。
	if err := cx.ResourceType(component.ResourceType{Name: "group", Render: renderRecord}); err != nil {
		return err
	}
	// 背景注入：每回合调模型前，把本机器人已读位置之后的群记录补齐。
	if err := cx.Status("group_bg", func(c *callctx.Ctx) []inject.Item {
		if p.g == nil {
			return nil
		}
		return p.g.Background(c)
	}); err != nil {
		return err
	}
	// 机器人回复写回群记录：回合结束后读最终回复，写记录并按 @ 扇出。
	component.RegisterAt(cx, assemble.AfterTurn, ServiceName, assemble.AfterTurnFunc(func(id string, _ session.EndReason) {
		if p.g != nil {
			p.g.AfterTurn(id)
		}
	}))
	return nil
}

func (p *plug) Start(sc *component.Scope) error {
	g, err := New(p.h, p.db, p.msgs, p.reads, p.ui)
	if err != nil {
		return err
	}
	p.g = g
	sc.OnClose(func(ctx context.Context) error { return nil })
	return nil
}
