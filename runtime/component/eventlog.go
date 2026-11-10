package component

// 组件事件订阅（框架设计 04 4.6，T4b）。
//
// 组件在 Setup 里用 On 登记订阅，框架在 Assembly.Start 里按读位点投递：
// 存储打开之后、组件 Start 之前。每个订阅一个协程，处理成功才推进读位点，
// 失败重试有限次后跳过，不进发布方的调用栈。

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
)

// 失败重试（约束清单「事件日志」）：三次，间隔从 EventRetryBackoff 起翻倍。
const (
	EventRetries      = 3
	EventRetryBackoff = time.Second
)

// EventHandler 订阅处理函数。返回错误时框架重试，不传给发布方。
type EventHandler func(ctx context.Context, e store.ComponentEvent) error

// subscription 一个组件登记的订阅。
type subscription struct {
	plugin  string
	name    string
	prefix  bool
	handler EventHandler
}

// On 订阅组件事件（04 4.6）。
//
// name 支持末尾 .* 表示前缀（"session.*" 收 session. 开头的全部）。同一组件对同一 name 只能登记一次。
// 处理函数返回错误时框架重试，不进发布方的调用栈。
func (cx *Registrar) On(name string, fn EventHandler) error {
	if err := store.CheckEventName(name); err != nil {
		return fmt.Errorf("插件 %s：%w", cx.plugin, err)
	}
	if fn == nil {
		return fmt.Errorf("插件 %s：订阅 %s 缺处理函数", cx.plugin, name)
	}
	for _, s := range cx.asm.subscriptions {
		if s.plugin == cx.plugin && s.name == name {
			return fmt.Errorf("插件 %s 重复订阅 %s", cx.plugin, name)
		}
	}
	cx.asm.subscriptions = append(cx.asm.subscriptions, subscription{
		plugin: cx.plugin, name: name, prefix: strings.HasSuffix(name, ".*"), handler: fn})
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "event", Name: name})
	return nil
}

// cursorName 读位点的键：订阅者是组件，不是事件名，同一组件的多个订阅各自记。
func (s subscription) cursorName() string { return s.plugin + "." + s.name }

// match 这条事件是否属于本订阅。
func (s subscription) match(name string) bool {
	if s.prefix {
		return strings.HasPrefix(name, strings.TrimSuffix(s.name, "*"))
	}
	return name == s.name
}

// deliverEvents 启动全部已登记订阅的投递（T4b 执行方案 3.2）。
//
// 与 OpenCollections 放在同一处：存储打开之后、第一个用到它的插件 Start 之前。
// 没有订阅时直接返回，存储不必实现 EventLog。
func (a *Assembly) deliverEvents(ctx context.Context, sc *Scope, db store.Store) error {
	if len(a.subscriptions) == 0 {
		return nil
	}
	el, ok := db.(store.EventLog)
	if !ok {
		return fmt.Errorf("存储不支持事件日志（%s 登记了订阅）", a.subscriptions[0].plugin)
	}
	for _, s := range a.subscriptions {
		sc.Go("event:"+s.cursorName(), func(ctx context.Context) error {
			runSubscription(ctx, el, s, a.eventBackoff)
			return nil
		})
	}
	return nil
}

// runSubscription 从一个订阅的读位点起逐段取、逐条处理，直到作用域取消。backoff 是失败重试的起始间隔，零值取 EventRetryBackoff。
//
// 返回 nil：Scope.Go 把返回错误当失败重启，而这里的失败是单条事件的，重启会把读位点之前的再投一遍。
func runSubscription(ctx context.Context, el store.EventLog, s subscription, backoff time.Duration) {
	if backoff <= 0 {
		backoff = EventRetryBackoff
	}
	cur, err := el.Cursor(ctx, s.cursorName())
	if err != nil {
		log.Printf("component: 插件 %s 的订阅 %s 读读位点失败：%v", s.plugin, s.name, err)
		return
	}
	for ctx.Err() == nil {
		evs, err := el.Read(ctx, cur, 0)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("component: 插件 %s 的订阅 %s 读事件失败：%v", s.plugin, s.name, err)
			if !waitBackoff(ctx, backoff) {
				return
			}
			continue
		}
		advanced := false
		for _, e := range evs {
			if !s.match(e.Name) {
				cur = e.Seq
				continue
			}
			if err := deliverOne(ctx, s, e, backoff); err != nil {
				log.Printf("component: 插件 %s 的订阅 %s 处理事件 %d 三次仍失败，跳过：%v", s.plugin, s.name, e.Seq, err)
			}
			cur = e.Seq
			advanced = true
		}
		if advanced {
			if err := el.SetCursor(ctx, s.cursorName(), cur); err != nil && ctx.Err() == nil {
				log.Printf("component: 插件 %s 的订阅 %s 记读位点 %d 失败：%v", s.plugin, s.name, cur, err)
			}
		}
		if len(evs) == 0 {
			// 读位点先记下再等：停在这里被取消时，重启从已处理的位置继续，不重复投递。
			if !waitBackoff(ctx, 50*time.Millisecond) {
				return
			}
		}
	}
}

// deliverOne 处理一条，失败按 backoff 起翻倍重试三次。三次仍失败返回最后的错误。
func deliverOne(ctx context.Context, s subscription, e store.ComponentEvent, backoff time.Duration) error {
	delay := backoff
	var err error
	for attempt := 0; attempt <= EventRetries; attempt++ {
		if attempt > 0 {
			log.Printf("component: 插件 %s 的订阅 %s 处理事件 %d 失败，重试：%v", s.plugin, s.name, e.Seq, err)
			if !waitBackoff(ctx, delay) {
				return ctx.Err()
			}
			delay *= 2
		}
		err = runSafe(ctx, func(ctx context.Context) error { return s.handler(ctx, e) })
		if err == nil || ctx.Err() != nil {
			return err
		}
	}
	return err
}

// waitBackoff 等一段时间或作用域取消。返回 false 表示该停了。
func waitBackoff(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}
