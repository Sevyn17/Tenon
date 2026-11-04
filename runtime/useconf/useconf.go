// Package useconf：使用配置链（docs/框架设计/06 - 配置.md 6.2、6.3，D8）。
//
// 使用配置 = "某个会话怎么用某组件"：组件缺省值 < 定义 [use.<组件>] < 租户配置 < 用户配置。
// 建会话时算好，存进会话行 use_config，之后不再变；组件通过 *Ctx 读取，自己不去翻文件。
//
// 限制类字段（预算、权限、工具白名单）不能靠覆盖，只能取严：limit:"min" 取各层最小值，
// limit:"intersect" 取各层交集。
//
// 本包只做"读"和"校验"，不认识谁装配了组件，也不知道组件的其它配置：字段表来自结构体原型。
package useconf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 取值错误。调用方用 errors.Is 判断。
var (
	ErrUnknownKey = errors.New("useconf: 未知配置项")
	ErrNotTenant  = errors.New("useconf: 该字段不允许租户/用户覆盖")
	ErrRequired   = errors.New("useconf: 缺少必填配置")
	ErrEnum       = errors.New("useconf: 取值不在枚举内")
)

// Field 一个使用配置项。
type Field struct {
	Key      string
	Type     reflect.Type
	Default  any
	Required bool
	Tenant   bool   // 允许租户/用户层覆盖
	Secret   bool   // 值不进日志、不进 explain
	Hot      bool   // 改了可以热生效（T3 用）
	Limit    string // "" / "min" / "intersect"
	Enum     []string
	index    int // 结构体字段序号
}

// Schema 一个组件登记的使用配置结构。
type Schema struct {
	Component string
	Fields    []Field
	typ       reflect.Type
}

// SchemaOf 从零值结构体指针（组件的 AgentConfig）读出字段表。键名优先 toml 标签，其次 cfg。
func SchemaOf(component string, proto any) (Schema, error) {
	rv := reflect.ValueOf(proto)
	if !rv.IsValid() || rv.Kind() != reflect.Ptr || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return Schema{}, fmt.Errorf("useconf: 组件 %s 的使用配置原型必须是结构体指针，给的是 %T", component, proto)
	}
	t := rv.Elem().Type()
	s := Schema{Component: component, typ: t}
	seen := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.PkgPath != "" {
			continue
		}
		key := sf.Tag.Get("toml")
		if key == "" {
			key = sf.Tag.Get("cfg")
		}
		if key == "" || key == "-" {
			continue
		}
		if seen[key] {
			return Schema{}, fmt.Errorf("useconf: 组件 %s 的 %q 重复", component, key)
		}
		seen[key] = true
		f := Field{
			Key:      key,
			Type:     sf.Type,
			Required: tagBool(sf, "required"),
			Tenant:   tagBool(sf, "tenant"),
			Secret:   tagBool(sf, "secret"),
			Hot:      tagBool(sf, "hot"),
			Limit:    sf.Tag.Get("limit"),
			index:    i,
		}
		switch f.Limit {
		case "", "min", "intersect":
		default:
			return Schema{}, fmt.Errorf("useconf: 组件 %s 的 %s 用了不认识的 limit=%q（只认 min / intersect）", component, key, f.Limit)
		}
		if e := sf.Tag.Get("enum"); e != "" {
			f.Enum = splitTrim(e)
		}
		if d, ok := sf.Tag.Lookup("default"); ok {
			v, err := convert(d, sf.Type, true)
			if err != nil {
				return Schema{}, fmt.Errorf("useconf: %s.%s 的 default：%w", component, key, err)
			}
			f.Default = v
		}
		s.Fields = append(s.Fields, f)
	}
	if len(s.Fields) == 0 {
		return Schema{}, fmt.Errorf("useconf: 组件 %s 的使用配置原型没有任何配置项", component)
	}
	return s, nil
}

// Field 按键查字段。
func (s Schema) Field(key string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Keys 全部键名，已排序（报错与 explain 用，保证同一份定义每次输出一致）。
func (s Schema) Keys() []string {
	out := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		out = append(out, f.Key)
	}
	sort.Strings(out)
	return out
}

// Source 租户层与用户层的配置来源（06 6.3）。没配就返回空表。
type Source interface {
	Tenant(ctx context.Context, tenant, component string) (map[string]any, error)
	User(ctx context.Context, tenant, user, component string) (map[string]any, error)
}

// Resolve 按链算出最终值；第二个返回值是每项的来源（default / definition / tenant / user，
// 取严的项按 "definition+tenant" 这样连起来）。
func Resolve(ctx context.Context, s Schema, def map[string]any, src Source, tenant, user string) (map[string]any, map[string]string, error) {
	out := make(map[string]any, len(s.Fields))
	from := make(map[string]string, len(s.Fields))
	for _, f := range s.Fields {
		if f.Default != nil {
			out[f.Key] = f.Default
			from[f.Key] = "default"
		}
	}

	type layer struct {
		name string
		vals map[string]any
	}
	layers := []layer{{name: "definition", vals: def}}
	if src != nil {
		tv, err := src.Tenant(ctx, tenant, s.Component)
		if err != nil {
			return nil, nil, err
		}
		layers = append(layers, layer{name: "tenant", vals: tv})
		if user != "" {
			uv, err := src.User(ctx, tenant, user, s.Component)
			if err != nil {
				return nil, nil, err
			}
			layers = append(layers, layer{name: "user", vals: uv})
		}
	}

	for _, l := range layers {
		if len(l.vals) == 0 {
			continue
		}
		keys := make([]string, 0, len(l.vals))
		for k := range l.vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			f, ok := s.Field(key)
			if !ok {
				return nil, nil, fmt.Errorf("%w：%s.%s（层 %s）", ErrUnknownKey, s.Component, key, l.name)
			}
			if l.name != "definition" && !f.Tenant {
				return nil, nil, fmt.Errorf("%w：%s.%s（层 %s）", ErrNotTenant, s.Component, key, l.name)
			}
			v, err := convert(l.vals[key], f.Type, false)
			if err != nil {
				return nil, nil, fmt.Errorf("useconf: %s.%s（层 %s）：%w", s.Component, key, l.name, err)
			}
			prev, has := out[key]
			if f.Limit != "" && has && prev != nil {
				tv, err := tighten(f.Limit, prev, v)
				if err != nil {
					return nil, nil, fmt.Errorf("useconf: %s.%s（层 %s）：%w", s.Component, key, l.name, err)
				}
				out[key] = tv
				from[key] = joinSource(from[key], l.name)
				continue
			}
			out[key] = v
			from[key] = l.name
		}
	}

	for _, f := range s.Fields {
		if f.Required && out[f.Key] == nil {
			return nil, nil, fmt.Errorf("%w：%s.%s", ErrRequired, s.Component, f.Key)
		}
		if err := checkEnum(f, out[f.Key]); err != nil {
			return nil, nil, err
		}
	}
	return out, from, nil
}

// Apply 把算好的值填进组件自己的结构体（*Ctx.Config 用）。
func (s Schema) Apply(proto any, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	rv := reflect.ValueOf(proto)
	if !rv.IsValid() || rv.Kind() != reflect.Ptr || rv.IsNil() || rv.Elem().Type() != s.typ {
		return fmt.Errorf("useconf: 组件 %s 的配置目标类型不对，给的是 %T", s.Component, proto)
	}
	elem := rv.Elem()
	for key, raw := range values {
		f, ok := s.Field(key)
		if !ok {
			return fmt.Errorf("%w：%s.%s", ErrUnknownKey, s.Component, key)
		}
		v, err := convert(raw, f.Type, false)
		if err != nil {
			return fmt.Errorf("useconf: %s.%s：%w", s.Component, key, err)
		}
		if v == nil {
			continue
		}
		elem.Field(f.index).Set(reflect.ValueOf(v))
	}
	return nil
}

// Encode 把算好的值编成 use_config 里该组件的一段：`{"<组件>": {...}}`。
func Encode(component string, values map[string]any) (map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	return map[string]any{component: values}, nil
}

// ResolveAll 把一个定义上的 `[use.*]` 逐组件算成该会话的使用配置（06 6.3）。
//
// schemas 是装配期各组件登记的使用配置原型；use 是定义里的 `use.<组件>` 表。
// 定义里出现没登记过的组件算装配期错误，直接报。
// 返回值：组件名 → 算好的配置（只有确实有配置的组件才出现）。
func ResolveAll(ctx context.Context, schemas []Schema, use map[string]map[string]any, src Source, tenant, user string) (map[string]map[string]any, error) {
	known := make(map[string]Schema, len(schemas))
	for _, s := range schemas {
		known[s.Component] = s
	}
	out := make(map[string]map[string]any, len(use))
	for name, def := range use {
		s, ok := known[name]
		if !ok {
			return nil, fmt.Errorf("定义里写了 [use.%s]，但没有组件登记这份使用配置", name)
		}
		values, _, err := Resolve(ctx, s, def, src, tenant, user)
		if err != nil {
			return nil, fmt.Errorf("[use.%s]：%w", name, err)
		}
		if len(values) == 0 {
			continue
		}
		out[name] = values
	}
	return out, nil
}

// EncodeAll 把算好的使用配置编成会话行的 use_config（`{"<组件>": {...}}`）。
// 空配置返回 nil，写库时当"没有使用配置"。
func EncodeAll(values map[string]map[string]any) (json.RawMessage, error) {
	flat := make(map[string]any, len(values))
	for name, v := range values {
		if len(v) == 0 {
			continue
		}
		flat[name] = v
	}
	if len(flat) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(flat)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// joinSource 记"这一项由哪几层取严得到"。
func joinSource(prev, name string) string {
	if prev == "" || prev == "default" {
		return name
	}
	for _, p := range strings.Split(prev, "+") {
		if p == name {
			return prev
		}
	}
	return prev + "+" + name
}

// tighten 按 limit 取严。
func tighten(limit string, prev, next any) (any, error) {
	switch limit {
	case "min":
		p, ok1 := numberOf(prev)
		n, ok2 := numberOf(next)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("limit=min 只用于数值字段")
		}
		if n < p {
			return next, nil
		}
		return prev, nil
	case "intersect":
		p, ok1 := stringSetOf(prev)
		n, ok2 := stringSetOf(next)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("limit=intersect 只用于字符串列表字段")
		}
		keep := make([]string, 0, len(p))
		for _, s := range p {
			for _, t := range n {
				if s == t {
					keep = append(keep, s)
					break
				}
			}
		}
		return keep, nil
	}
	return next, nil
}

func numberOf(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func stringSetOf(v any) ([]string, bool) {
	switch s := v.(type) {
	case []string:
		return s, true
	case []any:
		out := make([]string, 0, len(s))
		for _, it := range s {
			str, ok := it.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	case string:
		return splitTrim(s), true
	}
	return nil, false
}

func checkEnum(f Field, v any) error {
	if len(f.Enum) == 0 || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	for _, e := range f.Enum {
		if e == s {
			return nil
		}
	}
	return fmt.Errorf("%w：%s = %q，可选 %s", ErrEnum, f.Key, s, strings.Join(f.Enum, " / "))
}

// convert 把 TOML / JSON 解出来的值换成字段类型。literal 为 true 表示来自 struct tag 的字符串字面量。
func convert(val any, t reflect.Type, literal bool) (any, error) {
	if val == nil {
		return nil, nil
	}
	if literal {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("默认值必须是字符串字面量，给的是 %T", val)
		}
		switch {
		case t.Kind() == reflect.String:
			return s, nil
		case t == reflect.TypeOf(time.Duration(0)):
			val = s
		case t.Kind() == reflect.Slice || t.Kind() == reflect.Array:
			val = s // "a,b" 形式在下面按列表拆
		default:
			// 数字、布尔：default:"5" 当 JSON 字面量直接解析。
			val = json.RawMessage(s)
		}
	}
	// 时长写 "720h" 这种文本：TOML 里也是文本，两种来源都按 time.ParseDuration 解析。
	if t == reflect.TypeOf(time.Duration(0)) {
		if s, ok := val.(string); ok {
			d, err := time.ParseDuration(s)
			if err != nil {
				return nil, err
			}
			return d, nil
		}
	}
	// 字符串转列表（"a,b"）单独处理。
	if s, ok := val.(string); ok && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
		parts := splitTrim(s)
		out := reflect.MakeSlice(reflect.SliceOf(t.Elem()), 0, len(parts))
		for _, p := range parts {
			pv := reflect.New(t.Elem()).Elem()
			if err := json.Unmarshal([]byte(strconv.Quote(p)), pv.Addr().Interface()); err != nil {
				return nil, err
			}
			out = reflect.Append(out, pv)
		}
		return out.Interface(), nil
	}
	b, err := json.Marshal(val)
	if err != nil {
		return nil, err
	}
	p := reflect.New(t)
	if err := json.Unmarshal(b, p.Interface()); err != nil {
		return nil, fmt.Errorf("类型不符（期望 %s）：%w", t, err)
	}
	return p.Elem().Interface(), nil
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func tagBool(sf reflect.StructField, name string) bool {
	switch sf.Tag.Get(name) {
	case "true", "1", "yes":
		return true
	}
	return false
}
