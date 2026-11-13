package httpx

// 协议适配器共用的 SSE 行解析（2026-11-13 审查 L13）。
//
// 三个适配器原先各写一份同样的骨架：bufio.Scanner + 缓冲区上限 + 判 "data:" 前缀 +
// 去空白 + 跳过空行。其中单行上限 8<<20 这个魔数写在三处，改的时候漏一处就会在
// 特定模型上表现为「输出被截断」，且很难定位。
//
// 这里只收「怎么切行」这一层，不收「切出来之后怎么处理」：三个适配器的循环体里
// 有 continue / break / return，强行包成回调会把控制流搅乱。所以暴露 NewSSEScanner
// 与 SSELine，各适配器保持自己的 for 循环形状。

import (
	"bufio"
	"io"
	"strings"
)

// SSE 扫描的缓冲区参数：初始 64 KiB，单行上限 8 MiB。
// 单行上限要够大：某些协议把一整块内容放在一个 data: 行里，模型输出长时单行会很长。
const (
	sseInitialBuf = 64 << 10
	sseMaxLine    = 8 << 20
)

// NewSSEScanner 构造按行扫描 SSE 流的 scanner，缓冲区参数统一在这里。
func NewSSEScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, sseInitialBuf), sseMaxLine)
	return sc
}

// SSELine 取出一行的 data 内容（去掉 "data:" 前缀与首尾空白）。
// 不是 data 行、或内容为空时 ok 为 false，调用方跳过即可。
func SSELine(line string) (data string, ok bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	return data, data != ""
}
