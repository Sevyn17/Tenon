package tasks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Task 只用可 JSON 序列化的字段（为进程外插件协议留路）：往返不丢，进行中不带 ended / exit_code。
func TestTaskJSON(t *testing.T) {
	code := 2
	in := Task{ID: "s1/job-1", Session: "s1", Kind: KindShell, Title: "sleep 1", Status: Failed,
		Summary: "退出码 2", ExitCode: &code, Started: time.Unix(100, 0).UTC(), Ended: time.Unix(105, 0).UTC()}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Task
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.Status != Failed || out.ExitCode == nil || *out.ExitCode != 2 || !out.Ended.Equal(in.Ended) {
		t.Fatalf("往返不一致：%s → %+v", b, out)
	}
	run, _ := json.Marshal(Task{ID: "x", Kind: KindSession, Status: Running, Started: time.Unix(1, 0)})
	if s := string(run); strings.Contains(s, "ended") || strings.Contains(s, "exit_code") {
		t.Fatalf("进行中不应带 ended / exit_code：%s", s)
	}
	for s, want := range map[Status]bool{Running: false, Waiting: false, Done: true, Failed: true, Stopped: true} {
		if s.Ended() != want {
			t.Fatalf("%s.Ended() = %v", s, !want)
		}
	}
}
