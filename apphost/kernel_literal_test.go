package apphost

import (
	"testing"

	"tenon/runtime/toolhub"
)

// 内核参数按 profile 接上（缺省值）：原先 [kernel] 解析结果存在私有字段里，命令行另拼 Runtime 时拿到零值——
// 存工件阈值为 0，3 行的 shell 输出也被存成工件（2026-11-08 v1 验收 accept_m1.sh 发现）。现在只有一条装配路径（SDK）。
func TestBuildAppKernelWithoutStart(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TENON_HOME", t.TempDir())
	r, err := Start(StartOptions{Flags: Flags{Workdir: root, Yes: true, MaxSteps: -1},
		App: AppOptions{MaxSteps: -1, ModesWorkdir: root}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	built, err := r.BuildApp()
	if err != nil {
		t.Fatal(err)
	}
	k := built.Builder.Kernel
	if k == nil || k.Thresholds != toolhub.DefaultThresholds || k.ToolTimeout != toolhub.DefaultTimeout {
		t.Fatalf("内核参数应取 profile（缺省值），得 %+v", k)
	}
}
