package agent

// 观测（review-1 G2 / G3，M2.10）：span 先进内存缓冲，随下一次 checkpoint 同事务提交，
// 不额外开事务、不拖慢模型调用；崩溃最多丢正在进行的那一步。

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"ybase/runtime/store"
)

// span 一个进行中的观测区间。
type span struct {
	a     *Agent
	s     store.Span
	attrs map[string]any
}

// startSpan 开一个 span；parent 为空表示顶层。
func (a *Agent) startSpan(kind, name, parent string) *span {
	id, _ := uuid.NewV7()
	return &span{a: a, s: store.Span{ID: id.String(), Parent: parent, Kind: kind, Name: name, Start: a.clock()},
		attrs: map[string]any{}}
}

// Set 记一个属性（只放元数据与引用，不放正文）。
func (sp *span) Set(k string, v any) *span {
	sp.attrs[k] = v
	return sp
}

// End 结束并放入待提交缓冲。
func (sp *span) End(status string) {
	sp.s.End = sp.a.clock()
	sp.s.Status = status
	if len(sp.attrs) > 0 {
		if b, err := json.Marshal(sp.attrs); err == nil {
			sp.s.Attrs = b
		}
	}
	sp.a.buf.spans = append(sp.a.buf.spans, sp.s)
}

func (a *Agent) clock() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}
