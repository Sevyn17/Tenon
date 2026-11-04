// Package session 是智能体的会话状态机：纯函数式、无 IO。
// 宿主调用 Input / ModelDone / SubmitResult 等命令，按返回的 Need 去调模型或执行工具。
// 来源：agentrt/core/ybase-session（tag pre-kernel-freeze），原样拷贝。
package session
