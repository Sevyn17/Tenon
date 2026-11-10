package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// 组件事件日志（框架设计 04 4.6，T4b）。
//
// 与会话事件表 session_events 是两条：那张表按会话、服务界面订阅、有保留期；
// 这一条按组件、全局一条序号、没有保留期。不在 Store 上，与 CollectionStore 同一规则
// （2026-11-04 审查 P1-53）：没实现的存储照样能用，登记了订阅的组件启动时报"存储不支持事件日志"。

// EventReadDefault / EventReadMax 是 Read 的条数缺省与上限（约束清单「事件日志」）。
const (
	EventReadDefault = 100
	EventReadMax     = 1000
)

// EventNameMax 事件名的最大长度（约束清单「事件日志」）。
const EventNameMax = 64

// ComponentEvent 一条组件事件。
type ComponentEvent struct {
	Seq    int64 // 存储分配，从 1 起，全局单调
	At     int64 // 毫秒；0 由存储补当前时间
	Source string
	Name   string
	Data   json.RawMessage // 载荷；空写 {}
}

// EventLog 组件事件日志。实现它的存储经类型断言使用。
type EventLog interface {
	// Append 追加一条。Seq 由存储分配；同一事务里追加的按调用顺序相邻，提交后才可见。
	Append(ctx context.Context, e ComponentEvent) (seq int64, err error)
	// Read 返回 seq > after 的事件，按 seq 升序，最多 limit 条。limit 缺省 EventReadDefault、超出按 EventReadMax。
	Read(ctx context.Context, after int64, limit int) ([]ComponentEvent, error)
	// Cursor 读订阅者的读位点。name 是 "<组件>.<事件名>"。没有记录返回 0。
	Cursor(ctx context.Context, name string) (int64, error)
	// SetCursor 记下读位点。seq 小于已记下的值时拒绝：读位点只前进，框架不会把同一条再交给订阅者。
	SetCursor(ctx context.Context, name string, seq int64) error
}

// eventName 事件名的字符集（约束清单「事件日志」）：小写字母、数字、点，可选末尾一个星号。
var eventName = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9]+)*(\.\*)?$`)

// CheckEventName 校验事件名。两个实现与登记入口都走它，规则只写一处。
func CheckEventName(name string) error {
	if !eventName.MatchString(name) || len(name) > EventNameMax {
		return fmt.Errorf("store: 事件名 %q 不合法（小写字母、数字、点，可选末尾一个 .*，最长 %d）", name, EventNameMax)
	}
	return nil
}

// EventReadLimit 把 Read 的 limit 归一到缺省与上限之间。
func EventReadLimit(n int) int {
	if n <= 0 {
		return EventReadDefault
	}
	if n > EventReadMax {
		return EventReadMax
	}
	return n
}
