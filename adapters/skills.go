package adapters

// 本地技能列表与启停（客户端改进 M11，配置T18）：把 skills 插件的 *skills.Skills 接成 appapi.Skills。

import (
	"context"
	"fmt"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/modules/context/skills"
	"github.com/Sevyn17/tenon/runtime/store"
)

// Skills appapi.Skills 的 skills 插件实现。
type Skills struct{ S *skills.Skills }

func skillInfo(i skills.Info) wire.SkillInfo {
	out := wire.SkillInfo{Name: i.Name, Description: i.Description, Source: i.Source, Path: i.Path,
		ModelInvocable: i.ModelInvocable, Enabled: i.Enabled}
	if !i.Enabled {
		out.DisabledScope, out.DisabledReason = i.Scope, i.Reason()
	}
	return out
}

func (a Skills) workspace() string {
	if a.S.State == nil {
		return ""
	}
	return a.S.State.Workspace
}

// List skills.list：全部技能（含已停用的）。启停记录读不出来时按全部启用，原因放进 warnings。
func (a Skills) List(context.Context) (wire.SkillsListResult, error) {
	is, err := a.S.Infos()
	out := wire.SkillsListResult{Skills: make([]wire.SkillInfo, 0, len(is)), Workspace: a.workspace(),
		Warnings: append([]string(nil), a.S.Warnings...)}
	if err != nil {
		out.Warnings = append(out.Warnings, "技能启停记录读不出来，按全部启用显示："+err.Error())
	}
	for _, i := range is {
		out.Skills = append(out.Skills, skillInfo(i))
	}
	return out, nil
}

// SetEnabled skills.setEnabled：只认当前列表里有的技能（not_found），写用户级的启停记录。
func (a Skills) SetEnabled(_ context.Context, in wire.SkillsSetEnabledArgs) (wire.SkillsSetEnabledResult, error) {
	if _, ok := a.S.Find(in.Name); !ok {
		return wire.SkillsSetEnabledResult{}, fmt.Errorf("%w: 没有技能 %s", store.ErrNotFound, in.Name)
	}
	if a.S.State == nil {
		return wire.SkillsSetEnabledResult{}, fmt.Errorf("技能目录没有启停记录")
	}
	if err := a.S.State.Set(in.Name, in.Enabled, in.Scope); err != nil {
		return wire.SkillsSetEnabledResult{}, err
	}
	is, err := a.S.Infos()
	if err != nil {
		return wire.SkillsSetEnabledResult{}, err
	}
	for _, i := range is {
		if i.Name == in.Name {
			return wire.SkillsSetEnabledResult{Skill: skillInfo(i)}, nil
		}
	}
	return wire.SkillsSetEnabledResult{}, fmt.Errorf("%w: 没有技能 %s", store.ErrNotFound, in.Name)
}
