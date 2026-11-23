// Package tasks 后台工作的登记口（Mortise docs/62 K2）。
//
// 标准模块（shell 后台 job、subagent 子会话……）在后台工作开始、更新、结束时各报一次；
// 谁要看（任务表、面板、通知）由产品提供 [Sink]，经 [Key] 注册成服务 "service:tasks"。
// 没人提供时模块不报，行为与没有本包时一样。tenon 内不保存任务、不做面板。
//
// 接口只用可 JSON 序列化的类型（Task 全是值字段），以后可直接映射到进程外插件协议消息。
package tasks

import (
	"time"

	"github.com/Sevyn17/tenon/runtime/ext"
)

// Service 任务登记服务名。可选：标准模块在 Requires 里写 Key.Maybe()。
const Service = "service:tasks"

// Key 类型化键：component.GetOptional(cx, tasks.Key)。
var Key = ext.NewKey[Sink](Service)

// Status 任务状态。
type Status string

// 五种状态。Running / Waiting 是进行中，其余是终态。
const (
	Running Status = "running"
	Waiting Status = "waiting" // 进行中但在等外部（确认、输入）
	Done    Status = "done"
	Failed  Status = "failed"
	Stopped Status = "stopped" // 被终止 / 取消
)

// Ended 是否终态。
func (s Status) Ended() bool { return s == Done || s == Failed || s == Stopped }

// 标准模块使用的 Kind。产品自己的来源用自己的命名（如 "acme.terminal"）。
const (
	KindShell    = "shell"    // shell 后台 job
	KindSubagent = "subagent" // 子智能体会话
)

// Task 一项后台工作的快照。每次回调传完整快照，接收方按 ID 覆盖即可。
type Task struct {
	// ID 在 Kind 内唯一（shell 为 "<会话>/job-N"，subagent 为子会话 id）。
	ID string `json:"id"`
	// Session 发起它的会话 id（可空）。
	Session string `json:"session,omitempty"`
	// Kind 来源（KindShell / KindSubagent / 产品自定）。
	Kind string `json:"kind"`
	// Title 一行说明（命令行、任务摘要）。
	Title  string `json:"title,omitempty"`
	Status Status `json:"status"`
	// Summary 结束时的一行结果或错误说明（可空）。
	Summary string `json:"summary,omitempty"`
	// ExitCode 进程类任务结束时的退出码；不适用或未结束为 nil。
	ExitCode *int      `json:"exit_code,omitempty"`
	Started  time.Time `json:"started"`
	// Ended 终态时的结束时间；进行中为零值。
	Ended time.Time `json:"ended,omitzero"`
}

// Sink 接收任务登记。实现须并发安全、尽快返回（调用方可能在工具调用或进程等待的协程里同步调用）。
type Sink interface {
	Started(Task)
	Updated(Task)
	Ended(Task)
}

// Reader 读一项任务输出的尾部（最多 max 字节）：text 为尾部文本，skipped 为尾部之前未返回的字节数；
// 任务不存在（或输出已回收）时 ok 为 false。只读：不影响模块自己的读偏移。须并发安全。
type Reader func(id string, max int) (text string, skipped int64, ok bool)

// OutputSink 可选：Sink 再实现它，有输出的模块在装配时按 Kind 交出 Reader（Ended 回调期间也可调用）。
type OutputSink interface {
	SetReader(kind string, r Reader)
}
