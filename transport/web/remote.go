package web

// 远端出站传输（框架设计 08 8.x、T6a）：把信封 POST 给对端 web 服务，实现 host.RemoteSender。
//
// 对端复用现有入站鉴权（Authorization: Bearer 令牌 + Origin/Host 检查）；caps 由发送方内核写入，
// 接收方再与自己权限求交。本客户端只负责传输，不做策略判断。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	hostpkg "github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/mail"
)

// RemotePath 远端投递的 HTTP 路径前缀（对端 handler 挂在它下面）。
const RemotePath = "/remote/"

// RemoteClient 出站传输：按 host（base URL，如 "http://peer:9000" 或 "peer:9000"）投信封。
type RemoteClient struct {
	// Token 对端要求的 Bearer 令牌。
	Token string
	// HTTP 自定义客户端（可空 = 默认 30s 超时）。
	HTTP *http.Client
}

// Send 实现 host.RemoteSender：把信封 POST 到 <host>/remote/agent:<名字>。
func (c *RemoteClient) Send(ctx context.Context, host string, env mail.Envelope) error {
	if host == "" {
		return fmt.Errorf("web: 远端地址缺 host")
	}
	base := host
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	url := strings.TrimSuffix(base, "/") + RemotePath + env.To.String()
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	cli := c.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("web: 投远端 %s：%w", host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("web: 远端 %s 返回 %d：%s", host, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// RemoteHandler 入站 handler：收远端信封，交给 deliver。
//
// deliver 必须是**远端入口**（`host.DeliverRemote`），不是 `host.Deliver`——后者是"本机入口"语义，
// 会把 caps 清成 nil（= 不限）并重置 hop，远端就能借此提权。安全归一（拒绝 human、强制
// origin=external、component 身份降级、caps/hop 沿链保留）在 `Host.DeliverRemote` 里做。
// 鉴权由外层（现有 web.Server 的令牌检查）负责。
func RemoteHandler(deliver func(ctx context.Context, env mail.Envelope) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// 路径 /remote/<kind>:<id>；信封本身带完整 To，路径只作路由提示。
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var env mail.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "bad envelope", http.StatusBadRequest)
			return
		}
		// 明显的非法来源在这里先拒（省一次投递）；其余安全归一见 Host.DeliverRemote。
		if env.From.Kind == mail.AddrHuman {
			http.Error(w, "remote cannot send as human", http.StatusForbidden)
			return
		}
		if err := deliver(r.Context(), env); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// RemoteEventsPath 跨机器事件续读的 HTTP 路径（对端 handler 挂在它上面）。
const RemoteEventsPath = "/remote/events"

// FetchEvents 实现 hostpkg.RemoteEventFetcher：按序号从对端拉事件（T6a 补 T4b 留的口）。
// 请求带 after 与 limit，对端从自己的组件事件日志里读 seq > after 的事件返回。
func (c *RemoteClient) FetchEvents(ctx context.Context, host string, after int64, limit int) ([]hostpkg.RemoteEvent, error) {
	if host == "" {
		return nil, fmt.Errorf("web: 远端地址缺 host")
	}
	base := host
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u := fmt.Sprintf("%s%s?after=%d&limit=%d", strings.TrimSuffix(base, "/"), RemoteEventsPath, after, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	cli := c.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web: 拉取 %s 的事件：%w", host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("web: 对端 %s 返回 %d：%s", host, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out []hostpkg.RemoteEvent
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("web: 对端事件格式不合法：%w", err)
	}
	return out, nil
}

// RemoteEventsHandler 入站 handler：按 after/limit 从本地组件事件日志读事件返回。
// read 由调用方给（通常是 store.EventLog.Read 的包装）；鉴权由外层 web.Server 令牌负责。
func RemoteEventsHandler(read func(ctx context.Context, after int64, limit int) ([]hostpkg.RemoteEvent, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		evs, err := read(r.Context(), after, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(evs)
	})
}
