package toolhub

// 工具管理器定义的通用工具属性与沙箱词汇（thin-kernel-plan §2.3）：
// 工具插件、权限插件、沙箱插件都按这里的名字通信，彼此不 import。

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"ybase/runtime/ext"
)

const (
	// AttrWorkspace 本工具只能改工作区内的文件（路径已在工具内限定）。
	AttrWorkspace = "workspace_confined"
	// AttrSandboxed 本工具的执行受沙箱约束，参数可带 sandbox_permissions。
	AttrSandboxed = "sandboxed"
)

// 效果类别（06 - 工具权限设计方案 §二）：权限项的 Item.Class 取这些值，权限插件按它查封顶表。
// 原先工具上另有一份 Attrs["effect"]，与 Item.Class 重复且没有读取方（2026-11-04 审查 P2），已删。
const (
	EffectRead        = "read"
	EffectWrite       = "write"
	EffectExec        = "exec"
	EffectNetwork     = "network"
	EffectInteract    = "interact"
	EffectSpawn       = "spawn"
	EffectCollab      = "collab"
	EffectOrchestrate = "orchestrate"
	EffectExternal    = "external"
)

// Effects 全部效果类别。
var Effects = []string{EffectRead, EffectWrite, EffectExec, EffectNetwork, EffectInteract,
	EffectSpawn, EffectCollab, EffectOrchestrate, EffectExternal}

// SandboxMode 沙箱模式。
type SandboxMode string

const (
	SandboxReadOnly       SandboxMode = "read-only"
	SandboxWorkspaceWrite SandboxMode = "workspace-write"
	SandboxFull           SandboxMode = "danger-full-access"
)

// ParseSandboxMode 校验模式名（空 = workspace-write）。
func ParseSandboxMode(s string) (SandboxMode, error) {
	switch SandboxMode(s) {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxFull:
		return SandboxMode(s), nil
	case "":
		return SandboxWorkspaceWrite, nil
	}
	return "", fmt.Errorf("sandbox: 未知模式 %q（read-only / workspace-write / danger-full-access）", s)
}

// Rank 宽度序：read-only 0、workspace-write 1、full 2；未知 -1。
func (m SandboxMode) Rank() int {
	switch m {
	case SandboxReadOnly:
		return 0
	case SandboxWorkspaceWrite:
		return 1
	case SandboxFull:
		return 2
	}
	return -1
}

// SandboxWider 报告 a 是否严格宽于 b。
func SandboxWider(a, b SandboxMode) bool { return a.Rank() > b.Rank() }

// RequestedSandbox 从工具参数取 sandbox_permissions（空 = 未申请）。
func RequestedSandbox(args json.RawMessage) (SandboxMode, error) {
	var a struct {
		P string `json:"sandbox_permissions"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.P == "" {
		return "", nil
	}
	return ParseSandboxMode(a.P)
}

// EffectiveSandbox 执行器侧核对：申请更宽的模式必须已获批准（纵深防御）。
func EffectiveSandbox(session SandboxMode, args json.RawMessage, approved bool) (SandboxMode, error) {
	req, err := RequestedSandbox(args)
	if err != nil {
		return "", err
	}
	if req == "" {
		return session, nil
	}
	if SandboxWider(req, session) && !approved {
		return "", fmt.Errorf("申请 %s 未获批准", req)
	}
	return req, nil
}

// Confiner 把子进程放进给定模式的沙箱（由沙箱插件实现，工具插件经服务取用）。
// 返回的 cleanup 在 cmd.Start 之后调用；无法强制执行时返回错误，调用方必须拒绝执行。
type Confiner interface {
	Apply(cmd *exec.Cmd, mode SandboxMode) (cleanup func(), err error)
}

// SandboxPolicy 按会话给出沙箱模式（由权限插件实现，沙箱 / shell 插件经服务取用）。
type SandboxPolicy interface {
	SandboxMode(sessionID string) SandboxMode
}

// SandboxApprover 可选：报告某次更宽沙箱申请是否已被会话规则批准（“本次会话内都允许”）。
// 由权限插件实现；沙箱 Guard 与 shell 据此免确认、放宽执行。
type SandboxApprover interface {
	AllowsSandbox(sessionID, tool string, args json.RawMessage, req SandboxMode) bool
}

// 沙箱相关服务的类型化键（审查 P1-48）：sandbox 组件提供 KeySandbox，permission 组件提供 KeySandboxPolicy。
var (
	KeySandbox       = ext.NewKey[Confiner](ext.ServiceSandbox)
	KeySandboxPolicy = ext.NewKey[SandboxPolicy](ext.ServicePermission)
)
