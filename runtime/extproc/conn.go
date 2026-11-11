package extproc

// 回桥（框架设计 04 4.8，T6b）：在 stdio 上跑 JSON-RPC 2.0 请求-响应循环。
// 一个插件进程服务所有会话：请求带 JSON-RPC id，响应按 id 路由回等待方，天然并发。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Conn 一条扩展进程连接（stdin 写、stdout 读）。
type Conn struct {
	w io.Writer
	r *bufio.Scanner

	mu      sync.Mutex
	wmu     sync.Mutex // 串行化写入：并发 Call 时两个 JSON 行不能交错
	pending map[string]chan Response
	nextID  atomic.Int64
	closed  bool

	// OnNotify 收到无 id 的通知（服务端主动推）时回调；可为空。
	OnNotify func(method string, params json.RawMessage)
}

// NewConn 用进程的 stdin / stdout 建连接，并启动读循环。
func NewConn(stdin io.Writer, stdout io.Reader) *Conn {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // 单条消息上限 16 MiB
	c := &Conn{w: stdin, r: sc, pending: map[string]chan Response{}}
	go c.readLoop()
	return c
}

// Call 发一个请求并等响应（ctx 取消则返回 ctx.Err()）。
func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	rawID, _ := json.Marshal(id)
	var p json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		p = b
	}
	b, err := Encode(Request{ID: rawID, Method: method, Params: p})
	if err != nil {
		return nil, err
	}
	ch := make(chan Response, 1)
	key := string(rawID)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("extproc: 连接已关闭")
	}
	c.pending[key] = ch
	c.mu.Unlock()

	c.wmu.Lock()
	_, werr := c.w.Write(append(b, '\n'))
	c.wmu.Unlock()
	if werr != nil {
		c.removePending(key)
		return nil, fmt.Errorf("extproc: 写请求：%w", werr)
	}
	select {
	case <-ctx.Done():
		c.removePending(key)
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("extproc: 连接已断开")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// Notify 发一个无 id 的通知（不等响应）。
func (c *Conn) Notify(method string, params any) error {
	var p json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		p = b
	}
	b, err := Encode(Request{Method: method, Params: p})
	if err != nil {
		return err
	}
	c.wmu.Lock()
	_, err = c.w.Write(append(b, '\n'))
	c.wmu.Unlock()
	return err
}

// Close 关闭连接：唤醒所有等待方。
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	for k, ch := range c.pending {
		close(ch)
		delete(c.pending, k)
	}
	c.mu.Unlock()
	return nil
}

func (c *Conn) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

// readLoop 逐行读响应 / 通知。
func (c *Conn) readLoop() {
	for c.r.Scan() {
		line := c.r.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue // 坏行跳过
		}
		// 有 id 且没有 method：响应。
		if len(probe.ID) > 0 && probe.Method == "" {
			var resp Response
			if err := json.Unmarshal(line, &resp); err != nil {
				continue
			}
			key := string(resp.ID)
			c.mu.Lock()
			ch := c.pending[key]
			delete(c.pending, key)
			c.mu.Unlock()
			if ch != nil {
				ch <- resp
			}
			continue
		}
		// 有 method 且没有 id：通知。
		if probe.Method != "" && len(probe.ID) == 0 && c.OnNotify != nil {
			var n struct {
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(line, &n)
			c.OnNotify(probe.Method, n.Params)
		}
	}
	err := c.r.Err()
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		for k, ch := range c.pending {
			close(ch)
			delete(c.pending, k)
		}
	}
	c.mu.Unlock()
}
