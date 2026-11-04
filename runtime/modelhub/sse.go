package modelhub

// 协议适配器共用的流式请求骨架（2026-11-04 审查 P1-60：原先三个适配器各写一份 Stream / run，
// 起协程、序列化、拼 URL、设请求头、区分取消与传输错误、读错误体，逐行近似）。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// RunStream 起协程执行 run，把它的返回值作为唯一的终止事件发出后关闭通道。
// run 期间的增量事件写进 out；返回值必须是 Done 或 Failed。
func RunStream(run func(out chan<- Event) Event) <-chan Event {
	out := make(chan Event, 32)
	go func() {
		defer close(out)
		out <- run(out)
	}()
	return out
}

// SSERequest 一次流式 POST。
type SSERequest struct {
	Client  *http.Client      // nil = http.DefaultClient
	BaseURL string            // 端点里的 base；空用 Default
	Default string            // 缺省 base
	Path    string            // 如 "/messages"
	Header  map[string]string // 额外请求头（鉴权、版本）
	Body    any               // 请求体，按 JSON 序列化
}

// PostSSE 发请求，返回 200 的响应体（调用方负责关闭）；失败按稳定失败码归一：
// 序列化 / 构造失败 → InvalidRequest；ctx 已取消 → Aborted；其余网络错误 → Transport；
// 非 200 → HTTPFailure（读最多 4KB 错误体）。fix 可空：按状态码再修正失败码（如 Anthropic 529）。
func PostSSE(ctx context.Context, r SSERequest, fix func(status int, f *Failure)) (io.ReadCloser, *Failure) {
	body, err := json.Marshal(r.Body)
	if err != nil {
		return nil, &Failure{Code: CodeInvalidRequest, Message: err.Error()}
	}
	base := r.BaseURL
	if base == "" {
		base = r.Default
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+r.Path, bytes.NewReader(body))
	if err != nil {
		return nil, &Failure{Code: CodeInvalidRequest, Message: err.Error()}
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	for k, v := range r.Header {
		if v != "" {
			hreq.Header.Set(k, v)
		}
	}
	hc := r.Client
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Failure{Code: CodeAborted, Message: err.Error()}
		}
		return nil, &Failure{Code: CodeTransport, Message: err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		f := HTTPFailure(resp.StatusCode, resp.Header, b)
		if fix != nil {
			fix(resp.StatusCode, f)
		}
		return nil, f
	}
	return resp.Body, nil
}
