package toolhub

import (
	"context"
	"encoding/json"

	"ybase-kernel/core/session"
)

// Simple 构造只读、返回纯文本的工具：call 出错时作为工具错误返回给模型。
// 供各工具插件共用（插件之间不得互相 import）。
func Simple(name, desc, schema string, call func(context.Context, json.RawMessage) (string, error)) *Tool {
	return &Tool{
		Spec: session.ToolSpec{Name: name, Description: desc, Schema: json.RawMessage(schema)},
		Risk: Read,
		Call: func(ctx context.Context, _ *Cx, raw json.RawMessage) (Output, error) {
			s, err := call(ctx, raw)
			if err != nil {
				return Errorf("%v", err), nil
			}
			return Text(s), nil
		},
	}
}
