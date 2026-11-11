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
	}, 0)
	got, err := m(ResourceRecord{Body: "x"}, nil)
	if err != nil || got.Matched {
		t.Fatalf("判断失败应按不匹配、不报错：%v %v", got, err)
	}
}

func TestModelMatcherYes(t *testing.T) {
	m := ModelMatcher(func(context.Context, ResourceRecord, map[string]string) (bool, error) {
		return true, nil
	}, 0)
	got, err := m(ResourceRecord{Body: "x"}, nil)
	if err != nil || !got.Matched {
		t.Fatalf("判断为真应命中：%v %v", got, err)
	}
}

func TestModelMatcherCap(t *testing.T) {
	m := ModelMatcher(func(context.Context, ResourceRecord, map[string]string) (bool, error) {
		return true, nil
	}, 2)
	for i := 0; i < 2; i++ {
		if got, _ := m(ResourceRecord{Body: "x"}, nil); !got.Matched {
			t.Fatalf("第 %d 次应命中", i+1)
		}
	}
	if got, _ := m(ResourceRecord{Body: "x"}, nil); got.Matched {
		t.Fatal("判断次数用尽后应按不匹配")
	}
}
