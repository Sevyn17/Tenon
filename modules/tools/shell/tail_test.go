package shell

// Tail（tasks.Reader）：按任务 id 只读地取后台 job 输出尾部，不推进 job_output 的读偏移；别的会话 / 不存在的 id 返回 false。

import (
	"strings"
	"testing"
	"time"
)

func TestTailReadOnly(t *testing.T) {
	s := NewShell(t.TempDir())
	out, isErr := callT(t, s, "shell", `{"command":`+q("echo tail-marker-1")+`,"background":true}`)
	if isErr {
		t.Fatalf("启动失败：%q", out)
	}
	id := jobID(t, out)
	task := "/" + id // callT 的会话 id 为空
	deadline := time.Now().Add(20 * time.Second)
	var text string
	for time.Now().Before(deadline) {
		var ok bool
		text, _, ok = s.Tail(task, 0)
		if !ok {
			t.Fatalf("Tail 应找到 %s", task)
		}
		if strings.Contains(text, "tail-marker-1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(text, "tail-marker-1") {
		t.Fatalf("Tail 输出：%q", text)
	}
	// 只取最后 4 字节：skipped 等于之前的字节数
	short, skipped, ok := s.Tail(task, 4)
	if !ok || len(short) > 4 || skipped == 0 {
		t.Fatalf("tail=4：%q skipped=%d ok=%v", short, skipped, ok)
	}
	// 不推进读偏移：job_output 仍能读到全部输出
	if o, _ := callT(t, s, "job_output", `{"id":"`+id+`","wait":true,"timeout":20}`); !strings.Contains(o, "tail-marker-1") {
		t.Fatalf("job_output 应仍有完整输出：%q", o)
	}
	for _, bad := range []string{"other/" + id, "/job-999", "noslash"} {
		if _, _, ok := s.Tail(bad, 0); ok {
			t.Fatalf("%q 不应找到", bad)
		}
	}
}
