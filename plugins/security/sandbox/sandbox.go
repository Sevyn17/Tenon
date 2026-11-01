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
	"time"

	"ybase-kernel/core/toolhub"
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
	// Protect 工作区内即使 workspace-write 也不可写的子目录（相对 Root），默认 .ybase。
	Protect []string
	// TempDir 沙箱专用临时 / 缓存目录的上级；空 = %LOCALAPPDATA%\ybase\sbtmp（plugin-switch-plan §2.2）。
	TempDir string
	// KeepGoCache true = 不把 GOCACHE 改到沙箱临时目录（用户自己保证缓存可写）。
	KeepGoCache bool
}

func (s *Sandbox) protect() []string {
	if s.Protect == nil {
		return []string{".ybase"}
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
