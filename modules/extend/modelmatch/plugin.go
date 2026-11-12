// Package modelmatch 登记「模型判断类」匹配器 llm：把匹配问题交给模型回答，
// 用于不适合写死规则的判定（框架设计 04 4.7，T5c）。
package modelmatch

// 模型判断类匹配器组件（框架设计 04 4.7，T5c）：登记一个匹配器 `llm`，用模型判断
// 「这条记录是否符合订阅参数里的 question」。判断失败按不匹配，不阻塞扇出；判断次数上限本地兜底。

import (
	"context"
	"strings"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
)

// ServiceName 组件名。
const ServiceName = "modelmatch"

// MatcherName 登记的匹配器名。订阅里 Matcher: "llm"，Params: {"question": "..."}。
const MatcherName = "llm"

// MaxJudgments 单个匹配器实例的判断次数上限（本地兜底；真正的预算账本 v1 后）。
const MaxJudgments = 1000

func init() { component.Register(ServiceName, func() component.Component { return &plug{} }) }

type plug struct {
	m *assemble.Models
	h *host.Host
}

func (*plug) Meta() component.Meta {
	return component.Meta{Name: ServiceName, Version: "0.1.0",
		Requires: []string{"?" + assemble.ServiceModelhub, "?" + assemble.ServiceHost}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	if m, ok, err := component.UseOptional[*assemble.Models](cx, assemble.ServiceModelhub); err == nil && ok {
		p.m = m
	}
	if h, ok, err := component.UseOptional[*host.Host](cx, assemble.ServiceHost); err == nil && ok {
		p.h = h
	}
	judge := func(ctx context.Context, rec component.ResourceRecord, params map[string]string) (bool, error) {
		if p.m == nil || p.m.Hub == nil {
			return false, nil
		}
		q := strings.TrimSpace(params["question"])
		if q == "" {
			return false, nil
		}
		// 构造一次最简判断请求：让模型只回 yes / no。
		req := session.Request{Model: p.m.Model, Messages: []session.Message{{
			Role: "user",
			Content: session.Content{session.NewTextPart(
				"判断下面这条记录是否符合条件，只回答 yes 或 no。\n条件：" + q + "\n记录：" + rec.Body)},
		}}}
		done, fail := p.m.Hub.SideCall(ctx, p.m.Model, req)
		if fail != nil || done == nil {
			return false, nil // 判断失败按不匹配
		}
		var b strings.Builder
		for _, part := range done.Reply.Content {
			b.WriteString(part.Text)
		}
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.String())), "yes"), nil
	}
	// 判断次数用尽时记一条 guard.denied 类事件（T5c：预算/次数用尽要可见）。
	onExhausted := func() {
		if p.h != nil {
			p.h.Publish("", "guard.denied", map[string]any{"matcher": MatcherName, "reason": "judgments_exhausted"})
		}
	}
	if err := cx.Matcher(MatcherName, component.ModelMatcher(judge, MaxJudgments, onExhausted)); err != nil {
		return err
	}
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
