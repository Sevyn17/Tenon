package responses

// 协议适配器 openai-responses（M6.7）：OpenAI /responses SSE。
// 无状态用法（store=false）：每次发完整 input，推理以 encrypted_content 不透明回放（只回传同协议的块，03 #9）。
// 单次调用：不重试、不聚合；恰好一个终止事件；缓存用量取 input_tokens_details.cached_tokens。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"ybase-kernel/core/modelhub"
	"ybase-kernel/core/session"
)

// ProtocolResponses 协议名。
const ProtocolResponses = "openai-responses"

// OpenAIResponses openai-responses 协议适配器。
type OpenAIResponses struct {
	HTTP *http.Client
}

// Protocol 协议名。
func (OpenAIResponses) Protocol() string { return ProtocolResponses }

// Stream 单次流式调用。
func (a OpenAIResponses) Stream(ctx context.Context, r *modelhub.Request, ep modelhub.Endpoint) <-chan modelhub.Event {
	out := make(chan modelhub.Event, 32)
	go func() {
		defer close(out)
		out <- a.run(ctx, r, ep, out)
	}()
	return out
}

func (a OpenAIResponses) run(ctx context.Context, r *modelhub.Request, ep modelhub.Endpoint, out chan<- modelhub.Event) modelhub.Event {
	body, err := json.Marshal(responsesBody(r, ep))
	if err != nil {
		return modelhub.Event{Failed: &modelhub.Failure{Code: modelhub.CodeInvalidRequest, Message: err.Error()}}
	}
	base := ep.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return modelhub.Event{Failed: &modelhub.Failure{Code: modelhub.CodeInvalidRequest, Message: err.Error()}}
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	if ep.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+ep.APIKey)
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
		return modelhub.Event{Failed: modelhub.HTTPFailure(resp.StatusCode, resp.Header, b)}
	}
	return parseResponses(ctx, resp.Body, ep.Model, out)
}

// responsesOpaque 推理块的不透明部分，原样回传。
type responsesOpaque struct {
	Protocol  string `json:"protocol"`
	Encrypted string `json:"encrypted_content,omitempty"`
}

type rsPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func responsesBody(r *modelhub.Request, ep modelhub.Endpoint) map[string]any {
	req := r.Req
	wire := map[string]string{}
	input := []map[string]any{}
	for _, m := range req.Messages {
		t := modelhub.ContentString(m.Content)
		switch m.Role {
		case "assistant":
			for _, rb := range m.Reasoning {
				var op responsesOpaque
				if b, err := json.Marshal(rb.Opaque); err == nil {
					_ = json.Unmarshal(b, &op)
				}
				if op.Protocol != ProtocolResponses || op.Encrypted == "" {
					continue // 只回放同协议、带加密内容的推理
				}
				summary := []rsPart{}
				if rb.Text != nil && *rb.Text != "" {
					summary = append(summary, rsPart{Type: "summary_text", Text: *rb.Text})
				}
				input = append(input, map[string]any{"type": "reasoning", "summary": summary, "encrypted_content": op.Encrypted})
			}
			if t != "" {
				input = append(input, map[string]any{"type": "message", "role": "assistant",
					"content": []rsPart{{Type: "output_text", Text: t}}})
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
				input = append(input, map[string]any{"type": "function_call", "call_id": wid, "name": tc.Name, "arguments": args})
			}
		case "tool":
			id := wire[m.ToolCallID]
			if id == "" {
				id = m.ToolCallID
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": id, "output": t})
		default:
			input = append(input, map[string]any{"type": "message", "role": "user",
				"content": []rsPart{{Type: "input_text", Text: t}}})
		}
	}
	b := map[string]any{"model": ep.Model, "input": input, "stream": true, "store": false,
		"include": []string{"reasoning.encrypted_content"}}
	if len(req.System) > 0 {
		parts := make([]string, 0, len(req.System))
		for _, s := range req.System {
			parts = append(parts, s.Text)
		}
		b["instructions"] = strings.Join(parts, "\n\n")
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			wt := map[string]any{"type": "function", "name": t.Name}
			if t.Description != "" {
				wt["description"] = t.Description
			}
			if len(t.Schema) > 0 {
				wt["parameters"] = t.Schema
			}
			tools = append(tools, wt)
		}
		b["tools"] = tools
	}
	if req.Params.Temperature != nil {
		b["temperature"] = *req.Params.Temperature
	}
	if req.Params.MaxTokens != nil {
		b["max_output_tokens"] = *req.Params.MaxTokens
	}
	if req.Params.Reasoning != nil && *req.Params.Reasoning != "" {
		b["reasoning"] = map[string]any{"effort": *req.Params.Reasoning, "summary": "auto"}
	}
	if ep.Compat.PromptCacheKey && r.SessionKey != "" {
		b["prompt_cache_key"] = r.SessionKey
	}
	for _, p := range req.Params.Extra {
		b[p.Name] = p.Value
	}
	return b
}

type rsItem struct {
	Type      string   `json:"type"`
	ID        string   `json:"id"`
	CallID    string   `json:"call_id"`
	Name      string   `json:"name"`
	Arguments string   `json:"arguments"`
	Encrypted string   `json:"encrypted_content"`
	Summary   []rsPart `json:"summary"`
}

type rsError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type rsEvent struct {
	Type        string   `json:"type"`
	OutputIndex int      `json:"output_index"`
	Delta       string   `json:"delta"`
	Item        *rsItem  `json:"item"`
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	Error       *rsError `json:"error"`
	Response    *struct {
		Status            string   `json:"status"`
		Error             *rsError `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage *struct {
			InputTokens        uint64 `json:"input_tokens"`
			OutputTokens       uint64 `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens uint64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

type rsAcc struct {
	kind, callID, name, enc string
	buf                     strings.Builder // 文本 / 推理摘要 / 参数
	final                   bool            // 已收到 output_item.done 的完整内容
}

func rsCode(code, msg string) modelhub.Code {
	low := strings.ToLower(code + " " + msg)
	switch {
	case strings.Contains(low, "context_length") || strings.Contains(low, "context window") || strings.Contains(low, "maximum context"):
		return modelhub.CodeContextOverflow
	case strings.Contains(low, "rate_limit"):
		return modelhub.CodeRateLimit
	case strings.Contains(low, "insufficient_quota"):
		return modelhub.CodeQuota
	case strings.Contains(low, "invalid"):
		return modelhub.CodeInvalidRequest
	}
	return modelhub.CodeServer
}

func parseResponses(ctx context.Context, r io.Reader, model string, out chan<- modelhub.Event) modelhub.Event {
	var (
		items    = map[int]*rsAcc{}
		usage    = session.Usage{Source: "Missing"}
		finished bool
		stop     session.StopReason
	)
	get := func(i int, kind string) *rsAcc {
		a := items[i]
		if a == nil {
			a = &rsAcc{kind: kind}
			items[i] = a
		}
		if a.kind == "" {
			a.kind = kind
		}
		return a
	}
	build := func() session.AssistantItem {
		it := session.AssistantItem{Model: model, Usage: usage}
		idx := make([]int, 0, len(items))
		for i := range items {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		var text strings.Builder
		for _, i := range idx {
			a := items[i]
			switch a.kind {
			case "message":
				text.WriteString(a.buf.String())
			case "reasoning":
				s := a.buf.String()
				rb := session.ReasoningBlock{Text: &s}
				if a.enc != "" {
					rb.Opaque = responsesOpaque{Protocol: ProtocolResponses, Encrypted: a.enc}
				}
				it.Reasoning = append(it.Reasoning, rb)
			case "function_call":
				args := a.buf.String()
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				it.ToolCalls = append(it.ToolCalls, session.ToolCall{WireID: a.callID, Name: a.name, Args: json.RawMessage(args)})
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
loop:
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev rsEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fail(modelhub.CodeProtocol, "bad sse event: "+err.Error())
		}
		switch ev.Type {
		case "response.output_item.added":
			if ev.Item != nil {
				a := get(ev.OutputIndex, ev.Item.Type)
				a.callID, a.name = ev.Item.CallID, ev.Item.Name
			}
		case "response.output_text.delta":
			get(ev.OutputIndex, "message").buf.WriteString(ev.Delta)
			out <- modelhub.Event{TextDelta: ev.Delta}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			get(ev.OutputIndex, "reasoning").buf.WriteString(ev.Delta)
			out <- modelhub.Event{ReasoningDelta: ev.Delta}
		case "response.function_call_arguments.delta":
			get(ev.OutputIndex, "function_call").buf.WriteString(ev.Delta)
			out <- modelhub.Event{} // 心跳：长工具参数流期间不被判超时（P0-13）
		case "response.output_item.done":
			if ev.Item == nil {
				continue
			}
			a := get(ev.OutputIndex, ev.Item.Type)
			switch ev.Item.Type {
			case "function_call": // 完整参数以 done 为准
				a.callID, a.name = ev.Item.CallID, ev.Item.Name
				if ev.Item.Arguments != "" {
					a.buf.Reset()
					a.buf.WriteString(ev.Item.Arguments)
				}
			case "reasoning":
				a.enc = ev.Item.Encrypted
				if a.buf.Len() == 0 {
					for _, s := range ev.Item.Summary {
						a.buf.WriteString(s.Text)
					}
				}
			}
			a.final = true
		case "response.completed", "response.incomplete":
			if rp := ev.Response; rp != nil {
				if u := rp.Usage; u != nil {
					usage = session.Usage{Input: u.InputTokens, Output: u.OutputTokens,
						CacheRead: u.InputTokensDetails.CachedTokens, Source: "Reported"}
				}
				if rp.IncompleteDetails != nil && rp.IncompleteDetails.Reason == "max_output_tokens" {
					stop = session.StopReason{V: "length"}
				} else if rp.IncompleteDetails != nil {
					stop = session.StopReason{V: rp.IncompleteDetails.Reason}
				}
			}
			finished = true
			break loop
		case "response.failed":
			msg, code := "response failed", ""
			if ev.Response != nil && ev.Response.Error != nil {
				msg, code = ev.Response.Error.Message, ev.Response.Error.Code
			}
			return fail(rsCode(code, msg), msg)
		case "error":
			msg, code := ev.Message, ev.Code
			if ev.Error != nil {
				msg, code = ev.Error.Message, ev.Error.Code
			}
			return fail(rsCode(code, msg), msg)
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
	if !finished {
		return fail(modelhub.CodeTransport, "stream closed without response.completed")
	}
	it := build()
	if stop.V == "" {
		stop = session.StopReason{V: "stop"}
		if len(it.ToolCalls) > 0 {
			stop = session.StopReason{V: "tool_calls"}
		}
	}
	if len(it.Content) == 0 && len(it.ToolCalls) == 0 {
		return fail(modelhub.CodeEmptyResponse, "空回复")
	}
	return modelhub.Event{Done: &modelhub.Done{Reply: it, Stop: stop}}
}
