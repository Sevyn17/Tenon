package anthropic

// anthropic-messages 协议适配器（model-hub D2）：/v1/messages SSE。
//
//   - 系统提示分段映射为 system 块，SystemBlock.Breakpoint → cache_control（缓存断点）。
//   - 连续的工具结果合成一条 user 消息（tool_result 块）。
//   - 推理块：thinking + signature 原样保留在 ReasoningBlock.Opaque，回传给同协议（思考只读）。
//   - 用量归一（G5）：cache_read_input_tokens → CacheRead，cache_creation_input_tokens → CacheWrite。
//   - 恰好一个终止事件；失败归一为稳定 code。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ybase-kernel/core/modelhub"
	"ybase-kernel/core/session"
)

// AnthropicVersion 请求头 anthropic-version。
const AnthropicVersion = "2023-06-01"

// DefaultAnthropicMaxTokens max_tokens 为必填，未给时的默认值。
const DefaultAnthropicMaxTokens = 8192

// Anthropic anthropic-messages 协议适配器。
type Anthropic struct {
	HTTP *http.Client
}

// Protocol 协议名。
func (Anthropic) Protocol() string { return "anthropic-messages" }

// Stream 单次流式调用。
func (a Anthropic) Stream(ctx context.Context, r *modelhub.Request, ep modelhub.Endpoint) <-chan modelhub.Event {
	out := make(chan modelhub.Event, 32)
	go func() {
		defer close(out)
		out <- a.run(ctx, r, ep, out)
	}()
	return out
}

func (a Anthropic) run(ctx context.Context, r *modelhub.Request, ep modelhub.Endpoint, out chan<- modelhub.Event) modelhub.Event {
	body, err := json.Marshal(anthropicBody(r, ep))
	if err != nil {
		return modelhub.Event{Failed: &modelhub.Failure{Code: modelhub.CodeInvalidRequest, Message: err.Error()}}
	}
	base := ep.BaseURL
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/messages", bytes.NewReader(body))
	if err != nil {
		return modelhub.Event{Failed: &modelhub.Failure{Code: modelhub.CodeInvalidRequest, Message: err.Error()}}
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	hreq.Header.Set("anthropic-version", AnthropicVersion)
	if ep.APIKey != "" {
		hreq.Header.Set("x-api-key", ep.APIKey)
	}
	hc := a.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return modelhub.Event{Failed: &modelhub.Failure{Code: modelhub.CodeAborted, Message: err.Error()}}
		}
		return modelhub.Event{Failed: &modelhub.Failure{Code: modelhub.CodeTransport, Message: err.Error()}}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		f := modelhub.HTTPFailure(resp.StatusCode, resp.Header, b)
		if resp.StatusCode == 529 { // overloaded
			f.Code = modelhub.CodeServer
		}
		return modelhub.Event{Failed: f}
	}
	return parseAnthropic(ctx, resp.Body, ep.Model, out)
}

// —— 请求 ——//

type anBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Thinking     *string         `json:"thinking,omitempty"` // 指针：thinking 块即使为空串也要带上该字段
	Signature    string          `json:"signature,omitempty"`
	CacheControl *anCache        `json:"cache_control,omitempty"`
}

type anCache struct {
	Type string `json:"type"`
}

type anMsg struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

// anthropicOpaque 推理块的不透明部分（签名），原样回传。
type anthropicOpaque struct {
	Protocol  string `json:"protocol"`
	Signature string `json:"signature,omitempty"`
}

func anthropicBody(r *modelhub.Request, ep modelhub.Endpoint) map[string]any {
	req := r.Req
	var system []anBlock
	for _, s := range req.System {
		if s.Text == "" {
			continue
		}
		b := anBlock{Type: "text", Text: s.Text}
		if s.Breakpoint {
			b.CacheControl = &anCache{Type: "ephemeral"}
		}
		system = append(system, b)
	}
	wire := map[string]string{}
	var msgs []anMsg
	push := func(role string, blocks ...anBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, anMsg{Role: role, Content: blocks})
	}
	for _, m := range req.Messages {
		t := modelhub.ContentString(m.Content)
		switch m.Role {
		case "assistant":
			var bs []anBlock
			for _, rb := range m.Reasoning {
				var op anthropicOpaque
				if b, err := json.Marshal(rb.Opaque); err == nil {
					_ = json.Unmarshal(b, &op)
				}
				if op.Protocol != "anthropic-messages" || rb.Text == nil {
					continue // 只回传同协议的思考（回放状态只在同一适配器内流动）
				}
				// 签名为空也回传：DeepSeek 的 Anthropic 端点不给签名，但工具调用回合要求带回 thinking 块。
				bs = append(bs, anBlock{Type: "thinking", Thinking: rb.Text, Signature: op.Signature})
			}
			if t != "" {
				bs = append(bs, anBlock{Type: "text", Text: t})
			}
			for _, tc := range m.ToolCalls {
				wid := tc.WireID
				if wid == "" {
					wid = tc.ID
				}
				wire[tc.ID] = wid
				args := tc.Args
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				bs = append(bs, anBlock{Type: "tool_use", ID: wid, Name: tc.Name, Input: args})
			}
			push("assistant", bs...)
		case "tool":
			id := wire[m.ToolCallID]
			if id == "" {
				id = m.ToolCallID
			}
			push("user", anBlock{Type: "tool_result", ToolUseID: id, Content: t})
		default:
			push("user", anBlock{Type: "text", Text: t})
		}
	}
	// 最后一条 user 消息打缓存断点：下一步请求能命中整段历史前缀。
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		bs := msgs[n-1].Content
		bs[len(bs)-1].CacheControl = &anCache{Type: "ephemeral"}
	}
	maxTok := uint64(DefaultAnthropicMaxTokens)
	if req.Params.MaxTokens != nil {
		maxTok = *req.Params.MaxTokens
	}
	b := map[string]any{"model": ep.Model, "messages": msgs, "stream": true, "max_tokens": maxTok}
	if len(system) > 0 {
		b["system"] = system
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tm := map[string]any{"name": t.Name, "input_schema": json.RawMessage(t.Schema)}
			if len(t.Schema) == 0 {
				tm["input_schema"] = json.RawMessage(`{"type":"object"}`)
			}
			if t.Description != "" {
				tm["description"] = t.Description
			}
			tools = append(tools, tm)
		}
		b["tools"] = tools
	}
	if req.Params.Temperature != nil {
		b["temperature"] = *req.Params.Temperature
	}
	for _, p := range req.Params.Extra {
		b[p.Name] = p.Value
	}
	return b
}

// —— 响应 ——//

type anEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage anUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anUsage struct {
	InputTokens              uint64 `json:"input_tokens"`
	OutputTokens             uint64 `json:"output_tokens"`
	CacheReadInputTokens     uint64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens"`
}

type anAcc struct {
	kind, id, name string
	buf            strings.Builder
	sig            string
}

func parseAnthropic(ctx context.Context, r io.Reader, model string, out chan<- modelhub.Event) modelhub.Event {
	var (
		blocks  = map[int]*anAcc{}
		order   []int
		stop    session.StopReason
		usage   = session.Usage{Source: "Missing"}
		stopped bool
	)
	build := func() session.AssistantItem {
		it := session.AssistantItem{Model: model, Usage: usage}
		var text strings.Builder
		for _, i := range order {
			b := blocks[i]
			switch b.kind {
			case "text":
				text.WriteString(b.buf.String())
			case "thinking":
				s := b.buf.String()
				it.Reasoning = append(it.Reasoning, session.ReasoningBlock{Text: &s,
					Opaque: anthropicOpaque{Protocol: "anthropic-messages", Signature: b.sig}})
			case "tool_use":
				args := b.buf.String()
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				it.ToolCalls = append(it.ToolCalls, session.ToolCall{WireID: b.id, Name: b.name, Args: json.RawMessage(args)})
			}
		}
		if text.Len() > 0 {
			it.Content = session.Content{session.NewTextPart(text.String())}
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
		var ev anEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fail(modelhub.CodeProtocol, "bad sse event: "+err.Error())
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				u := ev.Message.Usage
				usage = session.Usage{Input: u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
					Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens,
					Source: "Reported"}
			}
		case "content_block_start":
			if ev.ContentBlock != nil {
				blocks[ev.Index] = &anAcc{kind: ev.ContentBlock.Type, id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
				order = append(order, ev.Index)
			}
		case "content_block_delta":
			b := blocks[ev.Index]
			if b == nil || ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				b.buf.WriteString(ev.Delta.Text)
				out <- modelhub.Event{TextDelta: ev.Delta.Text}
			case "thinking_delta":
				b.buf.WriteString(ev.Delta.Thinking)
				out <- modelhub.Event{ReasoningDelta: ev.Delta.Thinking}
			case "signature_delta":
				b.sig += ev.Delta.Signature
				out <- modelhub.Event{} // 心跳：不展示的增量也要重置空闲计时（P0-13）
			case "input_json_delta":
				b.buf.WriteString(ev.Delta.PartialJSON)
				out <- modelhub.Event{} // 心跳：长工具参数流期间不被判超时（P0-13）
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stop = session.StopReason{V: ev.Delta.StopReason}
			}
			if ev.Usage != nil && ev.Usage.OutputTokens > 0 {
				usage.Output = ev.Usage.OutputTokens
			}
		case "message_stop":
			stopped = true
		case "error":
			msg, typ := "", ""
			if ev.Error != nil {
				msg, typ = ev.Error.Message, ev.Error.Type
			}
			code := modelhub.CodeServer
			switch {
			case typ == "rate_limit_error":
				code = modelhub.CodeRateLimit
			case typ == "authentication_error" || typ == "permission_error":
				code = modelhub.CodeAuth
			case typ == "invalid_request_error":
				code = modelhub.CodeInvalidRequest
				if strings.Contains(strings.ToLower(msg), "prompt is too long") || strings.Contains(strings.ToLower(msg), "context") {
					code = modelhub.CodeContextOverflow
				}
			}
			return fail(code, msg)
		}
		if stopped {
			break
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
	if !stopped && stop.V == "" {
		return fail(modelhub.CodeTransport, "stream closed without message_stop")
	}
	it := build()
	if len(it.Content) == 0 && len(it.ToolCalls) == 0 {
		return fail(modelhub.CodeEmptyResponse, "空回复")
	}
	return modelhub.Event{Done: &modelhub.Done{Reply: it, Stop: stop}}
}
