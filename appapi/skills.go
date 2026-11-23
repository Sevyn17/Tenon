package appapi

// 本地技能列表与单个技能启停（客户端改进 M11，配置T18；docs/50 5.1：技能是「定义」，全局一份，按范围记录启用）。
// 能力由 skills 插件经 adapters.Skills 注入（Options.Skills）；没装（或插件没开）时这两条命令返回 unsupported。
//
// 启停记录在用户级目录（<home>/skills.toml），不写进项目仓库。改了之后对新建的会话生效：系统提示里的技能目录
// 在建会话时定下（前缀缓存稳定），已有会话的目录不变，但 skill 工具装载已停用的技能会被拒绝。不需要重建运行时。

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// 本文件的命令（按文件登记，v2.go 的 registerCommands）。
func init() {
	registerCommands(map[string]handler{
		"skills.list":       bind((*App).SkillsList),
		"skills.setEnabled": bind((*App).SkillsSetEnabled),
		"skills.get":        bind((*App).SkillsGet),
	})
}

// SkillReader 可选：Skills 实现再实现它就支持 skills.get（读正文）；没实现时 skills.get 返回 unsupported。
// 正文已去掉 frontmatter；截断由 App 统一按 wire.SkillBodyMax 做。
type SkillReader interface {
	Body(ctx context.Context, name string) (wire.SkillInfo, string, error)
}

// SkillsGet 读一个技能的正文（只读，viewer 可用，与 skills.list 同权限）。
func (v *App) SkillsGet(ctx context.Context, in wire.SkillsGetArgs) (wire.SkillsGetResult, error) {
	s, err := v.skills()
	if err != nil {
		return wire.SkillsGetResult{}, err
	}
	r, ok := s.(SkillReader)
	if !ok {
		return wire.SkillsGetResult{}, Errorf(wire.CodeUnsupported, "skill body not available")
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return wire.SkillsGetResult{}, invalid("missing name")
	}
	if in.Workspace != "" {
		l, err := s.List(ctx)
		if err != nil {
			return wire.SkillsGetResult{}, err
		}
		if l.Workspace != "" && l.Workspace != in.Workspace {
			return wire.SkillsGetResult{}, Errorf(wire.CodeConflict, "workspace changed")
		}
	}
	info, body, err := r.Body(ctx, in.Name)
	if err != nil {
		return wire.SkillsGetResult{}, err
	}
	out := wire.SkillsGetResult{Skill: info, Body: body, Size: len(body)}
	if len(body) > wire.SkillBodyMax {
		out.Body, out.Truncated = clipUTF8(body, wire.SkillBodyMax), true
	}
	return out, nil
}

// clipUTF8 截到不超过 n 字节且不切断多字节字符。
func clipUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Skills 技能目录与启停记录（skills 插件适配，见 adapters.Skills）。
type Skills interface {
	List(ctx context.Context) (wire.SkillsListResult, error)
	// SetEnabled 改一条记录；技能不存在返回 store.ErrNotFound（not_found）。scope 已由调用方规范成 SkillScope*。
	SetEnabled(ctx context.Context, in wire.SkillsSetEnabledArgs) (wire.SkillsSetEnabledResult, error)
}

func (v *App) skills() (Skills, error) {
	if v.o.Skills == nil {
		return nil, Errorf(wire.CodeUnsupported, "skills plugin not enabled")
	}
	return v.o.Skills, nil
}

// SkillsList 当前工作区看得到的全部技能（含已停用的）与启停状态。
func (v *App) SkillsList(ctx context.Context, _ wire.Empty) (wire.SkillsListResult, error) {
	s, err := v.skills()
	if err != nil {
		return wire.SkillsListResult{}, err
	}
	r, err := s.List(ctx)
	if err != nil {
		return wire.SkillsListResult{}, err
	}
	if r.Skills == nil {
		r.Skills = []wire.SkillInfo{}
	}
	return r, nil
}

// SkillsSetEnabled 启用或停用一个技能（写命令，只给 operator）。scope 空 = global。
func (v *App) SkillsSetEnabled(ctx context.Context, in wire.SkillsSetEnabledArgs) (wire.SkillsSetEnabledResult, error) {
	s, err := v.skills()
	if err != nil {
		return wire.SkillsSetEnabledResult{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return wire.SkillsSetEnabledResult{}, invalid("missing name")
	}
	switch in.Scope {
	case "":
		in.Scope = wire.SkillScopeGlobal
	case wire.SkillScopeGlobal, wire.SkillScopeWorkspace:
	default:
		return wire.SkillsSetEnabledResult{}, invalid("scope must be %s or %s, got %q", wire.SkillScopeGlobal, wire.SkillScopeWorkspace, in.Scope)
	}
	return s.SetEnabled(ctx, in)
}
