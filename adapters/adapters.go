// Package adapters 把装配结果接成应用协议（appapi）的可选能力。本包只做与具体插件无关的通用部分：
// 模型列表与切换、建议追问、自动标题、手动压缩、工件、删除会话时的清理、扩展取装配服务。
//
// 依赖具体标准模块的适配（权限档位、技能、MCP、文件检查点、工作区文件列表）在 tenon/std 里，
// 由 std 的 init 经 [Register] 登记（Mortise docs/62 K6）。只 import 根包、不导入 std 的应用拿到的就是
// 「只有会话」的协议面，能力表里不报 modes、skills、mcp、changes。
//
// appapi 属于框架核心，不能 import 插件包，所以这一层放在核心之外。SDK（tenon.App）与应用宿主（apphost）
// 都用它：用 SDK 自己装配的应用，Start 之后 App.API() 拿到的协议层已经接好这些能力，可以直接交给
// transport/web 或 transport/bind 提供给前端。
package adapters

import (
	"fmt"
	"sync"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Parts 装配结果里接协议要用的部分。
type Parts struct {
	Asm    *component.Assembly
	Models *assemble.Models  // 可空：没有就不提供模型列表、追问、标题
	Blobs  toolhub.Artifacts // 可空：工件存储
	// Workdir 工作目录（登记的适配用，如 std 的 workspace.files）；空 = 不提供文件列表。
	Workdir string
	// Approvals 项目档位放宽权限的确认记录；nil = 不记，每次都要确认。
	Approvals ModeApprovals
}

// ModeApprovals 项目档位放宽权限的确认记录：记在哪由应用决定（apphost 记在 ~/.tenon/trust.toml，按项目）。
// widened 是放宽清单（顿号连接）；清单变了要重新确认。
type ModeApprovals interface {
	Approved(mode, widened string) bool
	Approve(mode, widened string) error
}

// Adapter 登记的协议适配：按装配结果填 o 里还没设的能力。
type Adapter func(o *appapi.Options, p Parts) error

var (
	regMu sync.Mutex
	regs  []Adapter
)

// Register 登记一个协议适配（通常在 init 里调用，如 tenon/std）。[Fill] 在通用部分之后按登记顺序调用。
func Register(f Adapter) {
	if f == nil {
		panic("adapters: Register(nil)")
	}
	regMu.Lock()
	defer regMu.Unlock()
	regs = append(regs, f)
}

func registered() []Adapter {
	regMu.Lock()
	defer regMu.Unlock()
	return append([]Adapter(nil), regs...)
}

// Fill 按装配结果填 o 的可选能力；o 里已经设了的字段不动（应用可以先换成自己的实现）。
// 先填通用部分，再跑 [Register] 登记的适配。
func Fill(o *appapi.Options, p Parts) error {
	if m := p.Models; m != nil {
		if o.Models == nil {
			o.Models = ModelList{M: m}
		}
		if o.Followups == nil {
			o.Followups = FollowupsFrom(m)
		}
		if o.Titler == nil {
			o.Titler = TitlerFrom(m)
		}
	}
	if o.Artifacts == nil && p.Blobs != nil {
		o.Artifacts = p.Blobs
	}
	if p.Asm != nil {
		if o.Services == nil { // 扩展命令取当前装配的服务（App.Service，docs/62 K1）
			o.Services = p.Asm.Value
		}
		for _, it := range p.Asm.Items(assemble.PointOnSessionDelete) { // 删除会话时的插件清理回调（session.delete）
			c, ok := it.Impl.(store.Cleaner)
			if !ok {
				return fmt.Errorf("插件 %s 的 %s 类型不符（%T）", it.Plugin, assemble.PointOnSessionDelete, it.Impl)
			}
			o.Cleaners = append(o.Cleaners, c)
		}
		if o.Compactor == nil {
			if v, ok := p.Asm.Value(assemble.ServiceCompactor); ok {
				if c, ok := v.(appapi.Compactor); ok {
					o.Compactor = c
				}
			}
		}
	}
	for _, f := range registered() {
		if err := f(o, p); err != nil {
			return err
		}
	}
	return nil
}
