package runtime

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
