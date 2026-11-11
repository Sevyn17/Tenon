package component

// T5c：模型判断类匹配器原语——失败按不匹配、判断次数上限本地兜底。

import (
	"context"
	"errors"
	"testing"
)

func TestModelMatcherFailureIsNoMatch(t *testing.T) {
	m := ModelMatcher(func(context.Context, ResourceRecord, map[string]string) (bool, error) {
		return false, errors.New("模型炸了")
	}, 0, nil)
	got, err := m(ResourceRecord{Body: "x"}, nil)
	if err != nil || got.Matched {
		t.Fatalf("判断失败应按不匹配、不报错：%v %v", got, err)
	}
}

func TestModelMatcherYes(t *testing.T) {
	m := ModelMatcher(func(context.Context, ResourceRecord, map[string]string) (bool, error) {
		return true, nil
	}, 0, nil)
	got, err := m(ResourceRecord{Body: "x"}, nil)
	if err != nil || !got.Matched {
		t.Fatalf("判断为真应命中：%v %v", got, err)
	}
}

func TestModelMatcherCap(t *testing.T) {
	m := ModelMatcher(func(context.Context, ResourceRecord, map[string]string) (bool, error) {
		return true, nil
	}, 2, nil)
	for i := 0; i < 2; i++ {
		if got, _ := m(ResourceRecord{Body: "x"}, nil); !got.Matched {
			t.Fatalf("第 %d 次应命中", i+1)
		}
	}
	if got, _ := m(ResourceRecord{Body: "x"}, nil); got.Matched {
		t.Fatal("判断次数用尽后应按不匹配")
	}
}

// 判断次数用尽时回调只触发一次（T5c：记 guard.denied 类事件不刷屏）。
func TestModelMatcherExhaustedCallbackOnce(t *testing.T) {
	var n int
	m := ModelMatcher(func(context.Context, ResourceRecord, map[string]string) (bool, error) {
		return true, nil
	}, 1, func() { n++ })
	_, _ = m(ResourceRecord{Body: "x"}, nil) // 第 1 次：命中，用满
	for i := 0; i < 5; i++ {
		if got, _ := m(ResourceRecord{Body: "x"}, nil); got.Matched {
			t.Fatal("用尽后应按不匹配")
		}
	}
	if n != 1 {
		t.Fatalf("用尽回调应只触发一次，实际 %d", n)
	}
}
