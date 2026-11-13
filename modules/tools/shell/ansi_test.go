package shell

import (
	"testing"
)

// D3 的根因回归：增量读的切点落在转义序列中间时，残缺的尾部必须留到下次读，
// 不能把它后面的正常文本当成序列参数吞掉（CI 上偶发丢 "end" 就是这个）。
//
// 注意：残缺序列拼上下一段后**可能恰好构成合法序列**（"\x1b[3" + "end" = "\x1b[3e"），
// 那是字节流本身的歧义，清洗层无法区分。所以这里只断言「留了多少、这次吐了什么」，
// 不假设补齐后一定完整——真正的保证是调用方不推进读偏移，等序列闭合再吐。
func TestStripANSIHoldingKeepsPartialTail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		hold int
	}{
		{name: "CSI 被切断", in: "start\x1b[3", want: "start", hold: 3},
		{name: "OSC 被切断", in: "a\x1b]0;title", want: "a", hold: 9},
		{name: "末尾孤立 ESC", in: "x\x1b", want: "x", hold: 1},
		{name: "完整序列不受影响", in: "\x1b[31mred\x1b[0m", want: "red", hold: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, hold := stripANSIHolding(c.in)
			if got != c.want || hold != c.hold {
				t.Fatalf("stripANSIHolding(%q) = %q, %d；期望 %q, %d", c.in, got, hold, c.want, c.hold)
			}
		})
	}
}

// 关键保证：留在尾部的字节原文一字不差地保留，调用方回退偏移后下次能原样再读到。
func TestStripANSIHoldingTailIsVerbatim(t *testing.T) {
	in := "out\x1b[38;5;"
	_, hold := stripANSIHolding(in)
	if hold <= 0 {
		t.Fatalf("应报告残缺尾部：hold=%d", hold)
	}
	if got := in[len(in)-hold:]; got != "\x1b[38;5;" {
		t.Fatalf("保留的尾部应与原文一致：%q", got)
	}
}

// 完整序列照旧全部去掉；单次执行路径（stripANSI）不留残缺尾部。
func TestStripANSIRemovesCompleteSequences(t *testing.T) {
	if got := stripANSI("\x1b[31mred\x1b[0m"); got != "red" {
		t.Fatalf("完整序列应去掉：%q", got)
	}
	if got := stripANSI("plain"); got != "plain" {
		t.Fatalf("无序列应原样：%q", got)
	}
	if got := stripANSI("tail\x1b[3"); got != "tail" {
		t.Fatalf("单次执行不留残缺尾部：%q", got)
	}
}
