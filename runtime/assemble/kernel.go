package assemble

// [kernel] 配置节（plugin-switch-plan §1，登记见 docs/01 - 设计文档/03 - 约束清单.md「[kernel] 配置节」）：
// 内核机制参数，不改会话语义；未写的键用内核缺省值。

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Kernel 解析后的 [kernel] 节。
type Kernel struct {
	Policy          session.Policy
	ToolTimeout     time.Duration
	ConfirmDeadline time.Duration
	Thresholds      toolhub.Thresholds

	// 宿主参数（不影响会话内容，02 2.2 / 2.4）：0 / 空 = 内核缺省。
	SleepAfter   time.Duration // 空闲多久后休眠（0 = 不休眠）；>0 时同时启动兜底扫描
	Concurrency  int           // 同时推理的会话数上限（0 = 不限）
	TreePolicy   string        // 子会话一轮失败时：give_up（缺省）/ restart / escalate
	TreeRestarts int           // restart 策略下的最多重跑次数（0 = 1）
	Retain       time.Duration // 观测与记账行的保留期（0 = 7 天）
	MaxHop       int           // 单封信封最大跳数（0 = 内核缺省 8；租户可调小，08 8.4 / T4f）
}

// DefaultKernel 内核缺省值。
func DefaultKernel() Kernel {
	return Kernel{Policy: session.DefaultPolicy(), ToolTimeout: toolhub.DefaultTimeout,
		ConfirmDeadline: toolhub.DefaultConfirmDeadline, Thresholds: toolhub.DefaultThresholds}
}

// ParseKernel 把 profile 的 [kernel] 节解析到缺省值上；未知键报错。
func ParseKernel(m map[string]any) (Kernel, error) {
	k := DefaultKernel()
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		v := m[key]
		var err error
		switch key {
		case "max_steps":
			err = setUint(v, func(n uint64) { k.Policy.MaxSteps = uint32(n) }, 1<<31)
		case "max_tool_calls_per_step":
			err = setUint(v, func(n uint64) { k.Policy.MaxToolCallsPerStep = uint32(n) }, 1<<31)
		case "max_result_bytes":
			err = setUint(v, func(n uint64) { k.Policy.MaxResultBytes = n }, 1<<40)
		case "tool_timeout":
			err = setDur(v, &k.ToolTimeout)
		case "confirm_deadline":
			err = setDur(v, &k.ConfirmDeadline)
		case "spill_bytes":
			err = setUint(v, func(n uint64) { k.Thresholds.Bytes = int(n) }, 1<<31)
		case "spill_lines":
			err = setUint(v, func(n uint64) { k.Thresholds.Lines = int(n) }, 1<<31)
		case "preview_bytes":
			err = setUint(v, func(n uint64) { k.Thresholds.Preview = int(n) }, 1<<31)
		case "page_lines":
			err = setUint(v, func(n uint64) { k.Thresholds.PageLines = int(n) }, 1<<31)
		case "sleep_after":
			err = setDurOrZero(v, &k.SleepAfter)
		case "concurrency":
			err = setUint(v, func(n uint64) { k.Concurrency = int(n) }, 1<<16)
		case "tree_policy":
			s, ok := v.(string)
			switch {
			case !ok:
				err = fmt.Errorf("应为字符串，得到 %v", v)
			case s == "give_up" || s == "restart" || s == "escalate":
				k.TreePolicy = s
			default:
				err = fmt.Errorf("只能是 give_up / restart / escalate，得到 %q", s)
			}
		case "tree_restarts":
			err = setUint(v, func(n uint64) { k.TreeRestarts = int(n) }, 100)
		case "retain":
			err = setDur(v, &k.Retain)
		case "max_hop":
			err = setUint(v, func(n uint64) { k.MaxHop = int(n) }, 1<<16)
		default:
			return k, fmt.Errorf("[kernel] 未知配置项 %q", key)
		}
		if err != nil {
			return k, fmt.Errorf("[kernel] %s: %w", key, err)
		}
	}
	if k.Policy.MaxToolCallsPerStep == 0 {
		return k, fmt.Errorf("[kernel] max_tool_calls_per_step 不能为 0")
	}
	return k, nil
}

func setUint(v any, set func(uint64), max uint64) error {
	b, _ := json.Marshal(v)
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("应为非负整数，得到 %v", v)
	}
	var u uint64
	if _, err := fmt.Sscan(n.String(), &u); err != nil || u > max {
		return fmt.Errorf("应为不超过 %d 的非负整数，得到 %v", max, v)
	}
	set(u)
	return nil
}

// setDurOrZero 同 setDur，但允许 "0" / "0s" 表示关闭。
func setDurOrZero(v any, d *time.Duration) error {
	if s, ok := v.(string); ok && (s == "0" || s == "0s") {
		*d = 0
		return nil
	}
	return setDur(v, d)
}

func setDur(v any, d *time.Duration) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("应为时长字符串（如 \"120s\"），得到 %v", v)
	}
	x, err := time.ParseDuration(s)
	if err != nil || x <= 0 {
		return fmt.Errorf("无效时长 %q", s)
	}
	*d = x
	return nil
}
