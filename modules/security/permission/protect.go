package permission

// protect.go：受保护目录（Mortise docs/63 W-6）。
//
// 应用 home（会话库、密钥、配置、记忆）不应被智能体读写：会话库是用户级的，读到它就绕过了工作区隔离。
// Windows 沙箱只限写不限读，所以读这一侧在权限层按参数拦。
//
// 判定在所有规则之前，命中即 deny：档位、公共规则、本会话放行都不能覆盖。
//   - 带 path 的工具：路径（相对路径按 Root 解析）落在受保护目录内即拒；
//   - 命令：任一命令视图（含拆段、启动器内层）提到受保护目录即拒（大小写按平台、斜杠方向都归一）。
//
// 局限：命令侧是字面匹配，经变量、通配、编码拼出来的路径看不出来（如 `cat $HOME/<应用目录>/x`）；
// 这类依赖沙箱与运行时其它防线，这里只挡直白写法。

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// protectedHit 本次调用是否触及受保护目录；返回命中的目录。
func (p Policy) protectedHit(args json.RawMessage) (string, bool) {
	if len(p.Protected) == 0 {
		return "", false
	}
	var a struct {
		Command string `json:"command"`
		Path    string `json:"path"`
	}
	_ = json.Unmarshal(args, &a)
	if a.Path != "" {
		path := a.Path
		if !filepath.IsAbs(path) && p.Root != "" {
			path = filepath.Join(p.Root, path)
		}
		path = normPath(filepath.Clean(path))
		for _, d := range p.Protected {
			nd := normPath(filepath.Clean(d))
			if nd != "" && (path == nd || strings.HasPrefix(path, nd+"/")) {
				return d, true
			}
		}
	}
	if a.Command != "" {
		views := commandViews(a.Command, 0)
		for _, d := range p.Protected {
			nd := normPath(filepath.Clean(d))
			if nd == "" {
				continue
			}
			for _, v := range views {
				if strings.Contains(normPath(v), nd) {
					return d, true
				}
			}
		}
	}
	return "", false
}

// normPath 斜杠统一为 /；Windows 上不分大小写（同 Glob）。
func normPath(s string) string {
	s = strings.ReplaceAll(s, `\`, "/")
	if foldCase {
		s = strings.ToLower(s)
	}
	return s
}

func protectedDecision(dir string) Decision {
	return Decision{Action: Deny, Reason: fmt.Sprintf("受保护目录 %s（应用数据，不可由智能体访问）", dir)}
}
