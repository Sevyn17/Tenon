// Package adapters 把插件的能力接成应用协议（appapi）的可选能力：模型列表与切换、权限档位、建议追问、
// 自动标题、工作区文件列表、手动压缩、工件、删除会话时的清理。
//
// appapi 属于框架核心，不能 import 插件包，所以这一层放在核心之外。SDK（tenon.App）与应用宿主（apphost）
// 都用它：用 SDK 自己装配的应用，Start 之后 App.API() 拿到的协议层已经接好这些能力，可以直接交给
// transport/web 或 transport/bind 提供给前端。
package adapters

import (
	"context"
	"fmt"
	"strings"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/modules/security/permission"
	"github.com/Sevyn17/tenon/modules/tools/fs"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// MaxScanFiles workspace.files 最多遍历的文件数（大仓库里 @ 文件只看前这么多个）。
const MaxScanFiles = 20000

// Parts 装配结果里接协议要用的部分。
type Parts struct {
	Asm    *component.Assembly
	Models *assemble.Models  // 可空：没有就不提供模型列表、追问、标题
	Blobs  toolhub.Artifacts // 可空：工件存储
	// Workdir workspace.files 遍历的目录；空 = 不提供文件列表。
	Workdir string
	// Approvals 项目档位放宽权限的确认记录；nil = 不记，每次都要确认。
	Approvals ModeApprovals
}

// Fill 按装配结果填 o 的可选能力；o 里已经设了的字段不动（应用可以先换成自己的实现）。
// 返回装配里的 permission 会话服务（没装 permission 时为 nil），应用还要用它时不必再查一次。
func Fill(o *appapi.Options, p Parts) (*permission.Sessions, error) {
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
	if o.Files == nil && p.Workdir != "" {
		o.Files = Files(p.Workdir)
	}
	if p.Asm == nil {
		return nil, nil
	}
	for _, it := range p.Asm.Items(assemble.PointOnSessionDelete) { // 删除会话时的插件清理回调（session.delete）
		c, ok := it.Impl.(store.Cleaner)
		if !ok {
			return nil, fmt.Errorf("插件 %s 的 %s 类型不符（%T）", it.Plugin, assemble.PointOnSessionDelete, it.Impl)
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
	var perms *permission.Sessions
	if v, ok := p.Asm.Value(permission.Service); ok {
		perms, _ = v.(*permission.Sessions)
	}
	if perms != nil {
		if o.Modes == nil {
			o.Modes = PermModes{P: perms, Approvals: p.Approvals}
		}
		// 项目档位当新会话缺省时，也要用户确认过放宽（与 mode.set 切换共用同一份确认记录）。
		ap := p.Approvals
		perms.ModeApproved = func(m permission.Mode, widened []string) bool {
			return ap != nil && ap.Approved(string(m), strings.Join(widened, "、"))
		}
	}
	return perms, nil
}

// Files workspace.files 的实现：按 fs 插件的规则（遵守 .gitignore、不出目录）列 root 下的文件。
func Files(root string) appapi.Files {
	return func(ctx context.Context, q string, limit int) ([]wire.FileInfo, bool, error) {
		hits, trunc, err := fs.ListFiles(ctx, root, q, limit, MaxScanFiles)
		out := make([]wire.FileInfo, len(hits))
		for i, h := range hits {
			out[i] = wire.FileInfo{Path: h.Path, Size: h.Size, MTime: h.MTime}
		}
		return out, trunc, err
	}
}
