package openaichat

// 协议适配器 openai-chat（model-hub D2，review-1 G5）：OpenAI 兼容 /chat/completions SSE。
// 单次调用：不重试、不聚合、不计量。错误归一为稳定 code；恰好一个终止事件。
// 缓存用量归一：OpenAI prompt_tokens_details.cached_tokens、DeepSeek prompt_cache_hit_tokens → Usage.CacheRead；
// 厂商没报用量时 Source = Missing（不当作未命中）。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"ybase/runtime/modelhub"
	"ybase/runtime/session"
)

// OpenAIChat openai-chat 协议适配器。
type OpenAIChat struct {
	HTTP *http.Client
}

// Protocol 协议名。
func (OpenAIChat) Protocol() string { return "openai-chat" }

// Stream 单次流式调用。
func (a OpenAIChat) Stream(ctx context.Context, r *modelhub.Request, ep modelhub.Endpoint) <-chan modelhub.Event {
	auth := ""
	if ep.APIKey != "" {
		auth = "Bearer " + ep.APIKey
	}
	return modelhub.RunStream(func(out chan<- modelhub.Event) modelhub.Event {
		body, f := modelhub.PostSSE(ctx, modelhub.SSERequest{Client: a.HTTP, BaseURL: ep.BaseURL,
			Default: "https://api.openai.com/v1", Path: "/chat/completions", Body: openAIBody(r, ep),
			Header: map[string]string{"Authorization": auth}}, nil)
		if f != nil {
			return modelhub.Event{Failed: f}
		}
		defer body.Close()
		return parseOpenAI(ctx, body, ep.Model, ep.Compat.ReasoningField, out)
	})
}

type oaFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function oaFunc `json:"function"`
}

type oaMsg struct {
	Role       string   `json:"role"`
	Content    any      `json:"content"` // *string，或带图片的 user 消息用 []oaPart；nil = null
	ToolCalls  []oaCall `json:"tool_calls,omitempty"`
	ToolCallID string   `json:"tool_call_id,omitempty"`
}

// oaPart 多模态 user 消息的一段（text / image_url）。
type oaPart struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

type oaImageURL struct {
	URL string `json:"url"`
}

// oaUserParts 文本 + 图片（data URL 内嵌）组成的 user 消息内容。
func oaUserParts(text string, imgs []modelhub.Image) []oaPart {
	parts := make([]oaPart, 0, len(imgs)+1)
	if text != "" {
		parts = append(parts, oaPart{Type: "text", Text: text})
	}
	for _, im := range imgs {
		parts = append(parts, oaPart{Type: "image_url", ImageURL: &oaImageURL{URL: modelhub.DataURL(im)}})
	}
	return parts
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

func openAIBody(r *modelhub.Request, ep modelhub.Endpoint) map[string]any {
	req := r.Req
	var msgs []oaMsg
	if len(req.System) > 0 {
		parts := make([]string, 0, len(req.System))
		for _, s := range req.System {
			parts = append(parts, s.Text)
		}
		s := strings.Join(parts, "\n\n")
		msgs = append(msgs, oaMsg{Role: "system", Content: &s})
	}
	wire := map[string]string{}
	// tool 消息只收文本：工具结果里的图片攒起来，这一串 tool 消息结束后补一条 user 消息带上
	// （插在 tool 消息之间会破坏"tool 消息紧跟调用"的约束）。
	var toolImgs []modelhub.Image
	flushToolImgs := func() {
		if len(toolImgs) == 0 {
			return
		}
		msgs = append(msgs, oaMsg{Role: "user", Content: oaUserParts("（以上工具结果附带的图片）", toolImgs)})
		toolImgs = nil
	}
	for _, m := range req.Messages {
		t, imgs := modelhub.SplitContent(r, ep, m.Content)
		if m.Role != "tool" {
			flushToolImgs()
		}
		switch m.Role {
		case "assistant":
			wm := oaMsg{Role: "assistant"}
			if t != "" || len(m.ToolCalls) == 0 {
				wm.Content = &t
			}
			for _, tc := range m.ToolCalls {
				wid := tc.WireID
				if wid == "" {
					wid = tc.ID
				}
				wire[tc.ID] = wid
				args := string(tc.Args)
				if args == "" {
					args = "{}"
				}
				wm.ToolCalls = append(wm.ToolCalls, oaCall{ID: wid, Type: "function", Function: oaFunc{Name: tc.Name, Arguments: args}})
			}
			msgs = append(msgs, wm)
		case "tool":
			id := wire[m.ToolCallID]
			if id == "" {
				id = m.ToolCallID
			}
			if len(imgs) > 0 {
				t += fmt.Sprintf("\n[附 %d 张图片，见下一条消息]", len(imgs))
			}
			msgs = append(msgs, oaMsg{Role: "tool", ToolCallID: id, Content: &t})
			toolImgs = append(toolImgs, imgs...)
		default:
			if len(imgs) > 0 {
				msgs = append(msgs, oaMsg{Role: "user", Content: oaUserParts(t, imgs)})
			} else {
				msgs = append(msgs, oaMsg{Role: "user", Content: &t})
			}
		}
	}
	flushToolImgs()
	b := map[string]any{"model": ep.Model, "messages": msgs, "stream": true,
		"stream_options": map[string]any{"include_usage": true}}
	if len(req.Tools) > 0 {
		var tools []oaTool
		for _, t := range req.Tools {
			var wt oaTool
			wt.Type = "function"
			wt.Function.Name, wt.Function.Description, wt.Function.Parameters = t.Name, t.Description, t.Schema
			tools = append(tools, wt)
		}
		b["tools"] = tools
	}
	if req.Params.Temperature != nil {
		b["temperature"] = *req.Params.Temperature
	}
	if req.Params.MaxTokens != nil {
		f := ep.Compat.MaxTokensField
		if f == "" {
			f = "max_tokens"
		}
		b[f] = *req.Params.MaxTokens
	}
	if tc := chatToolChoice(req.Params.ToolChoice); tc != nil && len(req.Tools) > 0 {
		b["tool_choice"] = tc // 审查 P1-32：原先三家适配器都没映射
	}
	if req.Params.Reasoning != nil && *req.Params.Reasoning != "" {
		b["reasoning_effort"] = *req.Params.Reasoning
	}
	if ep.Compat.PromptCacheKey && r.SessionKey != "" {
		b["prompt_cache_key"] = r.SessionKey // D5：会话缓存亲和
	}
	for _, p := range req.Params.Extra {
		b[p.Name] = p.Value
	}
	return b
}

// chatToolChoice 映射 tool_choice：auto / none / required，或指定一个函数。空 = 不设（厂商缺省 auto）。
func chatToolChoice(tc *session.ToolChoice) any {
	if tc == nil {
		return nil
	}
	switch tc.Mode {
	case "auto", "none", "required":
		return tc.Mode
	case "tool":
		if tc.Name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
		}
	}
	return nil
}

// reasoningText 取一个推理增量：Compat.ReasoningField 指定字段时只认它；未指定时优先 reasoning_content，
// 没有再取 reasoning。两个字段都发的厂商不再把同一段推理拼两遍（审查 P1-32）。
func reasoningText(field, content, alt string) string {
	switch field {
	case "reasoning_content":
		return content
	case "reasoning":
		return alt
	}
	if content != "" {
		return content
	}
	return alt
}

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function oaFunc `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens         uint64 `json:"prompt_tokens"`
		CompletionTokens     uint64 `json:"completion_tokens"`
		PromptCacheHitTokens uint64 `json:"prompt_cache_hit_tokens"` // DeepSeek
		PromptTokensDetails  struct {
			CachedTokens uint64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

type oaAcc struct {
	id, name string
	args     strings.Builder
}

func parseOpenAI(ctx context.Context, r io.Reader, model, reasoningField string, out chan<- modelhub.Event) modelhub.Event {
	var (
		text, reasoning strings.Builder
		calls           = map[int]*oaAcc{}
		stop            session.StopReason
		usage           = session.Usage{Source: "Missing"}
		done            bool
	)
	build := func() session.AssistantItem {
		it := session.AssistantItem{Model: model, Usage: usage}
		if reasoning.Len() > 0 {
			s := reasoning.String()
			it.Reasoning = []session.ReasoningBlock{{Text: &s}}
		}
		if text.Len() > 0 {
			it.Content = session.Content{session.NewTextPart(text.String())}
		}
		idx := make([]int, 0, len(calls))
		for i := range calls {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		for _, i := range idx {
			c := calls[i]
			args := c.args.String()
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			it.ToolCalls = append(it.ToolCalls, session.ToolCall{WireID: c.id, Name: c.name, Args: json.RawMessage(args)})
		}
		return it
	}
	fail := func(code modelhub.Code, msg string) modelhub.Event {
		p := build()
		return modelhub.Event{Failed: &modelhub.Failure{Code: code, Message: msg, Partial: &p}}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			done = true
			break
		}
		var ck oaChunk
		if err := json.Unmarshal([]byte(data), &ck); err != nil {
			return fail(modelhub.CodeProtocol, "bad sse chunk: "+err.Error())
		}
		if ck.Error != nil {
			code := modelhub.CodeServer
			if strings.Contains(strings.ToLower(ck.Error.Message), "context") {
				code = modelhub.CodeContextOverflow
			}
			return fail(code, ck.Error.Message)
		}
		for _, ch := range ck.Choices {
			if ch.FinishReason != "" {
				stop = session.StopReason{V: ch.FinishReason}
			}
			d := ch.Delta
			if d.Content != "" {
				text.WriteString(d.Content)
				out <- modelhub.Event{TextDelta: d.Content}
			}
			if rd := reasoningText(reasoningField, d.ReasoningContent, d.Reasoning); rd != "" {
				reasoning.WriteString(rd)
				out <- modelhub.Event{ReasoningDelta: rd}
			}
			for _, tc := range d.ToolCalls {
				a := calls[tc.Index]
				if a == nil {
					a = &oaAcc{}
					calls[tc.Index] = a
				}
				if tc.ID != "" {
					a.id = tc.ID
				}
				if tc.Function.Name != "" {
					a.name = tc.Function.Name
				}
				a.args.WriteString(tc.Function.Arguments)
			}
			if len(d.ToolCalls) > 0 {
				out <- modelhub.Event{} // 心跳：长工具参数流期间不被判超时（P0-13）
			}
		}
		if u := ck.Usage; u != nil {
			cache := u.PromptTokensDetails.CachedTokens
			if u.PromptCacheHitTokens > cache {
				cache = u.PromptCacheHitTokens
			}
			usage = session.Usage{Input: u.PromptTokens, Output: u.CompletionTokens, CacheRead: cache, Source: "Reported"}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return fail(modelhub.CodeAborted, err.Error())
		}
		return fail(modelhub.CodeTransport, "read stream: "+err.Error())
	}
	if ctx.Err() != nil {
		return fail(modelhub.CodeAborted, ctx.Err().Error())
	}
	if !done && stop.V == "" {
		return fail(modelhub.CodeTransport, "stream closed without [DONE]")
	}
	it := build()
	if len(it.Content) == 0 && len(it.ToolCalls) == 0 {
		return fail(modelhub.CodeEmptyResponse, "空回复")
	}
	return modelhub.Event{Done: &modelhub.Done{Reply: it, Stop: stop}}
}
