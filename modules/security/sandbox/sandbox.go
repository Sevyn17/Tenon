// Package sandbox：子进程 OS 级沙箱（docs/90 - 归档/20 - 插件/06 - 沙箱.md，安全项）。
//
// 机制：Apply 在进程启动前把 exec.Cmd 放进给定模式的沙箱；无法强制执行时返回
// ErrUnavailable，调用方必须拒绝执行（不降级）。
// 策略：Layer 是工具 Guard——申请比会话模式更宽的沙箱时要求人工确认（一次性）。
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"ybase/runtime/toolhub"
)

// Mode 沙箱模式（词汇定义在 toolhub，工具与权限插件不必 import 本包）。
type Mode = toolhub.SandboxMode

const (
	ReadOnly       = toolhub.SandboxReadOnly
	WorkspaceWrite = toolhub.SandboxWorkspaceWrite
	Full           = toolhub.SandboxFull
)

// Code 不可强制执行时的稳定错误码。
const Code = "SANDBOX_UNAVAILABLE"

// ErrUnavailable 请求的模式无法强制执行。
var ErrUnavailable = errors.New(Code)

// AttrSandboxed 工具属性：本工具的执行受沙箱约束，参数可带 sandbox_permissions。
const AttrSandboxed = toolhub.AttrSandboxed

// ParseMode 校验模式名（空 = workspace-write）。
func ParseMode(s string) (Mode, error) { return toolhub.ParseSandboxMode(s) }

func rank(m Mode) int { return m.Rank() }

// Wider 报告 a 是否严格宽于 b。
func Wider(a, b Mode) bool { return toolhub.SandboxWider(a, b) }

// Sandbox 一个工作区的沙箱。
type Sandbox struct {
	Root string // 工作区根（绝对路径）
	Mode Mode   // 会话默认模式
	// Protect 工作区内即使 workspace-write 也不可写的子目录（相对 Root），默认 DefaultProtect。
	Protect []string
	// TempDir 沙箱专用临时 / 缓存目录的上级；空 = %LOCALAPPDATA%\ybase\sbtmp（plugin-switch-plan §2.2）。
	TempDir string
	// KeepGoCache true = 不把 GOCACHE 改到沙箱临时目录（用户自己保证缓存可写）。
	KeepGoCache bool
	// Degrade 本平台没有受限模式实现时按 danger-full-access 执行（不报错）。插件装配时设为真并告警一次；
	// 平台能力只由本包判断（2026-11-04 审查 P1-14：原先权限插件写死"非 Windows 一律 full access"，且不提示）。
	Degrade bool
}

// PlatformSupported 本平台是否有受限模式（read-only / workspace-write）的实现。
func PlatformSupported() bool { return platformSupported }

// DefaultProtect 缺省受保护路径：
//   - .ybase：本框架的项目配置（权限档位、钩子、MCP）；
//   - .git：沙箱里写进 .git/hooks、.git/config 的东西，会在用户之后于沙箱外执行 git 时以完整权限运行，
//     等于逃出沙箱（2026-11-04 审查 P0-14）。代价：workspace-write 里不能 git commit，需要时申请更宽沙箱。
var DefaultProtect = []string{".ybase", ".git"}

// existingOnly 只在已存在时才保护、不存在不新建的路径（新建空 .git 会让 git 误以为这是仓库）。
func existingOnly(p string) bool { return filepath.Clean(p) == ".git" }

func (s *Sandbox) protect() []string {
	if s.Protect == nil {
		return DefaultProtect
	}
	return s.Protect
}

// Apply 把 cmd 放进 mode 沙箱。返回的 cleanup 在 cmd.Start 之后调用（释放令牌等）。
func (s *Sandbox) Apply(cmd *exec.Cmd, mode Mode) (cleanup func(), err error) {
	if mode == Full {
		return func() {}, nil
	}
	if rank(mode) < 0 {
		return nil, fmt.Errorf("%w：未知模式 %q", ErrUnavailable, mode)
	}
	if !platformSupported && s.Degrade {
		return func() {}, nil
	}
	return apply(s, cmd, mode)
}

// Requested 从工具参数取 sandbox_permissions（空 = 未申请）。
func Requested(args json.RawMessage) (Mode, error) { return toolhub.RequestedSandbox(args) }

// Effective 执行器侧核对：申请更宽的模式必须已获批准（纵深防御）。
func Effective(session Mode, args json.RawMessage, approved bool) (Mode, error) {
	return toolhub.EffectiveSandbox(session, args, approved)
}

// Layer 沙箱 Guard：受沙箱工具申请更宽模式时要求人工确认（权限模式 full 也不例外）。
// approve 可空：会话规则已批准同类申请时免确认。
func Layer(mode func(sessionID string) Mode, approve ...toolhub.SandboxApprover) toolhub.Layer {
	return toolhub.Layer{
		Name:    "sandbox",
		Guard:   true,
		Timeout: 5 * time.Second,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			if on, _ := cx.Tool.Attrs[AttrSandboxed].(bool); !on {
				return next(ctx, cx, args)
			}
			req, err := Requested(args)
			if err != nil {
				return toolhub.Errorf("参数不合法：%v", err), nil
			}
			cur := mode(cx.SessionID)
			if req != "" && Wider(req, cur) && !cx.Approved {
				for _, a := range approve {
					if a != nil && a.AllowsSandbox(cx.SessionID, cx.Tool.Spec.Name, args, req) {
						return next(ctx, cx, args)
					}
				}
				return toolhub.Output{Kind: toolhub.Confirm,
					Reason: fmt.Sprintf("%s 申请沙箱 %s（当前 %s），仅本次有效", cx.Tool.Spec.Name, req, cur)}, nil
			}
			return next(ctx, cx, args)
		},
	}
}
