package component

// 模型判断类匹配器原语（框架设计 04 4.7，T5c）。
//
// 普通匹配器是纯函数（只看记录与参数、不读存储、不调模型）。模型判断类匹配器要调模型，
// 由组件登记：组件用 ModelMatcher 把「一个判断函数（可内部调模型）+ 失败按不匹配 + 判断次数上限」
// 包成标准 Matcher。内核只内置 exact / prefix / list，不内置模型匹配器；扩展进程也不能内置（T6b）。

import (
	"context"
	"sync/atomic"
)

// Judge 一次模型判断：返回这条记录是否命中本订阅。可调模型（阻塞）。
type Judge func(ctx context.Context, rec ResourceRecord, params map[string]string) (bool, error)

// ModelMatcher 把判断函数包成 Matcher：失败按不匹配（不阻塞扇出）；max>0 时判断次数用尽即判不匹配
// 并回调 onExhausted（组件用它记 guard.denied 类事件；可为空）。
func ModelMatcher(j Judge, max int, onExhausted func()) Matcher {
	if j == nil {
		return func(ResourceRecord, map[string]string) (Match, error) { return Match{}, nil }
	}
	var used atomic.Int64
	var reported atomic.Bool
	return func(rec ResourceRecord, params map[string]string) (Match, error) {
		if max > 0 && used.Load() >= int64(max) {
			if onExhausted != nil && reported.CompareAndSwap(false, true) {
				onExhausted()
			}
			return Match{}, nil // 判断次数用尽：按不匹配处理，不阻塞其余订阅
		}
		// 每次实际发起判断都计数（命中、未命中、失败都算）：上限按"判断次数"算，
		// 否则模型持续失败时永远触发不到上限。
		used.Add(1)
		ok, err := j(context.Background(), rec, params)
		if err != nil {
			return Match{}, nil // 判断失败按不匹配，不阻塞扇出
		}
		if !ok {
			return Match{}, nil
		}
		return Match{Matched: true}, nil
	}
}
