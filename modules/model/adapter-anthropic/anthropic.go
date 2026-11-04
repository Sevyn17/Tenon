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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ybase/runtime/modelhub"
	"ybase/runtime/session"
)

// AnthropicVersion 请求头 anthropic-version。
const AnthropicVersion = "2023-06-01"

// DefaultAnthropicMaxTokens max_tokens 为必填，未给时的默认值。
const DefaultAnthropicMaxTokens = 8192

// anthropicMaxBreakpoints 单次请求的 cache_control 断点上限（厂商限制）。
const anthropicMaxBreakpoints = 4

// Anthropic anthropic-messages 协议适配器。
type Anthropic struct {
	HTTP *http.Client
}

// Protocol 协议名。
func (Anthropic) Protocol() string { return "anthropic-messages" }

// Stream 单次流式调用。
func (a Anthropic) Stream(ctx context.Context, r *modelhub.Request, ep modelhub.Endpoint) <-chan modelhub.Event {
	return modelhub.RunStream(func(out chan<- modelhub.Event) modelhub.Event {
		body, f := modelhub.PostSSE(ctx, modelhub.SSERequest{Client: a.HTTP, BaseURL: ep.BaseURL,
			Default: "https://api.anthropic.com/v1", Path: "/messages", Body: anthropicBody(r, ep),
			Header: map[string]string{"anthropic-version": AnthropicVersion, "x-api-key": ep.APIKey}},
			func(status int, f *modelhub.Failure) {
				if status == 529 { // overloaded
					f.Code = modelhub.CodeServer
				}
			})
		if f != nil {
			return modelhub.Event{Failed: f}
		}
		defer body.Close()
		return parseAnthropic(ctx, body, ep.Model, out)
	})
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
	Data         string          `json:"data,omitempty"` // redacted_thinking
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
	// Redacted redacted_thinking 块的加密内容：原样回传（审查 P1-31：原先整块丢掉，工具回合里思考块不完整）。
	Redacted string `json:"redacted,omitempty"`
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
				if op.Protocol == "anthropic-messages" && op.Redacted != "" {
					bs = append(bs, anBlock{Type: "redacted_thinking", Data: op.Redacted})
					continue
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
			push("user", anBlock{Type: "tool_result", ToolUseID: id, Content: t, IsError: m.IsError})
		default:
			push("user", anBlock{Type: "text", Text: t})
		}
	}
	// 缓存断点（审查 P1-46：原先只有最后一条 user 消息打断点，工具循环里一条 user 消息并入很多 tool_result，
	// 跨度超过厂商的回看窗口时整段重写缓存）。厂商上限 4 个：工具末尾 1 + 系统末块 1 + 消息滚动 2
	// （最后两条 user 消息：本次写入的断点下次还能被读到）。系统块上显式的 Breakpoint 在名额内保留。
	userIdx := []int{}
	for i := len(msgs) - 1; i >= 0 && len(userIdx) < 2; i-- {
		if msgs[i].Role == "user" {
			userIdx = append(userIdx, i)
		}
	}
	for _, i := range userIdx {
		bs := msgs[i].Content
		bs[len(bs)-1].CacheControl = &anCache{Type: "ephemeral"}
	}
	budget := anthropicMaxBreakpoints - len(userIdx)
	if len(req.Tools) > 0 {
		budget--
	}
	if n := len(system); n > 0 {
		system[n-1].CacheControl = &anCache{Type: "ephemeral"}
		used := 1
		for i := n - 2; i >= 0; i-- { // 显式断点从后往前保留，超出名额的去掉
			if system[i].CacheControl == nil {
				continue
			}
			if used >= budget {
				system[i].CacheControl = nil
				continue
			}
			used++
		}
	}
	maxTok := uint64(DefaultAnthropicMaxTokens)
	if ep.MaxOutput > 0 { // 模型目录给了输出上限就用它（审查 P2：原先写死 8192）
		maxTok = uint64(ep.MaxOutput)
	}
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
		tools[len(tools)-1]["cache_control"] = anCache{Type: "ephemeral"} // 工具定义会话内冻结：末尾打断点
		b["tools"] = tools
	}
	if req.Params.Temperature != nil {
		b["temperature"] = *req.Params.Temperature
	}
	// tool_choice 与推理参数（审查 P1-32：原先三家适配器都没映射 ToolChoice，Reasoning 只有 responses 实现）。
	if tc := req.Params.ToolChoice; tc != nil && len(req.Tools) > 0 {
		switch tc.Mode {
		case "none":
			b["tool_choice"] = map[string]any{"type": "none"}
		case "required":
			b["tool_choice"] = map[string]any{"type": "any"}
		case "tool":
			b["tool_choice"] = map[string]any{"type": "tool", "name": tc.Name}
		case "auto":
			b["tool_choice"] = map[string]any{"type": "auto"}
		}
	}
	if budget := anthropicThinkingBudget(req.Params.Reasoning, maxTok); budget > 0 {
		b["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}
	for _, p := range req.Params.Extra {
		b[p.Name] = p.Value
	}
	return b
}

// anthropicThinkingBudget 把推理档位（low / medium / high）换成 thinking.budget_tokens；
// 预算必须小于 max_tokens、且不少于 1024（厂商下限），放不下时不开思考。
func anthropicThinkingBudget(effort *string, maxTok uint64) uint64 {
	if effort == nil {
		return 0
	}
	var budget uint64
	switch *effort {
	case "low":
		budget = 1024
	case "medium":
		budget = 4096
	case "high":
		budget = 16384
	default:
		return 0
	}
	if budget >= maxTok {
		budget = maxTok / 2
	}
	if budget < 1024 {
		return 0
	}
	return budget
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
		Data string `json:"data"` // redacted_thinking
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
	data           string // redacted_thinking 的加密内容
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
			case "redacted_thinking":
				if b.data != "" {
					it.Reasoning = append(it.Reasoning, session.ReasoningBlock{
						Opaque: anthropicOpaque{Protocol: "anthropic-messages", Redacted: b.data}})
				}
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
				blocks[ev.Index] = &anAcc{kind: ev.ContentBlock.Type, id: ev.ContentBlock.ID, name: ev.ContentBlock.Name, data: ev.ContentBlock.Data}
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
