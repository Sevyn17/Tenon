package useconf

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// 06 6.2 的例子：记忆组件的使用配置。
type memoryUse struct {
	Store string        `toml:"store" required:"true"`
	By    string        `toml:"by" enum:"user,tenant,session" default:"user"`
	TTL   time.Duration `toml:"ttl" default:"720h"`
	Top   int           `toml:"top" default:"5" tenant:"true" limit:"min"`
	Tools []string      `toml:"tools" tenant:"true" limit:"intersect"`
}

type fakeSource struct {
	tenant map[string]any
	user   map[string]any
	err    error
}

func (f *fakeSource) Tenant(ctx context.Context, tenant, component string) (map[string]any, error) {
	return f.tenant, f.err
}

func (f *fakeSource) User(ctx context.Context, tenant, user, component string) (map[string]any, error) {
	return f.user, f.err
}

func schemaOf(t *testing.T) Schema {
	t.Helper()
	s, err := SchemaOf("memory", &memoryUse{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	return s
}

func TestSchemaFromTags(t *testing.T) {
	s := schemaOf(t)
	if got := s.Keys(); len(got) != 5 || got[0] != "by" || got[4] != "ttl" {
		t.Fatalf("键（应按名字排序）：%v", got)
	}
	by, _ := s.Field("by")
	if len(by.Enum) != 3 || by.Enum[0] != "user" || by.Default != "user" {
		t.Fatalf("by：%+v", by)
	}
	top, _ := s.Field("top")
	if !top.Tenant || top.Limit != "min" || top.Default != 5 {
		t.Fatalf("top：%+v", top)
	}
	ttl, _ := s.Field("ttl")
	if ttl.Default != 720*time.Hour {
		t.Fatalf("ttl 默认值：%v", ttl.Default)
	}
	if _, ok := s.Field("nope"); ok {
		t.Fatal("不该有 nope")
	}
}

func TestSchemaRejectsBadProto(t *testing.T) {
	if _, err := SchemaOf("x", 3); err == nil {
		t.Fatal("非结构体应当报错")
	}
	type dup struct {
		A int `toml:"k"`
		B int `toml:"k"`
	}
	if _, err := SchemaOf("x", dup{}); err == nil {
		t.Fatal("键重复应当报错")
	}
	type badLimit struct {
		A int `toml:"a" limit:"max"`
	}
	if _, err := SchemaOf("x", badLimit{}); err == nil {
		t.Fatal("limit 只认 min / intersect")
	}
}

func TestResolveDefaultsOnly(t *testing.T) {
	out, from, err := Resolve(context.Background(), schemaOf(t), map[string]any{"store": "memlib"}, nil, "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out["store"] != "memlib" || out["by"] != "user" || out["top"] != 5 || out["ttl"] != 720*time.Hour {
		t.Fatalf("合并结果：%v", out)
	}
	if from["store"] != "definition" || from["by"] != "default" {
		t.Fatalf("来源：%v", from)
	}
	if _, ok := out["tools"]; ok {
		t.Fatal("没有默认值也没有层值的键不应出现")
	}
}

func TestResolveLayerOrder(t *testing.T) {
	src := &fakeSource{
		tenant: map[string]any{"top": 10},
		user:   map[string]any{"top": 2},
	}
	def := map[string]any{"store": "memlib", "top": 50}
	out, from, err := Resolve(context.Background(), schemaOf(t), def, src, "local", "u42")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// limit:"min" 取严：50 / 10 / 2 取 2；来源记成多层取严。
	if out["top"] != 2 {
		t.Fatalf("top 应取最小值 2：%v", out["top"])
	}
	if from["top"] != "definition+tenant+user" {
		t.Fatalf("来源：%q", from["top"])
	}
}

func TestResolveLimitIntersect(t *testing.T) {
	src := &fakeSource{tenant: map[string]any{"tools": []any{"a", "b"}}}
	def := map[string]any{"store": "s", "tools": "b,c"}
	out, _, err := Resolve(context.Background(), schemaOf(t), def, src, "local", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got, _ := out["tools"].([]string)
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("交集应为 [b]：%v（%T）", out["tools"], out["tools"])
	}
	// 交集为空时给空列表而不是 nil，方便直接存进 JSON。
	src2 := &fakeSource{tenant: map[string]any{"tools": []any{"z"}}}
	out2, _, err := Resolve(context.Background(), schemaOf(t), def, src2, "local", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got2, ok := out2["tools"].([]string); !ok || len(got2) != 0 {
		t.Fatalf("空交集应为空列表：%v（%T）", out2["tools"], out2["tools"])
	}
}

func TestResolveErrors(t *testing.T) {
	s := schemaOf(t)
	cases := []struct {
		name string
		def  map[string]any
		src  *fakeSource
		want error
	}{
		{"定义里写了未知键", map[string]any{"store": "s", "stroe": "x"}, nil, ErrUnknownKey},
		{"租户层未知键", map[string]any{"store": "s"}, &fakeSource{tenant: map[string]any{"x": 1}}, ErrUnknownKey},
		{"租户层覆盖不允许的键", map[string]any{"store": "s"}, &fakeSource{tenant: map[string]any{"by": "tenant"}}, ErrNotTenant},
		{"用户层覆盖不允许的键", map[string]any{"store": "s"}, &fakeSource{user: map[string]any{"by": "tenant"}}, ErrNotTenant},
		{"必填缺失", map[string]any{}, nil, ErrRequired},
		{"枚举越界", map[string]any{"store": "s", "by": "week"}, nil, ErrEnum},
	}
	for _, c := range cases {
		var src Source
		if c.src != nil {
			src = c.src
		}
		if _, _, err := Resolve(context.Background(), s, c.def, src, "local", "u1"); !errors.Is(err, c.want) {
			t.Fatalf("%s：want %v, got %v", c.name, c.want, err)
		}
	}
}

func TestResolveSourceError(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := Resolve(context.Background(), schemaOf(t), map[string]any{"store": "s"}, &fakeSource{err: boom}, "local", "u1")
	if !errors.Is(err, boom) {
		t.Fatalf("来源错误应透出：%v", err)
	}
}

func TestApply(t *testing.T) {
	s := schemaOf(t)
	vals := map[string]any{"store": "memlib", "by": "tenant", "top": 3, "ttl": "48h", "tools": []any{"a", "b"}}
	var got memoryUse
	if err := s.Apply(&got, vals); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.Store != "memlib" || got.By != "tenant" || got.Top != 3 || got.TTL != 48*time.Hour {
		t.Fatalf("解出来：%+v", got)
	}
	if len(got.Tools) != 2 || got.Tools[0] != "a" {
		t.Fatalf("tools：%v", got.Tools)
	}
	if err := s.Apply(nil, map[string]any{"store": "x"}); err == nil {
		t.Fatal("没有使用配置的组件给了键应报错")
	}
	if err := s.Apply(&got, map[string]any{"nope": 1}); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("未知键：%v", err)
	}
}

// 现有组件用的是 cfg 标签，过渡期两者都认（06 6.2）。
func TestSchemaFallsBackToCfgTag(t *testing.T) {
	type legacy struct {
		Mode string `cfg:"mode" default:"ask" enum:"ask,allow"`
	}
	s, err := SchemaOf("permission", &legacy{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	f, ok := s.Field("mode")
	if !ok || f.Default != "ask" || len(f.Enum) != 2 {
		t.Fatalf("cfg 标签没认出来：%+v", f)
	}
}

func TestEncode(t *testing.T) {
	got, err := Encode("memory", map[string]any{"store": "s"})
	if err != nil || got["memory"] == nil {
		t.Fatalf("Encode: %v %v", got, err)
	}
	if got, _ := Encode("memory", nil); got != nil {
		t.Fatalf("空配置应返回 nil：%v", got)
	}
}

func TestResolveAll(t *testing.T) {
	s := schemaOf(t)
	other, err := SchemaOf("budget", &struct {
		Tokens int `toml:"tokens" tenant:"true" limit:"min"`
	}{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	use := map[string]map[string]any{
		"memory": {"store": "memlib"},
		"budget": {"tokens": 5},
	}
	src := mapSource{"memory": {"top": 3}, "budget": {"tokens": 9}}
	out, err := ResolveAll(context.Background(), []Schema{s, other}, use, src, "local", "")
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}
	if out["memory"]["store"] != "memlib" || out["memory"]["top"] != 3 {
		t.Fatalf("memory：%v", out["memory"])
	}
	if out["budget"]["tokens"] != 5 {
		t.Fatalf("budget 应取严为 5：%v", out["budget"])
	}
	// 定义里写了没登记的组件：装配期就该报。
	if _, err := ResolveAll(context.Background(), []Schema{s}, map[string]map[string]any{"nope": {"x": 1}}, nil, "local", ""); err == nil {
		t.Fatal("未登记的组件应当报错")
	}
}

func TestEncodeAll(t *testing.T) {
	raw, err := EncodeAll(map[string]map[string]any{"memory": {"store": "s"}, "empty": {}})
	if err != nil {
		t.Fatalf("EncodeAll: %v", err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解回来：%v（%s）", err, raw)
	}
	if got["memory"]["store"] != "s" {
		t.Fatalf("结果：%s", raw)
	}
	if _, ok := got["empty"]; ok {
		t.Fatalf("空配置不该编进去：%s", raw)
	}
	if raw, _ := EncodeAll(nil); raw != nil {
		t.Fatalf("没有配置应返回 nil：%s", raw)
	}
}

// mapSource 按组件名给租户层配置；ResolveAll 一次算多个组件时用来验证互不串味。
type mapSource map[string]map[string]any

func (m mapSource) Tenant(ctx context.Context, tenant, component string) (map[string]any, error) {
	return m[component], nil
}

func (m mapSource) User(ctx context.Context, tenant, user, component string) (map[string]any, error) {
	return nil, nil
}
