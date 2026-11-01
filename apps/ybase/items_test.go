package main

// 装配期校验的单元测试版：所有内置插件声明的权限项都必须通过结构校验与封顶表（§9.2），
// 否则 ybase 启动即拒绝——这类错误要在 go test 里抓住，不要等到真机验收。

import (
	"testing"

	"ybase-kernel/core/toolhub"
	"ybase-kernel/plugins/collab/subagent"
	"ybase-kernel/plugins/collab/workgroup"
	"ybase-kernel/plugins/context/skills"
	"ybase-kernel/plugins/security/permission"
	"ybase-kernel/plugins/tools/codemode"
	"ybase-kernel/plugins/tools/fs"
	"ybase-kernel/plugins/tools/mcp"
	"ybase-kernel/plugins/tools/shell"
	"ybase-kernel/plugins/tools/web"
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
