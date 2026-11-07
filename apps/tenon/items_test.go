package main

// 装配期校验的单元测试版：所有内置插件声明的权限项都必须通过结构校验与封顶表（§9.2），
// 否则 tenon 启动即拒绝——这类错误要在 go test 里抓住，不要等到真机验收。

import (
	"testing"

	"tenon/modules/collab/subagent"
	"tenon/modules/collab/workgroup"
	"tenon/modules/context/skills"
	"tenon/modules/security/permission"
	"tenon/modules/tools/codemode"
	"tenon/modules/tools/fs"
	"tenon/modules/tools/mcp"
	"tenon/modules/tools/shell"
	"tenon/modules/tools/web"
	"tenon/runtime/toolhub"
)

func TestPluginItemsWithinCaps(t *testing.T) {
	var all []toolhub.Item
	all = append(all, subagent.Items()...)
	all = append(all, workgroup.Items()...)
	all = append(all, skills.Items()...)
	all = append(all, codemode.Items()...)
	all = append(all, fs.Items(t.TempDir())...)
	all = append(all, mcp.Items(mcp.Config{Server: []mcp.Server{
		{Name: "a", Trust: mcp.TrustAsk}, {Name: "b", Trust: mcp.TrustReadonly}, {Name: "c", Trust: mcp.TrustTrusted}}})...)
	all = append(all, shell.Items()...)
	all = append(all, web.Items()...)
	seen := map[string]bool{}
	for _, it := range all {
		if err := it.Validate(); err != nil {
			t.Error(err)
		}
		if seen[it.ID] {
			t.Errorf("权限项重名 %s", it.ID)
		}
		seen[it.ID] = true
	}
	if err := permission.CheckItems(all); err != nil {
		t.Fatal(err)
	}
}
