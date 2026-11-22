package assemble

// 装配期校验工具引用的权限项（Mortise docs/62 §4.7、K6）。
//
// 权限项是装配级点位（PermItems），工具经 Tool.Items 引用别的插件的项（如产品终端用 shell.exec）。
// 原先只在每次构造会话时由 hub.Validate 检查，被引用项所属插件关掉时，错误要到第一次打开会话才出。
// 这里在 Wire 里对全部已登记工具跑一次同样的检查，启动即报错，并写明该打开哪个插件。

import (
	"errors"
	"fmt"
	"strings"
)

// CheckItemRefs 校验 col 里每个工具引用的权限项都已登记。工具集合在调用时取（组件已启动后），
// 与会话可见性无关：只要工具可能出现在某个会话里，它引用的项就必须在。
func CheckItemRefs(col *Collected) error {
	known := make(map[string]bool, len(col.Items))
	for _, it := range col.Items {
		known[it.ID] = true
	}
	var errs []error
	seen := map[string]bool{}
	for _, set := range col.Tools {
		if set.Tools == nil {
			continue
		}
		for _, t := range set.Tools() {
			if t == nil {
				continue
			}
			for _, id := range t.Items {
				if known[id] || seen[t.Spec.Name+"\x00"+id] {
					continue
				}
				seen[t.Spec.Name+"\x00"+id] = true
				owner, _, _ := strings.Cut(id, ".")
				errs = append(errs, fmt.Errorf("runtime: 工具 %s 引用了未登记的权限项 %s（所属插件 %s 没有开启或没有导入）", t.Spec.Name, id, owner))
			}
		}
	}
	return errors.Join(errs...)
}
