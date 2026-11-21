// Package guard提供应用协议层及其传输实现共用的 panic边界。
package guard

import (
	"log"
	"runtime/debug"
)

// Recover记录被捕获的 panic及堆栈，再执行清理。必须直接 defer到需要守护的函数里；
// onPanic通常用于把命令改成 internal响应，或关闭发生故障的连接。
func Recover(what string, onPanic func()) {
	if r := recover(); r != nil {
		log.Printf("%s panic：%v\n%s", what, r, debug.Stack())
		if onPanic != nil {
			onPanic()
		}
	}
}
