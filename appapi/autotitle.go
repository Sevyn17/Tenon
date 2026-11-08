package appapi

// 会话自动标题：会话还没有标题时，用第一条用户消息兜底（取首个非空行，压掉多余空白，截到 AutoTitleRunes 个字符）。
// 只在标题为空时写，用户改过的标题不会被覆盖。

import (
	"context"
	"strings"
	"time"
	"unicode"

	"tenon/runtime/host"
	"tenon/runtime/session"
	"tenon/runtime/store"
)

// AutoTitleRunes 自动标题的最大字符数（超出加省略号）。
const AutoTitleRunes = 40

// TitleFromText 从一段用户输入取标题；取不到（全是空白）返回空串。
func TitleFromText(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.Join(strings.FieldsFunc(line, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
		if t == "" {
			continue
		}
		r := []rune(t)
		if len(r) > AutoTitleRunes {
			return strings.TrimSpace(string(r[:AutoTitleRunes])) + "…"
		}
		return t
	}
	return ""
}

// autoTitle 会话无标题时按这条输入设标题；失败不影响发送。
func (v *App) autoTitle(ctx context.Context, id string, c session.Content) {
	ms, ok := v.o.DB.(store.SessionMetaStore)
	if !ok {
		return
	}
	t := TitleFromText(textOf(c))
	if t == "" {
		return
	}
	l, err := host.LoadHead(ctx, v.o.DB, id)
	if err != nil || l.Meta.Title != "" {
		return
	}
	if ms.UpdateSessionMeta(ctx, id, &t, nil) != nil || v.o.Titler == nil {
		return
	}
	// 再让模型起一个短标题（后台，不挡发送）；只在标题仍是兜底值时替换，用户这期间改过就不动。
	model := ""
	if h := l.Snapshot.Header; h.Config != nil {
		model = h.Config.Model
	}
	input := clip(textOf(c), 4000)
	go func() {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), TitleTimeout)
		defer cancel()
		nt, err := v.o.Titler(bg, model, input)
		if nt = TitleFromText(nt); err != nil || nt == "" || nt == t {
			return
		}
		cur, err := host.LoadHead(bg, v.o.DB, id)
		if err != nil || cur.Meta.Title != t {
			return
		}
		_ = ms.UpdateSessionMeta(bg, id, &nt, nil)
	}()
}

// TitleTimeout 模型起标题最多等多久。
var TitleTimeout = 20 * time.Second
