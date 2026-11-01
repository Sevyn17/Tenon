package session

// 请求构造（M4 §3.2 Context；M6 §4.9 build_with_compaction / build_branch）。
//
// 纯函数：从上下文展开模型可见请求。压缩条目「取代完整日志中 [0, upto) 的内容」：
// 遇到 Compaction 条目时，丢弃完整日志下标 < upto 的消息（含更早的摘要），
// 在最前面放上新摘要，[upto, 压缩条目) 之间的消息保留。
//
// 只有一条构造路径（build）：BuildRequest 与 BuildWithCompaction 都经它，
// 后者只是在末尾多一个"假设的压缩条目"——保证二者与 Compact 生效后的请求逐字节一致
// （session-core C1 / C2，对拍测试见 request_test.go）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Context：内核上下文（M4 §3.2）。
type Context struct {
	Config *Config
	Items  []*Entry // 恢复时只加载 [base, end)
	Base   int      // items[0] 在完整日志中的下标
	Ext    map[string]any
}

// Message：请求消息（已按调用顺序展开）。
type Message struct {
	Role       string // user / assistant / tool
	Content    Content
	ToolCalls  []ToolCall // assistant：携带 wire_id 回传厂商
	ToolCallID string     // tool：对应的调用内核 id
	// Reasoning assistant 的思考块（只读原样回传；适配器只回传同协议带签名的，03 #9）。
	Reasoning []ReasoningBlock `json:",omitempty"`
}

// Request：模型可见请求。
type Request struct {
	Model    string
	System   []SystemBlock
	Messages []Message
	Tools    []ToolSpec
	Params   Params
}

// BuildRequest 从上下文构造请求。
func BuildRequest(ctx Context) (Request, error) {
	return build(ctx, nil)
}

// BuildWithCompaction 构造「假设已在 upto 处压缩」的请求（M6 §4.9 流程 3 的唯一例外入口）。
// 结果与 Compact(summary, upto) 生效后的 BuildRequest 逐字节一致。
func BuildWithCompaction(ctx Context, summary Content, upto int) (Request, error) {
	if upto < ctx.Base {
		return Request{}, fmt.Errorf("%w: upto=%d 早于加载点 %d", ErrState, upto, ctx.Base)
	}
	return build(ctx, &CompactionItem{Upto: upto, Summary: summary})
}

// BuildBranch 构造旁支请求：当前完整请求 + 一条合成提问（M6 §4.10 回合后旁支记忆）。
func BuildBranch(ctx Context, extraInput Content) (Request, error) {
	req, err := BuildRequest(ctx)
	if err != nil {
		return req, err
	}
	req.Messages = append(req.Messages, Message{Role: "user", Content: extraInput})
	return req, nil
}

// RequestHash 返回请求的确定性哈希（JSON 序列化后 SHA-256，十六进制）。
// 用于请求记录的 base_hash / final_hash（session-core D1 / D2）。
func RequestHash(req Request) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// summaryAbs：摘要消息的下标标记。小于任何合法 upto，后续压缩必然取代它。
const summaryAbs = -1

type absMessage struct {
	abs int
	msg Message
}

// build 是唯一的构造路径；virtual 非空时视作末尾追加了一个压缩条目。
func build(ctx Context, virtual *CompactionItem) (Request, error) {
	if ctx.Config == nil {
		return Request{}, fmt.Errorf("%w: 缺配置", ErrState)
	}
	req := Request{
		Model:  ctx.Config.Model,
		System: ctx.Config.System,
		Tools:  ctx.Config.Tools,
		Params: ctx.Config.Params,
	}
	var msgs []absMessage
	compact := func(c *CompactionItem) {
		kept := msgs[:0:0]
		if len(c.Summary) > 0 {
			kept = append(kept, absMessage{abs: summaryAbs, msg: Message{Role: "user", Content: c.Summary}})
		}
		for _, m := range msgs {
			if m.abs >= c.Upto {
				kept = append(kept, m)
			}
		}
		msgs = kept
	}
	for i, e := range ctx.Items {
		abs := ctx.Base + i
		switch e.Item.Kind {
		case KindCompaction:
			compact(e.Item.Compaction)
			continue
		case KindUnknown:
			continue // R15
		}
		m, err := entryToMessage(e, abs)
		if err != nil {
			return req, err
		}
		msgs = append(msgs, absMessage{abs: abs, msg: m})
	}
	if virtual != nil {
		compact(virtual)
	}
	for _, m := range msgs {
		req.Messages = append(req.Messages, m.msg)
	}
	return req, nil
}

// entryToMessage 展开 user / assistant / tool_result 条目。
func entryToMessage(e *Entry, abs int) (Message, error) {
	switch e.Item.Kind {
	case KindUser:
		return Message{Role: "user", Content: e.Item.User.Content}, nil
	case KindAssistant:
		return Message{Role: "assistant", Content: e.Item.Assistant.Content, ToolCalls: e.Item.Assistant.ToolCalls,
			Reasoning: e.Item.Assistant.Reasoning}, nil
	case KindToolResult:
		return Message{Role: "tool", Content: e.Item.ToolResult.Output, ToolCallID: e.Item.ToolResult.CallID}, nil
	default:
		return Message{}, fmt.Errorf("%w: 条目 %d 未知种类 %q", ErrState, abs, e.Item.Kind)
	}
}
