package host

import (
	"strings"
	"testing"
)

// 派出会话的标题取任务首个非空行（Mortise t3-shots #3），超长截断加省略号。
func TestTaskTitle(t *testing.T) {
	if got := TaskTitle("\n\n  看一下   CI 失败  \n第二行"); got != "看一下 CI 失败" {
		t.Fatalf("首行：%q", got)
	}
	long := strings.Repeat("字", TaskTitleRunes+10)
	if got := []rune(TaskTitle(long)); len(got) != TaskTitleRunes+1 || got[len(got)-1] != '…' {
		t.Fatalf("截断：%q", string(got))
	}
	if TaskTitle("  \n ") != "" {
		t.Fatal("全空白应为空")
	}
}
