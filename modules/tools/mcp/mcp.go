// Package mcp MCP 客户端：把外部 MCP 服务的工具注册为 toolhub 工具（动态来源，默认延迟显露）。
//
// 纯 Go 的 JSON-RPC 2.0；传输支持 stdio 与 Streamable HTTP。见 docs/90 - 归档/20 - 插件/13 - MCP 客户端.md。
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// ProtocolVersion 请求的协议版本。
const ProtocolVersion = "2025-06-18"

// DefaultTimeout 单次调用超时。
const DefaultTimeout = 60 * time.Second

// DescMax 工具描述上限。
const DescMax = 2 << 10

var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Server 一个服务的配置。
type Server struct {
	Name    string            `toml:"name"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	Cwd     string            `toml:"cwd"`
	URL     string            `toml:"url"`
	Headers map[string]string `toml:"headers"`
	Timeout int               `toml:"timeout"` // 秒；0 = 60
	// Trust 对本服务工具的信任："ask"（缺省，按档位 external 效果）/ "readonly"（按读）/ "trusted"（按写工作区）。
	Trust string `toml:"trust"`
	// Sandbox stdio 子进程的沙箱：read-only / workspace-write / danger-full-access；空 = 缺省档位的 [sandbox]。
	Sandbox string `toml:"sandbox"`

	confine func(cmd *exec.Cmd) (func(), error) // 由 Open 按 Config.Confine 填入
	// noExpand 插件包来源（第三方）：env / headers 不展开宿主环境变量，只认已替换好的 ${TENON_PLUGIN_DIR}
	// （2026-11-04 审查 P1-7：否则包里写 "${TENON_API_KEY}" 就能把宿主密钥带进子进程或发到自己的 URL）。
	noExpand bool
}

// expand 按来源展开值：用户自己的配置展开宿主环境变量，插件包来源原样使用。
func (s Server) expand(v string) string {
	if s.noExpand {
		return v
	}
	return os.ExpandEnv(v)
}

// 信任级别。
const (
	TrustAsk      = "ask"
	TrustReadonly = "readonly"
	TrustTrusted  = "trusted"
)

// Config 配置文件。
type Config struct {
	Server []Server `toml:"server"`
	// Confine 启动 stdio 子进程前调用（放进沙箱）；返回的 cleanup 在 Start 之后调用。nil = 不加沙箱。
	Confine func(cmd *exec.Cmd, s Server) (cleanup func(), err error) `toml:"-"`
}

// LoadConfig 读 TOML 配置；文件不存在返回空配置。
func LoadConfig(path string) (Config, error) {
	var c Config
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return c, fmt.Errorf("mcp: 配置 %s：%w", path, err)
	}
	if err := checkServers(c.Server); err != nil {
		return c, fmt.Errorf("mcp: %w", err)
	}
	return c, nil
}

// checkServers 校验一组服务配置（mcp.toml 与插件包的 [[server]] 共用）。
func checkServers(ss []Server) error {
	seen := map[string]bool{}
	for _, s := range ss {
		if !serverNameRe.MatchString(s.Name) {
			return fmt.Errorf("服务名 %q 不合法（[A-Za-z0-9_-]{1,32}）", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("服务名 %q 重复", s.Name)
		}
		seen[s.Name] = true
		if (s.Command == "") == (s.URL == "") {
			return fmt.Errorf("服务 %s 须且只能配置 command 或 url 之一", s.Name)
		}
		switch s.Trust {
		case "", TrustAsk, TrustReadonly, TrustTrusted:
		default:
			return fmt.Errorf("服务 %s 的 trust %q 不合法（ask / readonly / trusted）", s.Name, s.Trust)
		}
		if s.Sandbox != "" {
			if _, err := toolhub.ParseSandboxMode(s.Sandbox); err != nil {
				return fmt.Errorf("服务 %s：%w", s.Name, err)
			}
		}
	}
	return nil
}

func (s Server) timeout() time.Duration {
	if s.Timeout > 0 {
		return time.Duration(s.Timeout) * time.Second
	}
	return DefaultTimeout
}

// RPCError JSON-RPC 错误。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp: %d %s", e.Code, e.Message) }

type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// transport 一种传输。call 发请求等响应；notify 发通知。
type transport interface {
	call(ctx context.Context, id int64, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	close() error
}

// RemoteTool 服务端工具定义。
type RemoteTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint    *bool `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
	} `json:"annotations"`
}

// AttrDestructive 与权限插件约定的属性名：服务端声明工具有破坏性。
const AttrDestructive = "destructive"

// Client 一个服务的连接。
type Client struct {
	Server   Server
	Protocol string // 服务端回的协议版本
	Info     json.RawMessage

	mu   sync.Mutex
	tr   transport
	next int64
}

// Connect 建立连接并完成初始化握手。
func Connect(ctx context.Context, s Server) (*Client, error) {
	c := &Client{Server: s}
	if err := c.connect(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) connect(ctx context.Context) error {
	var tr transport
	var err error
	if c.Server.Command != "" {
		tr, err = newStdio(c.Server)
	} else {
		tr = newHTTP(c.Server)
	}
	if err != nil {
		return err
	}
	ictx, cancel := context.WithTimeout(ctx, c.Server.timeout())
	defer cancel()
	c.next++
	res, err := tr.call(ictx, c.next, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tenon", "version": "0.1"},
	})
	if err != nil {
		tr.close()
		return fmt.Errorf("mcp: %s 初始化失败：%w", c.Server.Name, err)
	}
	var ir struct {
		ProtocolVersion string          `json:"protocolVersion"`
		ServerInfo      json.RawMessage `json:"serverInfo"`
	}
	_ = json.Unmarshal(res, &ir)
	if h, ok := tr.(*httpTransport); ok {
		h.setVersion(ir.ProtocolVersion)
	}
	if err := tr.notify(ictx, "notifications/initialized", nil); err != nil {
		tr.close()
		return fmt.Errorf("mcp: %s 初始化失败：%w", c.Server.Name, err)
	}
	c.tr, c.Protocol, c.Info = tr, ir.ProtocolVersion, ir.ServerInfo
	return nil
}

// Close 断开。
func (c *Client) Close() error {
	c.mu.Lock()
	tr := c.tr
	c.tr = nil
	c.mu.Unlock()
	if tr != nil {
		return tr.close()
	}
	return nil
}

// errDead 传输已断开（stdio 进程退出等），可重连一次。
var errDead = errors.New("mcp: 连接已断开")

// errUnsent 请求没有发到服务端（连接早已断开、写失败、会话过期被拒）：重连后重发总是安全的。
// 与"发出后才断开"区分：后者服务端可能已经执行，有副作用的 tools/call 不能重发（审查 P1-8）。
var errUnsent = fmt.Errorf("%w（请求未发出）", errDead)

// Call 发一个请求；连接断开时重连一次再发。tools/call 不走这里（见 callTool）：断开时服务端可能已经执行，
// 重发会让有副作用的工具执行两次。
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.invoke(ctx, method, params, true)
}

// invoke 发一个请求；retry 时连接断开重连一次再发。
func (c *Client) invoke(ctx context.Context, method string, params any, retry bool) (json.RawMessage, error) {
	res, err := c.call(ctx, method, params)
	if (errors.Is(err, errUnsent) || retry && errors.Is(err, errDead)) && ctx.Err() == nil {
		c.mu.Lock()
		if c.tr != nil {
			c.tr.close()
			c.tr = nil
		}
		c.mu.Unlock()
		res, err = c.call(ctx, method, params)
	}
	return res, err
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.tr == nil {
		// 建连接（起进程 + 握手）不占这次请求的超时：负载高时起进程要几秒，否则重连后第一次调用的
		// 预算会被建立连接吃掉，报成"调用超时"（2026-11-07：CI 全量并行下偶发）。
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.Server.timeout())
		err := c.connect(cctx)
		cancel()
		if err != nil {
			c.mu.Unlock()
			return nil, err
		}
		if ctx.Err() != nil { // 等连接期间调用方已取消：别留下这条新连接
			_ = c.tr.close()
			c.tr = nil
			c.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	c.next++
	id, tr := c.next, c.tr
	c.mu.Unlock()
	// 单次请求的超时按请求计（调用方没给更短的期限时），重连后的重发重新计时。
	tctx, cancel := context.WithTimeout(ctx, c.Server.timeout())
	defer cancel()
	res, err := tr.call(tctx, id, method, params)
	if err != nil && tctx.Err() != nil && ctx.Err() == nil {
		// 本次请求超时（不是调用方取消）：告知服务端（尽力）
		nctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = tr.notify(nctx, "notifications/cancelled", map[string]any{"requestId": id, "reason": "timeout"})
		cancel()
	}
	return res, err
}

// ListTools 拉取全部工具（处理分页）。
func (c *Client) ListTools(ctx context.Context) ([]RemoteTool, error) {
	var all []RemoteTool
	cursor := ""
	for i := 0; i < 100; i++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.Call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []RemoteTool `json:"tools"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &page); err != nil {
			return nil, fmt.Errorf("mcp: tools/list 响应不合法：%w", err)
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
	return all, nil
}

// PublicName 模型侧工具名：mcp__<server>__<tool>；有损规范化或超长时追加 12 位哈希。
func PublicName(server, raw string) string {
	prefix := "mcp__" + server + "__"
	var b strings.Builder
	lossy := false
	for _, r := range raw {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
			lossy = true
		}
	}
	name := b.String()
	if name == "" {
		lossy = true
	}
	if !lossy && len(prefix)+len(name) <= 64 {
		return prefix + name
	}
	sum := sha256.Sum256([]byte(server + "\x00" + raw))
	h := "_" + hex.EncodeToString(sum[:])[:12]
	if max := 64 - len(prefix) - len(h); len(name) > max {
		name = name[:max]
	}
	return prefix + name + h
}

// Tools 把服务端工具转成 toolhub 工具。
func (c *Client) Tools(remote []RemoteTool) []*toolhub.Tool {
	no := false
	var out []*toolhub.Tool
	for _, rt := range remote {
		raw := rt.Name
		schema := rt.InputSchema
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		desc := rt.Description
		if len(desc) > DescMax {
			desc = clipUTF8(desc, DescMax) + "…" // 按 UTF-8 边界截，不切坏多字节字符（审查 P2）
		}
		desc = fmt.Sprintf("[MCP 服务 %s 提供] %s", c.Server.Name, desc)
		risk := toolhub.Write // 权限类别在权限项上（Items 按 trust 定），这里只定并行与取消语义
		if c.Server.Trust == TrustReadonly {
			risk = toolhub.Read
		}
		attrs := map[string]any{"mcp_server": c.Server.Name, "mcp_tool": raw}
		if h := rt.Annotations.DestructiveHint; h != nil && *h && c.Server.Trust != TrustReadonly {
			attrs[AttrDestructive] = true
		}
		// 只读工具断线后可以安全重发（2026-11-04 审查 P1-8：原先所有调用都重连重发）。
		readOnly := c.Server.Trust == TrustReadonly || (rt.Annotations.ReadOnlyHint != nil && *rt.Annotations.ReadOnlyHint)
		out = append(out, &toolhub.Tool{
			Spec:     session.ToolSpec{Name: PublicName(c.Server.Name, raw), Description: desc, Schema: schema},
			Risk:     risk,
			Parallel: &no,
			Timeout:  c.Server.timeout() + 5*time.Second,
			Deferred: true,
			Attrs:    attrs,
			Items:    []string{ItemID(c.Server.Name)},
			Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
				return c.callTool(ctx, cx, raw, args, readOnly), nil
			},
		})
	}
	return out
}

// callTool 调一次远端工具。readOnly=false 时断线不重发，按结果未知报给模型。
func (c *Client) callTool(ctx context.Context, cx *toolhub.Cx, raw string, args json.RawMessage, readOnly bool) toolhub.Output {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	res, err := c.invoke(ctx, "tools/call", map[string]any{"name": raw, "arguments": args}, readOnly)
	if err != nil {
		if errors.Is(err, errDead) && !errors.Is(err, errUnsent) && ctx.Err() == nil && !readOnly {
			return toolhub.Failf("MCP 服务 %s 在调用 %s 时断开，服务端可能已经执行，结果未知。请先核对状态再决定是否重试。", c.Server.Name, raw)
		}
		if ctx.Err() != nil {
			return toolhub.Output{Kind: toolhub.Done, Content: session.Content{session.NewTextPart("调用已取消")}, Status: session.ToolTurnCancelled}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return toolhub.Failf("MCP 服务 %s 调用 %s 超时（%s）", c.Server.Name, raw, c.Server.timeout())
		}
		return toolhub.Failf("MCP 服务 %s 调用 %s 失败：%v", c.Server.Name, raw, err)
	}
	return renderResult(res, mediaPutter(ctx, cx))
}

// mediaPutter 把 MCP 返回的图片存成本会话的工件（cx.Hub 没有工件存储时返回 nil，图片留占位文字）。
func mediaPutter(ctx context.Context, cx *toolhub.Cx) func(mime string, data []byte) (session.Part, bool) {
	if cx == nil || cx.Hub == nil {
		return nil
	}
	return func(mime string, data []byte) (session.Part, bool) {
		p, err := cx.Hub.PutMedia(ctx, cx.SessionID, mime, data)
		return p, err == nil
	}
}

// renderResult 把 tools/call 结果转成工具输出：文本拼成一段；图片经 put 存成工件、作为媒体片段跟在文本后面
// （2026-11-04 审查 P2 工具体验：原先图片一律丢弃）。put 为 nil 或存不了的图片、音频留占位文字。
func renderResult(res json.RawMessage, put func(mime string, data []byte) (session.Part, bool)) toolhub.Output {
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Data     []byte `json:"data"` // image / audio：base64（encoding/json 自动解码）
			URI      string `json:"uri"`
			Name     string `json:"name"`
			Resource *struct {
				URI      string `json:"uri"`
				Text     string `json:"text"`
				MimeType string `json:"mimeType"`
			} `json:"resource"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return toolhub.Failf("MCP 结果不合法：%v", err)
	}
	var parts []string
	var media session.Content
	for _, c := range r.Content {
		if c.Type == "image" && put != nil && len(c.Data) > 0 {
			if p, ok := put(c.MimeType, c.Data); ok {
				media = append(media, p)
				continue
			}
		}
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "resource_link":
			parts = append(parts, fmt.Sprintf("[资源 %s] %s", c.Name, c.URI))
		case "resource":
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else if c.Resource != nil {
				parts = append(parts, fmt.Sprintf("[内嵌资源 %s（%s）未显示]", c.Resource.URI, c.Resource.MimeType))
			}
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("[%s 内容（%s）未显示]", c.Type, c.MimeType))
		default:
			parts = append(parts, fmt.Sprintf("[未知内容类型 %s]", c.Type))
		}
	}
	if len(parts) == 0 && len(r.StructuredContent) > 0 {
		parts = append(parts, string(r.StructuredContent))
	}
	text := strings.Join(parts, "\n")
	if r.IsError {
		if text == "" {
			text = "MCP 工具报错（无详情）"
		}
		return toolhub.Failf("%s", text)
	}
	out := toolhub.Text(text)
	out.Content = append(out.Content, media...)
	return out
}

// scrubEnv 去掉名字像密钥的环境变量与 TENON_* 变量（DSH 同规则）。
var secretName = regexp.MustCompile(`(?i)KEY|PASSWORD|SECRET|TOKEN`)

func scrubEnv(environ []string, extra map[string]string) []string {
	return scrubEnvWith(environ, extra, os.ExpandEnv)
}

// scrubEnvWith 同 scrubEnv，extra 的值用 expand 展开（插件包来源传原样函数）。
func scrubEnvWith(environ []string, extra map[string]string, expand func(string) string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if secretName.MatchString(k) || strings.HasPrefix(strings.ToUpper(k), "TENON_") {
			continue
		}
		if _, over := extra[k]; over {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+expand(v))
	}
	return out
}

// Source 一组服务的连接与工具。
type Source struct {
	Clients  []*Client
	Tools    []*toolhub.Tool
	Warnings []string
}

// Open 连接配置里的全部服务并拉工具；失败的服务只告警。各服务并行连接，结果按配置顺序合并
// （2026-11-04 审查 P2 工具体验：原先串行，一个慢服务拖住全部启动）。
func Open(ctx context.Context, cfg Config) *Source {
	type result struct {
		c    *Client
		ts   []*toolhub.Tool
		warn string
	}
	res := make([]result, len(cfg.Server))
	var wg sync.WaitGroup
	for i, sv := range cfg.Server {
		if cfg.Confine != nil && sv.Command != "" {
			conf, orig := cfg.Confine, sv
			sv.confine = func(cmd *exec.Cmd) (func(), error) { return conf(cmd, orig) }
		}
		wg.Add(1)
		go func(i int, sv Server) {
			defer wg.Done()
			c, err := Connect(ctx, sv)
			if err != nil {
				res[i].warn = err.Error()
				return
			}
			ts, err := c.ListTools(ctx)
			if err != nil {
				res[i].warn = fmt.Sprintf("mcp: %s tools/list 失败：%v", sv.Name, err)
				c.Close()
				return
			}
			res[i] = result{c: c, ts: c.Tools(ts)}
		}(i, sv)
	}
	wg.Wait()
	s := &Source{}
	for _, r := range res {
		if r.warn != "" {
			s.Warnings = append(s.Warnings, r.warn)
			continue
		}
		s.Clients = append(s.Clients, r.c)
		s.Tools = append(s.Tools, r.ts...)
	}
	return s
}

// Close 断开全部。
func (s *Source) Close() {
	for _, c := range s.Clients {
		c.Close()
	}
}

// clipUTF8 截到不超过 n 字节，退到 UTF-8 字符边界。
func clipUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
