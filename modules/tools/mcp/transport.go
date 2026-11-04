package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// StderrTail stdio 服务 stderr 保留的尾部字节数（诊断用）。
const StderrTail = 8 << 10

// MaxMessage 单条消息上限。
const MaxMessage = 16 << 20

func encode(id *int64, method string, params any) ([]byte, error) {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		m["id"] = *id
	}
	if params != nil {
		m["params"] = params
	}
	return json.Marshal(m)
}

func intID(raw *json.RawMessage) (int64, bool) {
	if raw == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.Trim(string(*raw), `"`), 10, 64)
	return n, err == nil
}

func result(m message) (json.RawMessage, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	return m.Result, nil
}

// —— stdio ——

type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	wmu    sync.Mutex
	mu     sync.Mutex
	wait   map[int64]chan message
	dead   bool
	stderr *tail
	done   chan struct{}
}

type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > StderrTail {
		t.buf = t.buf[len(t.buf)-StderrTail:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func newStdio(s Server) (transport, error) {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = scrubEnv(os.Environ(), s.Env)
	cmd.Dir = s.Cwd
	t := &stdioTransport{cmd: cmd, wait: map[int64]chan message{}, stderr: &tail{}, done: make(chan struct{})}
	cmd.Stderr = t.stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cleanup := func() {}
	if s.confine != nil {
		c, err := s.confine(cmd)
		if err != nil {
			return nil, fmt.Errorf("mcp: %s 无法放进沙箱（未启动）：%w", s.Name, err)
		}
		cleanup = c
	}
	err = cmd.Start()
	cleanup()
	if err != nil {
		return nil, fmt.Errorf("mcp: 启动 %s 失败：%w", s.Name, err)
	}
	t.stdin = in
	go t.read(out)
	return t, nil
}

func (t *stdioTransport) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxMessage)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if m.Method != "" { // 服务端请求 / 通知
			if m.ID != nil { // 不支持服务端请求：回 method not found
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID,
					"error": map[string]any{"code": -32601, "message": "method not found"}})
				t.write(b)
			}
			continue
		}
		if id, ok := intID(m.ID); ok {
			t.mu.Lock()
			ch := t.wait[id]
			delete(t.wait, id)
			t.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	t.mu.Lock()
	t.dead = true
	for id, ch := range t.wait {
		close(ch)
		delete(t.wait, id)
	}
	t.mu.Unlock()
	_ = t.cmd.Wait()
	close(t.done)
}

func (t *stdioTransport) write(b []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, err := t.stdin.Write(append(b, '\n'))
	return err
}

func (t *stdioTransport) deadErr() error {
	if s := t.stderr.String(); s != "" {
		return fmt.Errorf("%w（stderr：%s）", errDead, s)
	}
	return errDead
}

func (t *stdioTransport) call(ctx context.Context, id int64, method string, params any) (json.RawMessage, error) {
	b, err := encode(&id, method, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan message, 1)
	t.mu.Lock()
	if t.dead {
		t.mu.Unlock()
		return nil, t.deadErr()
	}
	t.wait[id] = ch
	t.mu.Unlock()
	if err := t.write(b); err != nil {
		t.mu.Lock()
		delete(t.wait, id)
		t.mu.Unlock()
		return nil, t.deadErr()
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, t.deadErr()
		}
		return result(m)
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.wait, id)
		t.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (t *stdioTransport) notify(ctx context.Context, method string, params any) error {
	b, err := encode(nil, method, params)
	if err != nil {
		return err
	}
	if err := t.write(b); err != nil {
		return t.deadErr()
	}
	return nil
}

func (t *stdioTransport) close() error {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	<-t.done
	return nil
}

// —— Streamable HTTP ——

type httpTransport struct {
	s       Server
	client  *http.Client
	mu      sync.Mutex
	session string
	version string
}

func newHTTP(s Server) *httpTransport {
	return &httpTransport{s: s, client: &http.Client{}}
}

func (h *httpTransport) setVersion(v string) {
	h.mu.Lock()
	h.version = v
	h.mu.Unlock()
}

func (h *httpTransport) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.s.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range h.s.Headers {
		req.Header.Set(k, os.ExpandEnv(v))
	}
	h.mu.Lock()
	sess, ver := h.session, h.version
	h.mu.Unlock()
	if sess != "" {
		req.Header.Set("Mcp-Session-Id", sess)
	}
	if ver != "" {
		req.Header.Set("MCP-Protocol-Version", ver)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w：%v", errDead, err)
	}
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		h.mu.Lock()
		h.session = id
		h.mu.Unlock()
	}
	if resp.StatusCode == http.StatusNotFound && sess != "" { // 会话过期：重新初始化
		resp.Body.Close()
		return nil, fmt.Errorf("%w：会话已过期", errDead)
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("mcp: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

func (h *httpTransport) call(ctx context.Context, id int64, method string, params any) (json.RawMessage, error) {
	b, err := encode(&id, method, params)
	if err != nil {
		return nil, err
	}
	resp, err := h.post(ctx, b)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, MaxMessage)
	match := func(data []byte) (message, bool) {
		var ms []message
		data = bytes.TrimSpace(data)
		if len(data) > 0 && data[0] == '[' {
			_ = json.Unmarshal(data, &ms)
		} else {
			var m message
			if json.Unmarshal(data, &m) == nil {
				ms = append(ms, m)
			}
		}
		for _, m := range ms {
			if got, ok := intID(m.ID); ok && got == id && m.Method == "" {
				return m, true
			}
		}
		return message{}, false
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 64<<10), MaxMessage)
		var data bytes.Buffer
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "data:"):
				data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
				data.WriteByte('\n')
			case line == "":
				if m, ok := match(data.Bytes()); ok {
					return result(m)
				}
				data.Reset()
			}
		}
		if m, ok := match(data.Bytes()); ok {
			return result(m)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w：事件流结束但没有响应", errDead)
	}
	all, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if m, ok := match(all); ok {
		return result(m)
	}
	return nil, fmt.Errorf("mcp: 响应里没有 id=%d 的结果", id)
}

func (h *httpTransport) notify(ctx context.Context, method string, params any) error {
	b, err := encode(nil, method, params)
	if err != nil {
		return err
	}
	resp, err := h.post(ctx, b)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (h *httpTransport) close() error {
	h.mu.Lock()
	sess := h.session
	h.mu.Unlock()
	if sess == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, h.s.URL, nil)
	if err == nil {
		req.Header.Set("Mcp-Session-Id", sess)
		if resp, err := h.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	return nil
}
