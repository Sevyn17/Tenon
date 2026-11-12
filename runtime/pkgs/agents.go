package pkgs

// 插件包带定义（T4d，04 4.8）：agents 字段指向一份 agentdef.DirSource 布局的目录。
// 本包不解析定义，只把目录交给 agentdef，校验与信任规则都沿用现成的。

import (
	"context"
	"os"

	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/component"
)

// AgentSources 带定义的包各变成一个目录来源，顺序同 ps（Load 的顺序：用户级在前、项目级在后）。
// 交给 agentdef.Multi 合并时同层同名以先给的为准，即用户级包优先于项目级包。
func AgentSources(ps []*Package) []agentdef.Source {
	var out []agentdef.Source
	for _, p := range ps {
		if p != nil && p.AgentsDir != "" {
			out = append(out, agentdef.FS(os.DirFS(p.AgentsDir), p.AgentsDir))
		}
	}
	return out
}

// Infos 包的摘要（explain 用）。scope 决定列哪些层的定义（装配期用租户范围，不含用户层，
// 所以包里 user/<租户>/<用户>/<名> 的定义不会列进 explain——它们是按用户可见的，explain 只列平台 / 租户层）；
// 列不出（目录读失败）时定义一栏记成说明。
func Infos(ctx context.Context, ps []*Package, scope agentdef.Scope) []component.PackageInfo {
	out := make([]component.PackageInfo, 0, len(ps))
	for _, p := range ps {
		if p == nil {
			continue
		}
		info := component.PackageInfo{Name: p.Name, Scope: p.Scope, File: p.File, Contents: p.Contents()}
		if p.AgentsDir != "" {
			refs, err := agentdef.FS(os.DirFS(p.AgentsDir), p.AgentsDir).List(ctx, scope)
			if err != nil {
				info.Agents = []string{"（读定义目录失败：" + err.Error() + "）"}
				out = append(out, info)
				continue
			}
			for _, r := range refs {
				info.Agents = append(info.Agents, r.String())
			}
		}
		out = append(out, info)
	}
	return out
}
