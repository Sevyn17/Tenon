package std

// 依赖具体标准模块的协议适配（Mortise docs/62 §3.2、K6）：权限档位、技能、MCP、文件检查点、工作区文件列表。
// 原来写在 adapters.Fill 里，根包因此间接链接这些模块；现在在本包 init 里经 adapters.Register 登记，
// 只有导入 tenon/std 的应用才有这些能力。

import (
	"context"
	"strings"

	"github.com/Sevyn17/tenon/adapters"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/modules/context/skills"
	"github.com/Sevyn17/tenon/modules/security/permission"
	"github.com/Sevyn17/tenon/modules/tools/checkpoint"
	"github.com/Sevyn17/tenon/modules/tools/fs"
	"github.com/Sevyn17/tenon/modules/tools/mcp"
)

// MaxScanFiles workspace.files 最多遍历的文件数（大仓库里 @ 文件只看前这么多个）。
const MaxScanFiles = 20000

func init() { adapters.Register(Adapters) }

// Adapters 标准模块的协议适配（已在 init 里登记；导出给想自己组合登记表的应用看清楚做了什么）。
// o 里已经设了的字段不动。
func Adapters(o *appapi.Options, p adapters.Parts) error {
	if o.Files == nil && p.Workdir != "" {
		o.Files = Files(p.Workdir)
	}
	if p.Asm == nil {
		return nil
	}
	if o.Changes == nil { // 文件检查点（客户端改进 M9）：changes.* 与 session.rewind {restore_files}
		if v, ok := p.Asm.Value(checkpoint.ServiceName); ok {
			if e, ok := v.(*checkpoint.Engine); ok && e != nil {
				o.Changes = Changes{E: e}
			}
		}
	}
	if o.Skills == nil { // 本地技能（客户端改进 M11 配置T18）：skills.list / skills.setEnabled
		if v, ok := p.Asm.Value(skills.ServiceName); ok {
			if s, ok := v.(*skills.Skills); ok && s != nil {
				o.Skills = Skills{S: s}
			}
		}
	}
	if o.MCP == nil { // MCP 管理页（客户端改进 M11 配置T14）：mcp.list / test / setEnabled
		if v, ok := p.Asm.Value(mcp.Service); ok {
			if s, ok := v.(*mcp.ServiceAPI); ok && s != nil {
				o.MCP = MCP{S: s}
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
	return nil
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
