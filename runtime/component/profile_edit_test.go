package component

import (
	"testing"
)

func TestCheckConfigNormalizesInts(t *testing.T) {
	name := "zz-check-config"
	type cfg struct {
		N int     `cfg:"n"`
		F float64 `cfg:"f"`
	}
	registerOnce(name, func() Component { return metaOnly{Meta{Name: name, Config: &cfg{}}} })
	got, err := CheckConfig(name, map[string]any{"n": float64(3), "f": float64(2)})
	if err != nil || got["n"] != int64(3) || got["f"] != float64(2) {
		t.Fatalf("%#v %v", got, err)
	}
	if _, err := CheckConfig(name, map[string]any{"n": "x"}); err == nil {
		t.Fatal("类型不符应报错")
	}
	if _, err := CheckConfig(name, map[string]any{"zz": 1}); err == nil {
		t.Fatal("未知键应报错")
	}
	if _, err := CheckConfig("no-such-plugin", nil); err == nil {
		t.Fatal("未登记应报错")
	}
}

type metaOnly struct{ m Meta }

func (c metaOnly) Meta() Meta           { return c.m }
func (metaOnly) Setup(*Registrar) error { return nil }
func (metaOnly) Start(*Scope) error     { return nil }
