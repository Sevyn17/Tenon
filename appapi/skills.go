package appapi

// 本地技能列表与单个技能启停（客户端改进 M11，配置T18；docs/50 5.1：技能是「定义」，全局一份，按范围记录启用）。
// 能力由 skills 插件经 adapters.Skills 注入（Options.Skills）；没装（或插件没开）时这两条命令返回 unsupported。
//
// 启停记录在用户级目录（<home>/skills.toml），不写进项目仓库。改了之后对新建的会话生效：系统提示里的技能目录
// 在建会话时定下（前缀缓存稳定），已有会话的目录不变，但 skill 工具装载已停用的技能会被拒绝。不需要重建运行时。

import (
	"context"
	"strings"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// Skills 技能目录与启停记录（skills 插件适配，见 adapters.Skills）。
type Skills interface {
	List(ctx context.Context) (wire.SkillsListResult, error)
	// SetEnabled 改一条记录；技能不存在返回 store.ErrNotFound（not_found）。scope 已由调用方规范成 SkillScope*。
	SetEnabled(ctx context.Context, in wire.SkillsSetEnabledArgs) (wire.SkillsSetEnabledResult, error)
}

func (v *App) skills() (Skills, error) {
	if v.o.Skills == nil {
		return nil, Errorf(wire.CodeUnsupported, "未开技能插件（skills）")
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
		return wire.SkillsSetEnabledResult{}, invalid("缺少 name")
	}
	switch in.Scope {
	case "":
		in.Scope = wire.SkillScopeGlobal
	case wire.SkillScopeGlobal, wire.SkillScopeWorkspace:
	default:
		return wire.SkillsSetEnabledResult{}, invalid("scope 只能是 %s / %s，得到 %q", wire.SkillScopeGlobal, wire.SkillScopeWorkspace, in.Scope)
	}
	return s.SetEnabled(ctx, in)
}
