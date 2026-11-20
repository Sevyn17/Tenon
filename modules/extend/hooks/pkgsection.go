package hooks

// 插件包里的 [[hook]] 段（2026-11-04 审查 P1-56）：段的形状与校验归本组件，runtime/pkgs 只认段名。

import (
	"fmt"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/pkgs"
)

func init() {
	pkgs.RegisterSection(pkgs.Section{Key: "hook", Owner: "hooks", Label: "钩子",
		Decode: func(_ string, md toml.MetaData, prim toml.Primitive) (any, error) {
			var hs []Hook
			if err := md.PrimitiveDecode(prim, &hs); err != nil {
				return nil, fmt.Errorf("[[hook]]：%w", err)
			}
			if err := checkHooks(hs); err != nil {
				return nil, err
			}
			return hs, nil
		},
		Summary: func(v any) string {
			hs, _ := v.([]Hook)
			return fmt.Sprintf("钩子 %d 条", len(hs))
		},
		// 风险（客户端改进 M11 T22/T24）：钩子执行命令；沙箱缺省 workspace-write，不是 read-only 时可能写文件。
		Risks: func(v any) []string {
			hs, _ := v.([]Hook)
			var rs []string
			for _, h := range hs {
				rs = append(rs, pkgs.RiskExec)
				if h.Sandbox != "read-only" {
					rs = append(rs, pkgs.RiskWrite)
				}
			}
			return rs
		}})
}

// packageHooks 插件包里的钩子（已编译 matcher；没有返回 nil）。
func packageHooks(p *pkgs.Package) []Hook {
	hs, _ := pkgs.SectionValue[[]Hook](p, "hook")
	return hs
}
