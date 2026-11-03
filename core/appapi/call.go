package appapi

// 通用 JSON 分发：Wails 绑定与 api-server 共用。命令名见 §二 命令表。

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
)

type handler func(ctx context.Context, a *App, args json.RawMessage) (any, error)

// bind 把类型化方法包成 JSON 处理函数：args 为空或 null 时用零值参数。
func bind[In, Out any](f func(*App, context.Context, In) (Out, error)) handler {
	return func(ctx context.Context, a *App, raw json.RawMessage) (any, error) {
		var in In
		if t := bytes.TrimSpace(raw); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
			dec := json.NewDecoder(bytes.NewReader(t))
			if err := dec.Decode(&in); err != nil {
				return nil, Errorf(CodeBadRequest, "参数解析失败：%v", err)
			}
		}
		out, err := f(a, ctx, in)
		if err != nil {
			return nil, wrap(err)
		}
		return out, nil
	}
}

var commands = map[string]handler{
	"hello":             bind((*App).Hello),
	"sessions.list":     bind((*App).SessionsList),
	"session.open":      bind((*App).Open),
	"session.entries":   bind((*App).Entries),
	"session.send":      bind((*App).Send),
	"session.steer":     bind((*App).Steer),
	"session.cancel":    bind((*App).Cancel),
	"session.confirm":   bind((*App).Confirm),
	"session.answer":    bind((*App).Answer),
	"session.rewind":    bind((*App).Rewind),
	"session.compact":   bind((*App).Compact),
	"session.fork":      bind((*App).Fork),
	"session.stats":     bind((*App).Stats),
	"session.max_steps": bind((*App).MaxSteps),
	"mode.list":         bind((*App).ModeList),
	"mode.get":          bind((*App).ModeGet),
	"mode.set":          bind((*App).ModeSet),
	"mode.show":         bind((*App).ModeShow),
	"plugins.list":      bind((*App).PluginsList),
	"packages.list":     bind((*App).PackagesList),
	"plugins.set":       bind((*App).PluginsSet),
	"artifact.get":      bind((*App).ArtifactGet),
	"session.reply":     bind((*App).Reply),
	"trace.explain":     bind((*App).ExplainTrace),
}

// Commands 支持的命令名（排序后）。
func Commands() []string {
	out := make([]string, 0, len(commands))
	for k := range commands {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Call 按命令名分发：args 为该命令的 JSON 参数，返回值可直接 JSON 编码。
// 错误一律是 *Error；未知命令返回 unsupported。
func (a *App) Call(ctx context.Context, cmd string, args json.RawMessage) (any, error) {
	h, ok := commands[cmd]
	if !ok {
		return nil, Errorf(CodeUnsupported, "未知命令 %q", cmd)
	}
	return h(ctx, a, args)
}
