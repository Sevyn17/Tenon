// Package ptc 程序化工具调用（Programmatic Tool Calling）：run_code 工具在 JS 沙盒（goja）里执行模型写的程序，
// 程序内的工具调用回到 tool-hub 调用栈（Guard / 幂等 / 产物 / 确认照常），子调用 id 为 `<父调用 id>#<n>`，
// 结果只进工具结果存储，不进会话日志（kernel/docs/90 - 归档/10 - 框架模块/04 - 工具管理器.md D5）。
package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

// 默认值。
const (
	Name          = "run_code"
	DefaultTime   = 120 * time.Second
	MaxTime       = 600 * time.Second
	MaxOutput     = 50 << 10 // 输出（console + 返回值）上限
	MaxSubCalls   = 200      // 单次程序的子调用上限
	MaxCodeLength = 64 << 10
)

// Tool 构造 run_code。hub 为子调用所走的 tool-hub（与本工具注册在同一 hub）。
func Tool(hub *toolhub.Hub) *toolhub.Tool {
	no := false
	return &toolhub.Tool{
		Spec: session.ToolSpec{
			Name: Name,
			Description: "在 JavaScript 沙盒里运行一段程序，适合批量 / 循环 / 过滤式的多次工具调用，只把最终结果带回上下文。" +
				"程序里用 `tools.<名字>(参数对象)` 同步调用当前可用的工具，返回结果文本；工具失败抛出异常（e.message 为错误文本）。" +
				"`console.log` 的输出和程序最后一个表达式的值作为结果。没有文件系统、网络、定时器和 require；只能通过 tools 访问外部。",
			Schema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"JavaScript 程序（同步执行）"},"timeout":{"type":"integer","description":"秒，默认 120，最大 600"}},"required":["code"]}`),
		},
		Risk:     toolhub.Dangerous,
		Parallel: &no,
		Timeout:  MaxTime + 10*time.Second,
		Attrs:    map[string]any{toolhub.AttrEffect: toolhub.EffectOrchestrate},
		Items:    []string{ItemRun},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct {
				Code    string
				Timeout int
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return toolhub.Errorf("参数不合法：%v", err), nil
			}
			if strings.TrimSpace(a.Code) == "" {
				return toolhub.Errorf("code 不能为空"), nil
			}
			if len(a.Code) > MaxCodeLength {
				return toolhub.Errorf("code 超过 %d 字节", MaxCodeLength), nil
			}
			d := DefaultTime
			if a.Timeout > 0 {
				d = time.Duration(a.Timeout) * time.Second
			}
			if d > MaxTime {
				d = MaxTime
			}
			out, err := Run(ctx, hub, cx, a.Code, d)
			if err != nil {
				return toolhub.Errorf("%s", out+err.Error()), nil
			}
			return toolhub.Text(out), nil
		},
	}
}

// 错误种类（DSH ptc-runtime 的分类）。
var (
	ErrTimeout = errors.New("[timeout] 程序超时")
	ErrAbort   = errors.New("[abort] 回合已取消")
	ErrOutput  = errors.New("[output-limit] 输出超过上限")
	ErrCalls   = errors.New("[call-limit] 子调用次数超过上限")
)

// Run 执行程序，返回输出文本。出错时 out 为已产生的输出（可空），err 为分类后的错误。
func Run(ctx context.Context, hub *toolhub.Hub, cx *toolhub.Cx, code string, d time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	vm := goja.New()
	vm.SetFieldNameMapper(goja.UncapFieldNameMapper())

	var (
		mu    sync.Mutex
		buf   strings.Builder
		over  bool
		n     int
		fatal error
	)
	write := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		if buf.Len()+len(s) > MaxOutput {
			over = true
			return
		}
		buf.WriteString(s)
	}
	stop := func(err error) {
		if fatal == nil {
			fatal = err
		}
		vm.Interrupt(err)
	}
	// 外部取消 / 超时中断 VM。
	go func() {
		<-ctx.Done()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			vm.Interrupt(ErrTimeout)
		} else {
			vm.Interrupt(ErrAbort)
		}
	}()

	console := vm.NewObject()
	logf := func(c goja.FunctionCall) goja.Value {
		parts := make([]string, len(c.Arguments))
		for i, v := range c.Arguments {
			parts[i] = show(vm, v)
		}
		write(strings.Join(parts, " ") + "\n")
		if over {
			stop(ErrOutput)
		}
		return goja.Undefined()
	}
	for _, k := range []string{"log", "info", "warn", "error"} {
		_ = console.Set(k, logf)
	}
	_ = vm.Set("console", console)

	tools := vm.NewObject()
	for _, name := range available(hub, cx) {
		name := name
		_ = tools.Set(name, func(c goja.FunctionCall) goja.Value {
			n++
			if n > MaxSubCalls {
				stop(ErrCalls)
				return goja.Undefined()
			}
			args := json.RawMessage("{}")
			if len(c.Arguments) > 0 && !goja.IsUndefined(c.Arguments[0]) && !goja.IsNull(c.Arguments[0]) {
				b, err := json.Marshal(c.Arguments[0].Export())
				if err != nil {
					panic(vm.NewTypeError("参数无法序列化为 JSON：%v", err))
				}
				args = b
			}
			call := session.ToolCall{ID: fmt.Sprintf("%s#%d", cx.CallID, n), Name: name, Args: args}
			run := hub.ExecuteIn(ctx, cx.Toolset, cx.Ctx, []session.ToolCall{call}, nil)
			res, _ := run.Next()
			if res.Err != nil {
				stop(fmt.Errorf("[exception] 子调用 %s 基础设施错误：%v", name, res.Err))
				return goja.Undefined()
			}
			text := contentText(res.Output.Content)
			if res.Output.Status != session.ToolOK {
				panic(vm.NewGoError(errors.New(text)))
			}
			return vm.ToValue(text)
		})
	}
	_ = vm.Set("tools", tools)

	v, err := vm.RunString(code)
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if err != nil {
		var ie *goja.InterruptedError
		if errors.As(err, &ie) {
			if e, ok := ie.Value().(error); ok {
				return out, e
			}
		}
		if fatal != nil {
			return out, fatal
		}
		var ex *goja.Exception
		if errors.As(err, &ex) {
			return out, fmt.Errorf("[exception] %s", ex.Error())
		}
		return out, fmt.Errorf("[exception] %v", err)
	}
	if v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		s := show(vm, v)
		if len(out)+len(s) > MaxOutput {
			return out, ErrOutput
		}
		out += s
	}
	if out == "" {
		out = "（程序没有输出）"
	}
	return out, nil
}

// available 程序可调的工具：会话视图里的可用工具（不含 run_code 自身）。
func available(hub *toolhub.Hub, cx *toolhub.Cx) []string {
	var names []string
	if cx.Toolset != nil {
		names = cx.Toolset.Available()
	}
	var out []string
	for _, n := range names {
		if n == Name {
			continue
		}
		if _, ok := hub.Get(n); ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func show(vm *goja.Runtime, v goja.Value) string {
	if s, ok := v.Export().(string); ok {
		return s
	}
	if o, ok := v.(*goja.Object); ok && o.ClassName() != "Function" {
		if b, err := json.Marshal(o.Export()); err == nil {
			return string(b)
		}
	}
	return v.String()
}

func contentText(c session.Content) string {
	var b strings.Builder
	for _, p := range c {
		b.WriteString(p.Text)
	}
	return b.String()
}
