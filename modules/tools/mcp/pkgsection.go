package mcp

// 插件包里的 [[server]] 段（2026-11-04 审查 P1-56）：段的形状与校验归本组件，runtime/pkgs 只认段名。

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/pkgs"
)

func init() {
	pkgs.RegisterSection(pkgs.Section{Key: "server", Owner: "mcp", Label: "MCP 服务",
		Decode: func(dir string, md toml.MetaData, prim toml.Primitive) (any, error) {
			var ss []Server
			if err := md.PrimitiveDecode(prim, &ss); err != nil {
				return nil, fmt.Errorf("[[server]]：%w", err)
			}
			if err := checkServers(ss); err != nil {
				return nil, err
			}
			for _, s := range ss {
				if s.Cwd != "" && !strings.Contains(s.Cwd, "${"+pkgs.DirVar+"}") {
					if _, err := pkgs.Within(dir, s.Cwd); err != nil {
						return nil, fmt.Errorf("服务 %s 的 cwd：%v", s.Name, err)
					}
				}
			}
			return ss, nil
		},
		Summary: func(v any) string {
			ss, _ := v.([]Server)
			ns := make([]string, 0, len(ss))
			for _, s := range ss {
				ns = append(ns, s.Name)
			}
			return "MCP 服务 " + strings.Join(ns, "、")
		}})
}

// packageServers 插件包里的 MCP 服务（没有返回 nil）。
func packageServers(p *pkgs.Package) []Server {
	ss, _ := pkgs.SectionValue[[]Server](p, "server")
	return ss
}
