package tenon

// 工具外观（计划/08 3.1）：应用写工具只用本包——参数用结构体描述（schema 自动生成），返回值自动编成 JSON 文本，
// WithTools 直接加进来，不必写组件、不必 import 内核包。要按会话决定显不显露、要读写组件服务的，仍然写组件。

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Tool 一个工具。用 Func 或 Raw 构造；修饰方法返回新值，不改原来的。
type Tool struct{ t *toolhub.Tool }

// Name 工具名。
func (t Tool) Name() string {
	if t.t == nil {
		return ""
	}
	return t.t.Spec.Name
}

func (t Tool) clone() Tool {
	c := *t.t
	return Tool{t: &c}
}

// Writes 标成"会改东西"：交给权限组件按档位判定（缺省档位下要确认），不和别的调用并行。
func (t Tool) Writes() Tool { c := t.clone(); c.t.Risk = toolhub.Write; c.t.NoAuth = false; return c }

// Dangerous 标成"危险"：权限插件按档位要人确认（缺省档位下也要）。
func (t Tool) Dangerous() Tool {
	c := t.clone()
	c.t.Risk = toolhub.Dangerous
	c.t.NoAuth = false
	return c
}

// Timeout 单次调用的超时（缺省见内核参数 tool_timeout）。
func (t Tool) Timeout(d time.Duration) Tool { c := t.clone(); c.t.Timeout = d; return c }

// Serial 不和同一步里的其他调用并行（只读工具缺省可以并行）。
func (t Tool) Serial() Tool { c := t.clone(); no := false; c.t.Parallel = &no; return c }

// Func 用一个函数做工具：参数解码成 T（JSON Schema 按 T 的字段生成），返回值按下面的规则交给模型：
// string 原样，[]byte 当文本，其余编成 JSON。fn 返回错误时工具失败，错误信息交给模型（不让回合失败）。
//
// T 的字段用 json tag 取名；desc tag 写说明，enum tag 写可选值（逗号分隔）；指针或带 omitempty 的字段可选，
// 其余必填。
func Func[T any](name, desc string, fn func(ctx context.Context, args T) (any, error)) Tool {
	schema := SchemaOf[T]()
	return Raw(name, desc, schema, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a T
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("参数不符合 schema：%v", err)
			}
		}
		return fn(ctx, a)
	})
}

// Raw 自己写 JSON Schema 的工具；fn 拿到原始参数。返回值规则同 Func。
func Raw(name, desc, schema string, fn func(ctx context.Context, args json.RawMessage) (any, error)) Tool {
	t := toolhub.Simple(name, desc, schema, nil)
	// 应用自己的只读工具是可信代码：缺省不经权限确认（没有权限项的工具权限组件一律询问）。
	// 标了 Writes / Dangerous 的交给权限组件判定。
	t.NoAuth = true
	t.Call = func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
		if cx != nil {
			ci := CallInfo{Session: cx.SessionID, CallID: cx.CallID}
			if c := cx.Ctx; c != nil {
				s := c.Session()
				ci.Agent, ci.User, ci.Tenant, ci.Meta = s.Definition, s.Creator, c.Tenant(), c.Meta()
			}
			ctx = context.WithValue(ctx, callInfoKey{}, ci)
		}
		out, err := fn(ctx, raw)
		if err != nil {
			return toolhub.Failf("%v", err), nil
		}
		s, err := toolText(out)
		if err != nil {
			return toolhub.Failf("工具结果编码失败：%v", err), nil
		}
		return toolhub.Text(s), nil
	}
	return Tool{t: t}
}

func toolText(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	case json.RawMessage:
		return string(x), nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// CallInfo 工具调用的上下文：哪个会话、哪一次调用。在 Func / Raw 的函数里用 CallInfoFrom(ctx) 取。
type CallInfo struct {
	Session string // 会话 id
	CallID  string // 这次调用的 id
	Agent   string // 会话绑定的定义名
	Tenant  string // 租户（单机为本地租户）
	User    string // 会话创建者（本机为空或 LocalUser）
	Meta    Meta   // 会话的业务 meta（NewSession / Ask 给的）
}

type callInfoKey struct{}

// CallInfoFrom 取当前工具调用的上下文；不在工具调用里时 ok 为 false。
func CallInfoFrom(ctx context.Context) (CallInfo, bool) {
	ci, ok := ctx.Value(callInfoKey{}).(CallInfo)
	return ci, ok
}

// WithTools 加应用自己的工具：每个会话都能用（智能体定义的 tools 白名单仍然生效）。可以多次给，按名字不能重复。
func WithTools(ts ...Tool) Option {
	return func(c *config) error {
		for _, t := range ts {
			if t.t == nil || t.t.Spec.Name == "" {
				return fmt.Errorf("%w: WithTools 里有空工具（用 Func / Raw 构造）", ErrInvalid)
			}
			c.tools = append(c.tools, t)
		}
		return nil
	}
}

// appTools 把 WithTools 的工具登记成一个组件。
type appTools struct{ tools []*toolhub.Tool }

const appToolsName = "app-tools"

func (appTools) Meta() component.Meta { return component.Meta{Name: appToolsName, Default: true} }

func (p appTools) Setup(cx *component.Registrar) error {
	tools := p.tools
	component.RegisterAt(cx, assemble.SessionTools, appToolsName, assemble.ToolsEntry{Order: 50,
		Tools: func() []*toolhub.Tool { return tools }})
	return nil
}

func (appTools) Start(*component.Scope) error { return nil }

// SchemaOf 按 T 生成 JSON Schema（Func 用；也可以拿来给 Raw）。T 不是结构体时给一个宽松的 object。
func SchemaOf[T any]() string {
	t := reflect.TypeOf((*T)(nil)).Elem()
	b, _ := json.Marshal(schemaOf(t, 0))
	return string(b)
}

func schemaOf(t reflect.Type, depth int) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if depth > 8 { // 递归类型：到这里就不再展开
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"}
		}
		return map[string]any{"type": "array", "items": schemaOf(t.Elem(), depth+1)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaOf(t.Elem(), depth+1)}
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			p := schemaOf(f.Type, depth+1)
			if d := f.Tag.Get("desc"); d != "" {
				p["description"] = d
			}
			if e := f.Tag.Get("enum"); e != "" {
				var vs []string
				for _, v := range strings.Split(e, ",") {
					if v = strings.TrimSpace(v); v != "" {
						vs = append(vs, v)
					}
				}
				p["enum"] = vs
			}
			props[name] = p
			if f.Type.Kind() != reflect.Pointer && !strings.Contains(opts, "omitempty") {
				required = append(required, name)
			}
		}
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	return map[string]any{"type": "object"}
}
