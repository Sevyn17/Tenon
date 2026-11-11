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
	"strings"
	"time"

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

// RemoteHandler 入站 handler：收远端信封，交给 deliver（通常是 host.Deliver）。
// 返回 http.Handler；鉴权由外层（现有 web.Server 的令牌检查）负责。
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
		// 入站信封的 from 不能是 human（远端不能冒充本地已鉴权用户，08 8.5）。
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
