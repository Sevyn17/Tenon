package shell

import "testing"

func TestStripANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain 中文", "plain 中文"},
		{"\x1b[32;1mok\x1b[0m done", "ok done"},
		{"a\x1b[2Kb\x1b[1Gc", "abc"},
		{"\x1b]0;title\x07after", "after"},
		{"\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"x\x1b=y", "xy"},
		{"\u009b31mred", "red"},
		{"tail\x1b", "tail"},
		{"\x1b[?25lhide\x1b[?25h", "hide"},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
