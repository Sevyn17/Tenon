package assemble

import (
	"testing"
	"time"
)

func TestParseKernel(t *testing.T) {
	k, err := ParseKernel(nil)
	if err != nil || k.Policy.MaxToolCallsPerStep != 16 || k.ToolTimeout != 120*time.Second {
		t.Fatalf("缺省值不对：%+v %v", k, err)
	}
	k, err = ParseKernel(map[string]any{"max_steps": int64(50), "tool_timeout": "5m", "spill_bytes": 1024})
	if err != nil {
		t.Fatal(err)
	}
	if k.Policy.MaxSteps != 50 || k.ToolTimeout != 5*time.Minute || k.Thresholds.Bytes != 1024 {
		t.Fatalf("解析不对：%+v", k)
	}
	for _, bad := range []map[string]any{
		{"nope": 1},
		{"max_steps": -1},
		{"tool_timeout": 3},
		{"max_tool_calls_per_step": 0},
	} {
		if _, err := ParseKernel(bad); err == nil {
			t.Fatalf("应报错：%v", bad)
		}
	}
}

// E 组：宿主参数（休眠、并发上限、所属树策略、保留期）经 [kernel] 配置，非法值报错。
func TestParseKernelHostParams(t *testing.T) {
	k, err := ParseKernel(map[string]any{"sleep_after": "10m", "concurrency": 4, "tree_policy": "restart", "tree_restarts": 2, "retain": "48h"})
	if err != nil {
		t.Fatal(err)
	}
	if k.SleepAfter.String() != "10m0s" || k.Concurrency != 4 || k.TreePolicy != "restart" || k.TreeRestarts != 2 || k.Retain.String() != "48h0m0s" {
		t.Fatalf("宿主参数解析错：%+v", k)
	}
	if k, err := ParseKernel(map[string]any{"sleep_after": "0"}); err != nil || k.SleepAfter != 0 {
		t.Fatalf("sleep_after = 0 表示关闭：%v %v", k.SleepAfter, err)
	}
	for _, bad := range []map[string]any{{"tree_policy": "retry"}, {"concurrency": -1}, {"retain": "0"}, {"sleep_after": "abc"}} {
		if _, err := ParseKernel(bad); err == nil {
			t.Fatalf("非法值应报错：%v", bad)
		}
	}
}
