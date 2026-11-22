package appapi

// 交互回答的校验（10.6 schema）：只支持界面表单会生成的子集——type（object / string / number / integer / boolean / array）、
// properties + required、enum、minimum / maximum、minLength / maxLength、items、minItems / maxItems。
// 不认识的关键字忽略（宽进），认识的严格检查；错误带字段路径。

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

type jsonSchema struct {
	Type       any                    `json:"type"`
	Properties map[string]*jsonSchema `json:"properties"`
	Required   []string               `json:"required"`
	Enum       []any                  `json:"enum"`
	Minimum    *float64               `json:"minimum"`
	Maximum    *float64               `json:"maximum"`
	MinLength  *int                   `json:"minLength"`
	MaxLength  *int                   `json:"maxLength"`
	Items      *jsonSchema            `json:"items"`
	MinItems   *int                   `json:"minItems"`
	MaxItems   *int                   `json:"maxItems"`
}

// validateValue 按 schema 校验 value（JSON）；schema 为空时只要求 value 是合法 JSON。
func validateValue(schema, value json.RawMessage) error {
	var v any
	if err := json.Unmarshal(value, &v); err != nil {
		return fmt.Errorf("answer is not valid JSON: %v", err)
	}
	if len(schema) == 0 {
		return nil
	}
	var s jsonSchema
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("invalid interaction schema: %v", err)
	}
	return s.check("", v)
}

func (s *jsonSchema) types() []string {
	switch t := s.Type.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func typeOfJSON(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func (s *jsonSchema) check(path string, v any) error {
	at := func(format string, args ...any) error {
		p := path
		if p == "" {
			p = "answer"
		}
		return fmt.Errorf("%s: "+format, append([]any{p}, args...)...)
	}
	if ts := s.types(); len(ts) > 0 {
		got, ok := typeOfJSON(v), false
		for _, t := range ts {
			if t == got || (t == "number" && got == "integer") {
				ok = true
			}
		}
		if !ok {
			return at("expected %s, got %s", strings.Join(ts, " / "), got)
		}
	}
	if len(s.Enum) > 0 {
		hit := false
		for _, e := range s.Enum {
			if jsonEqual(e, v) {
				hit = true
			}
		}
		if !hit {
			return at("not one of the allowed values")
		}
	}
	switch x := v.(type) {
	case float64:
		if s.Minimum != nil && x < *s.Minimum {
			return at("must be >= %v", *s.Minimum)
		}
		if s.Maximum != nil && x > *s.Maximum {
			return at("must be <= %v", *s.Maximum)
		}
	case string:
		n := utf8.RuneCountInString(x)
		if s.MinLength != nil && n < *s.MinLength {
			return at("must be at least %d characters", *s.MinLength)
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			return at("must be at most %d characters", *s.MaxLength)
		}
	case []any:
		if s.MinItems != nil && len(x) < *s.MinItems {
			return at("select at least %d item(s)", *s.MinItems)
		}
		if s.MaxItems != nil && len(x) > *s.MaxItems {
			return at("select at most %d item(s)", *s.MaxItems)
		}
		if s.Items != nil {
			for i, e := range x {
				if err := s.Items.check(fmt.Sprintf("%s[%d]", path, i), e); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		for _, r := range s.Required {
			if val, ok := x[r]; !ok || val == nil || val == "" {
				return fmt.Errorf("%s is required", join(path, r))
			}
		}
		keys := make([]string, 0, len(s.Properties))
		for k := range s.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if val, ok := x[k]; ok && val != nil {
				if err := s.Properties[k].check(join(path, k), val); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}
