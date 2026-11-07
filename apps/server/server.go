package main

// 网页服务的 HTTP / WebSocket 处理：静态前端 + /ws（别名 /api）上的命令与事件。
// 协议见 docs/90 - 归档/10 - 框架模块/12 - 应用接口层.md §二；安全规则见 docs/90 - 归档/10 - 框架模块/14 - 网页服务.md。

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"

	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"tenon/appapi"
	"tenon/appapi/wire"
)

const (
	// CookieName 令牌 cookie（HttpOnly + SameSite=Strict），首个带 ?token= 的页面请求设置，之后静态资源与 WebSocket 都凭它通过。
	CookieName = "tenon_token"
	// MaxFrame 单个请求帧上限。
	MaxFrame = 4 << 20
	// MaxInflight 每个连接同时执行的命令数上限；满了暂停读帧（背压）。
	MaxInflight = 16
	// WriteTimeout 单帧写超时；超时视为连接坏掉，关闭。
	WriteTimeout = 10 * time.Second
)

// NewToken 生成随机令牌：32 字节 crypto/rand，base64url 无填充（43 字符）。
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// LoopbackHosts 本机访问允许的 Host 头（防 DNS 重绑定：只认这几个名字 + 实际端口）。
func LoopbackHosts(port int) []string {
	p := strconv.Itoa(port)
	return []string{"127.0.0.1:" + p, "localhost:" + p, "[::1]:" + p}
}

// Config 服务配置。
type Config struct {
	API    *appapi.App
	Token  string
	Hosts  []string // 允许的 Host 头（host:port），大小写不敏感
	TLS    bool     // 走 HTTPS：Origin 须为 https，cookie 加 Secure
	Static fs.FS    // 前端构建产物（web.Dist()）
}

// Server 网页服务（http.Handler）。
type Server struct {
	cfg    Config
	hosts  map[string]bool
	static http.Handler
}

// New 构造服务。
func New(c Config) *Server {
	s := &Server{cfg: c, hosts: map[string]bool{}}
	for _, h := range c.Hosts {
		s.hosts[strings.ToLower(h)] = true
	}
	if c.Static != nil {
		s.static = http.FileServerFS(c.Static)
	}
	return s
}

func (s *Server) scheme() string {
	if s.cfg.TLS {
		return "https"
	}
	return "http"
}

// ServeHTTP 先查 Host，再按路径分到 WebSocket 或静态页；两者都要令牌。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer") // URL 里可能带令牌
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if !s.hosts[strings.ToLower(r.Host)] {
		deny(w, http.StatusForbidden, "Host 不允许")
		return
	}
	// 严格 CSP：只同源；WebSocket 只连本服务。不发任何 CORS 头。
	ws := "ws://" + r.Host
	if s.cfg.TLS {
		ws = "wss://" + r.Host
	}
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self' "+ws+
		"; object-src 'none'; base-uri 'none'; form-action 'none'; frame-src 'none'; frame-ancestors 'none'")
	switch r.URL.Path {
	case "/ws", "/api": // /api 是前端 transport.ts 的缺省路径
		s.serveWS(w, r)
	case appapi.UploadPath: // 大附件上传（06 10.9）：与 WebSocket 一样要令牌与同源 Origin
		s.serveUpload(w, r)
	default:
		s.serveStatic(w, r)
	}
}

func deny(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	b, _ := json.Marshal(appapi.Errorf(appapi.CodeUnauthorized, "%s", msg))
	_, _ = w.Write(b)
}

// tokenOK 常量时间比较。
func (s *Server) tokenOK(t string) bool {
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.cfg.Token)) == 1
}

// authorized 令牌来源：?token=、cookie、Authorization: Bearer。返回是否通过与是否来自查询串。
func (s *Server) authorized(r *http.Request) (ok, fromQuery bool) {
	if q := r.URL.Query().Get("token"); q != "" {
		return s.tokenOK(q), true
	}
	if c, err := r.Cookie(CookieName); err == nil && s.tokenOK(c.Value) {
		return true, false
	}
	if a, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found && s.tokenOK(a) {
		return true, false
	}
	return false, false
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		deny(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	ok, fromQuery := s.authorized(r)
	if !ok {
		deny(w, http.StatusUnauthorized, "令牌错误或缺失：请用启动时打印的地址打开")
		return
	}
	if fromQuery {
		// 把令牌换成 cookie，再跳到不带令牌的地址（?token= 留空：前端据此选 WebSocket 传输），
		// 避免令牌留在地址栏和浏览历史里。
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: s.cfg.Token, Path: "/", HttpOnly: true,
			Secure: s.cfg.TLS, SameSite: http.SameSiteStrictMode})
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			http.Redirect(w, r, "/?token=", http.StatusSeeOther)
			return
		}
	}
	if s.static == nil {
		http.NotFound(w, r)
		return
	}
	s.static.ServeHTTP(w, r)
}

// originOK Origin 必须存在且与 Host 同源（同协议、同 host:port）。
func (s *Server) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Scheme != s.scheme() || u.Path != "" && u.Path != "/" || u.User != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request) {
	if ok, _ := s.authorized(r); !ok {
		deny(w, http.StatusUnauthorized, "令牌错误或缺失")
		return
	}
	if !s.originOK(r) { // 防跨站表单提交（cookie 是 SameSite=Strict，这里再挡一层）
		deny(w, http.StatusForbidden, "Origin 与 Host 不同源")
		return
	}
	s.cfg.API.UploadHandler().ServeHTTP(w, r)
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	if ok, _ := s.authorized(r); !ok {
		deny(w, http.StatusUnauthorized, "令牌错误或缺失")
		return
	}
	if !s.originOK(r) {
		deny(w, http.StatusForbidden, "Origin 与 Host 不同源")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		deny(w, http.StatusBadRequest, "需要 WebSocket 升级请求")
		return
	}
	websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil }, // Origin 已在上面检查
		Handler:   s.handleConn,
	}.ServeHTTP(w, r)
}

type request struct {
	ID   json.RawMessage `json:"id"`
	Cmd  string          `json:"cmd"`
	Args json.RawMessage `json:"args"`
}

type response struct {
	ID     json.RawMessage `json:"id"`
	OK     bool            `json:"ok"`
	Result any             `json:"result,omitempty"`
	Error  *appapi.Error   `json:"error,omitempty"`
}

type eventFrame struct {
	Event wire.Envelope `json:"event"`
}

// conn 一个 WebSocket 连接；写串行化。
type conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (c *conn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		if r, ok := v.(response); ok {
			b, _ = json.Marshal(response{ID: r.ID, Error: appapi.Errorf(appapi.CodeInternal, "结果编码失败：%v", err)})
		} else {
			return
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(WriteTimeout))
	if err := websocket.Message.Send(c.ws, string(b)); err != nil {
		_ = c.ws.Close() // 读循环随之退出
	}
}

func (s *Server) handleConn(ws *websocket.Conn) {
	ws.MaxPayloadBytes = MaxFrame
	// 请求上下文派生自 http.Server.BaseContext：服务关闭时取消，连接随之关闭。
	ctx, cancel := context.WithCancel(ws.Request().Context())
	defer cancel()
	c := &conn{ws: ws}
	// protocol 2（06 10.8、10.9）：每个连接一个 appapi.Conn，身份由本服务的令牌鉴权得出（单机固定 local / operator）；
	// 事件只推这个连接订阅了的会话。
	ac := s.cfg.API.NewConn(ctx, appapi.LocalPrincipal)
	defer ac.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-ctx.Done(); _ = ws.Close() }()
	go func() { defer wg.Done(); s.events(ctx, c, ac) }()
	sem := make(chan struct{}, MaxInflight)
	for {
		var text string
		if err := websocket.Message.Receive(ws, &text); err != nil {
			break
		}
		var f request
		if err := json.Unmarshal([]byte(text), &f); err != nil || f.Cmd == "" {
			c.send(response{ID: f.ID, Error: appapi.Errorf(appapi.CodeBadRequest, "请求帧须为 {\"id\",\"cmd\",\"args\"}")})
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(f request) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := ac.Call(ctx, f.Cmd, f.Args)
			if err != nil {
				c.send(response{ID: f.ID, Error: appapi.AsError(err)})
				return
			}
			c.send(response{ID: f.ID, OK: true, Result: res})
		}(f)
	}
	cancel()
	wg.Wait()
}

// events 把连接的出站事件（订阅了的会话的持久与易失事件、连接级 sessions.changed）推给客户端。
// 背压在 Conn 里处理（合并、丢弃、断开订阅并发 session.lagged），这里只管按序写。
func (s *Server) events(ctx context.Context, c *conn, ac *appapi.Conn) {
	for {
		evs, err := ac.Next(ctx)
		if err != nil {
			return
		}
		for _, e := range evs {
			c.send(eventFrame{Event: e})
		}
	}
}
