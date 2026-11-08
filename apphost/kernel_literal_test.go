package apphost

import (
	"testing"

	"tenon/appapi"
	"tenon/runtime/assemble"
	"tenon/runtime/bus"
	"tenon/runtime/toolhub"
)

// CLI 不走 Start、自己拼 Runtime 再 BuildApp（client/tenon/main.go buildChat）：内核参数仍要按 profile 接上。
// 原先 [kernel] 解析结果存在 Start 才填的私有字段里，CLI 拿到零值：存工件阈值为 0，3 行的 shell 输出也被存成工件、
// 预览为空、翻页每页 0 行（2026-11-08 v1 验收 accept_m1.sh 发现）。
func TestBuildAppKernelWithoutStart(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TENON_HOME", t.TempDir())
	var ui *appapi.UI
	r, err := Start(StartOptions{Flags: Flags{Workdir: root, Yes: true, MaxSteps: -1},
		UI: func(b *bus.Bus) assemble.UI { ui = appapi.NewUI(b); return ui }})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cli := &Runtime{Rt: r.Rt, Profile: r.Profile, Root: r.Root, Bus: r.Bus, Host: r.Host}
	built, err := cli.BuildApp(AppOptions{MaxSteps: -1, UI: ui, ModesWorkdir: root})
	if err != nil {
		t.Fatal(err)
	}
	k := built.Builder.Kernel
	if k == nil || k.Thresholds != toolhub.DefaultThresholds || k.ToolTimeout != toolhub.DefaultTimeout {
		t.Fatalf("内核参数应取 profile（缺省值），得 %+v", k)
	}
}
