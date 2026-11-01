package runtime

// 会话级插件点位（thin-kernel-plan §3）。插件在 Setup 里用 SetupCx.Register(点位, 名字, 实现) 登记，
// 应用装配后用 Collect 把登记项填进 Builder。

import (
	"context"
	"fmt"
	"sort"

	"ybase-kernel/core/plugin"
	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

// 点位名与实现类型。
const (
	PointSessionTools = "session_tools" // ToolsEntry
	PointSessionLayer = "session_layer" // LayerEntry
	PointSystemBlock  = "system_block"  // BlockEntry
	PointAfterTurn    = "after_turn"    // AfterTurnFunc
	PointReady        = "ready"         // ReadyFunc：会话构造器就绪后调用一次（如补投上次未送达的结果）
	// PointPermItems toolhub.Item：权限项声明（06 - 工具权限设计方案 §9.3）。
	// 装配级而非会话级：注册表由装进来的插件汇总一次，各会话共用。
	PointPermItems = "perm_items"
)

// 应用预置服务（plugin.Spec.Inject 的键）。
const (
	ServiceHost    = "service:host"    // *host.Host（DB / New / Child 在装配后由应用填好）
	ServiceUI      = "service:ui"      // UI
	ServiceWorkdir = "service:workdir" // string：工作目录绝对路径
	// ServiceTrust bool：项目是否受信（plugin-switch-plan §3.3）。不受信时插件不读项目 .ybase 下的配置；
	// 未预置时按受信处理（测试 / 嵌入方）。
	ServiceTrust = "service:trust"
)

// ProjectTrusted 取 service:trust；插件须在 Requires 里写 "?service:trust"。
func ProjectTrusted(cx *plugin.SetupCx) bool {
	v, ok, err := plugin.UseOptional[bool](cx, ServiceTrust)
	if err != nil {
		return false // 类型不符等异常按不受信处理
	}
	return !ok || v
}

// ToolsEntry 一条会话工具登记，按 Order、再按插件名排列（决定工具在请求里的顺序）。
type ToolsEntry struct {
	Order int
	Func  ToolsFunc
}

// LayerEntry 一条会话层登记。排序：Transform（Guard=false）在外、Guard 在内；同位置按 Order、再按插件名。
type LayerEntry struct {
	Guard bool
	Order int
	Func  LayerFunc
}

// BlockEntry 一条系统块登记，按 Order、再按插件名排列（应用自己的块在最前）。
type BlockEntry struct {
	Order int
	Func  BlockFunc
}

// AfterTurnFunc 回合结束通知；ReadyFunc 构造器就绪。
type (
	AfterTurnFunc func(id string, end session.EndReason)
	ReadyFunc     func(ctx context.Context) error
)

// UI 应用提供给插件的交互面（终端 / 前端各自实现）。
type UI interface {
	Print(text string)                                                          // 普通输出
	Warn(source, msg string)                                                    // 告警（如配置问题）
	Ask(ctx context.Context, question string, options []string) (string, error) // 向用户提问
}

// Collected 装配结果里的会话贡献。
type Collected struct {
	Tools     []ToolsFunc
	Layers    []LayerFunc
	Blocks    []BlockFunc
	AfterTurn []AfterTurnFunc
	Ready     []ReadyFunc
	// Items 权限项声明（装配级），按 id 排序后交给每个会话的 hub 登记。
	Items []toolhub.Item
}

// Collect 按点位取出登记项；工具、层、系统块按上面的规则排序（确定）。类型不符报错。
func Collect(asm *plugin.Assembly) (*Collected, error) {
	c := &Collected{}
	type tools struct {
		e      ToolsEntry
		plugin string
	}
	var tl []tools
	for _, it := range asm.Items(PointSessionTools) {
		e, ok := it.Impl.(ToolsEntry)
		if !ok {
			return nil, fmt.Errorf("runtime: 插件 %s 的 %s 类型不符（%T）", it.Plugin, PointSessionTools, it.Impl)
		}
		tl = append(tl, tools{e, it.Plugin})
	}
	sort.SliceStable(tl, func(i, j int) bool {
		if tl[i].e.Order != tl[j].e.Order {
			return tl[i].e.Order < tl[j].e.Order
		}
		return tl[i].plugin < tl[j].plugin
	})
	for _, t := range tl {
		c.Tools = append(c.Tools, t.e.Func)
	}
	type layer struct {
		e      LayerEntry
		plugin string
	}
	var ls []layer
	for _, it := range asm.Items(PointSessionLayer) {
		e, ok := it.Impl.(LayerEntry)
		if !ok {
			return nil, fmt.Errorf("runtime: 插件 %s 的 %s 类型不符（%T）", it.Plugin, PointSessionLayer, it.Impl)
		}
		ls = append(ls, layer{e, it.Plugin})
	}
	sort.SliceStable(ls, func(i, j int) bool {
		x, y := ls[i], ls[j]
		if x.e.Guard != y.e.Guard {
			return !x.e.Guard
		}
		if x.e.Order != y.e.Order {
			return x.e.Order < y.e.Order
		}
		return x.plugin < y.plugin
	})
	for _, l := range ls {
		c.Layers = append(c.Layers, l.e.Func)
	}
	type block struct {
		e      BlockEntry
		plugin string
	}
	var bs []block
	for _, it := range asm.Items(PointSystemBlock) {
		e, ok := it.Impl.(BlockEntry)
		if !ok {
			return nil, fmt.Errorf("runtime: 插件 %s 的 %s 类型不符（%T）", it.Plugin, PointSystemBlock, it.Impl)
		}
		bs = append(bs, block{e, it.Plugin})
	}
	sort.SliceStable(bs, func(i, j int) bool {
		if bs[i].e.Order != bs[j].e.Order {
			return bs[i].e.Order < bs[j].e.Order
		}
		return bs[i].plugin < bs[j].plugin
	})
	for _, b := range bs {
		c.Blocks = append(c.Blocks, b.e.Func)
	}
	for _, it := range asm.Items(PointAfterTurn) {
		f, ok := it.Impl.(AfterTurnFunc)
		if !ok {
			return nil, fmt.Errorf("runtime: 插件 %s 的 %s 类型不符（%T）", it.Plugin, PointAfterTurn, it.Impl)
		}
		c.AfterTurn = append(c.AfterTurn, f)
	}
	for _, it := range asm.Items(PointReady) {
		f, ok := it.Impl.(ReadyFunc)
		if !ok {
			return nil, fmt.Errorf("runtime: 插件 %s 的 %s 类型不符（%T）", it.Plugin, PointReady, it.Impl)
		}
		c.Ready = append(c.Ready, f)
	}
	for _, it := range asm.Items(PointPermItems) {
		item, ok := it.Impl.(toolhub.Item)
		if !ok {
			return nil, fmt.Errorf("runtime: 插件 %s 的 %s 类型不符（%T）", it.Plugin, PointPermItems, it.Impl)
		}
		if item.Owner == "" {
			item.Owner = it.Plugin
		}
		c.Items = append(c.Items, item)
	}
	sort.SliceStable(c.Items, func(i, j int) bool { return c.Items[i].ID < c.Items[j].ID })
	return c, nil
}
