package plugin

// 插件配置（plugin-manager D2）：配置 = Go struct + tag，是唯一来源。
//
//	type Cfg struct {
//	    Threshold float64 `cfg:"threshold" default:"0.85" title:"触发阈值" group:"压缩" widget:"slider" override:"agent"`
//	}
//
// tag：cfg = 键名；default = 默认值；title / description / group / widget = UI 注解（frontend D4 预留）；
// override = "agent" 表示可在智能体层覆盖，缺省不可覆盖。

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Field 一个配置项的 Schema。
type Field struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Group       string `json:"group,omitempty"`
	Widget      string `json:"widget,omitempty"`
	Override    bool   `json:"override,omitempty"`
}

// SchemaOf 读配置结构体的 Schema；proto 为 nil 返回 nil。
func SchemaOf(proto any) []Field {
	t := structType(proto)
	if t == nil {
		return nil
	}
	var out []Field
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Tag.Get("cfg")
		if key == "" || !f.IsExported() {
			continue
		}
		out = append(out, Field{Key: key, Type: f.Type.Kind().String(), Default: f.Tag.Get("default"),
			Title: f.Tag.Get("title"), Description: f.Tag.Get("description"), Group: f.Tag.Get("group"),
			Widget: f.Tag.Get("widget"), Override: f.Tag.Get("override") == "agent"})
	}
	return out
}

func structType(proto any) reflect.Type {
	if proto == nil {
		return nil
	}
	t := reflect.TypeOf(proto)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	return t
}

// decodeConfig 用默认值填一个新的配置实例，再用 profile 给出的值覆盖；返回各键来源。
// 未知键报错（防拼写错误静默失效）。
func decodeConfig(proto any, given map[string]any) (any, map[string]string, error) {
	t := structType(proto)
	src := map[string]string{}
	if t == nil {
		if len(given) > 0 {
			return nil, nil, fmt.Errorf("插件无配置项，却给了 %d 个", len(given))
		}
		return nil, src, nil
	}
	v := reflect.New(t)
	known := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Tag.Get("cfg")
		if key == "" || !f.IsExported() {
			continue
		}
		known[key] = i
		if d := f.Tag.Get("default"); d != "" {
			if err := setField(v.Elem().Field(i), d, true); err != nil {
				return nil, nil, fmt.Errorf("%s 默认值: %w", key, err)
			}
			src[key] = "default"
		}
	}
	keys := make([]string, 0, len(given))
	for k := range given {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		i, ok := known[k]
		if !ok {
			return nil, nil, fmt.Errorf("未知配置项 %q", k)
		}
		if err := setField(v.Elem().Field(i), given[k], false); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", k, err)
		}
		src[k] = "profile"
	}
	return v.Interface(), src, nil
}

// setField：default 是字符串字面量（按字段类型 JSON 解析，字符串字段直接用）；profile 值经 JSON 往返转换。
func setField(f reflect.Value, val any, literal bool) error {
	var b []byte
	if s, ok := val.(string); ok && literal {
		if f.Kind() == reflect.String {
			f.SetString(s)
			return nil
		}
		b = []byte(s)
	} else {
		var err error
		if b, err = json.Marshal(val); err != nil {
			return err
		}
	}
	p := reflect.New(f.Type())
	if err := json.Unmarshal(b, p.Interface()); err != nil {
		return fmt.Errorf("类型不符（期望 %s）: %w", f.Type(), err)
	}
	f.Set(p.Elem())
	return nil
}
