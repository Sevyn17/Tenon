package shell

// K2（Mortise docs/62）：后台 job 开始 / 结束向 tasks.Sink 登记；没有 Sink 时不报（其余测试即此情形）。

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/runtime/tasks"
)

type recSink struct {
	mu  sync.Mutex
	evs []string
	ts  []tasks.Task
}

func (r *recSink) add(ev string, t tasks.Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
	r.ts = append(r.ts, t)
}
func (r *recSink) Started(t tasks.Task) { r.add("started", t) }
func (r *recSink) Updated(t tasks.Task) { r.add("updated", t) }
func (r *recSink) Ended(t tasks.Task)   { r.add("ended", t) }

func (r *recSink) snap() ([]string, []tasks.Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.evs...), append([]tasks.Task(nil), r.ts...)
}

func jobID(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, "job-")
	if i < 0 {
		t.Fatalf("启动输出里没有 job id：%q", out)
	}
	j := i + 4
	for j < len(out) && out[j] >= '0' && out[j] <= '9' {
		j++
	}
	return out[i:j]
}

func TestShellJobReportsTasks(t *testing.T) {
	s := NewShell(t.TempDir())
	sink := &recSink{}
	s.Tasks = sink
	out, isErr := callT(t, s, "shell", `{"command":`+q("echo hi")+`,"background":true}`)
	if isErr {
		t.Fatalf("启动失败：%q", out)
	}
	id := jobID(t, out)
	if out, _ := callT(t, s, "job_output", `{"id":"`+id+`","wait":true,"timeout":20}`); !strings.Contains(out, "已结束") {
		t.Fatalf("job 应已结束：%q", out)
	}
	evs, ts := sink.snap()
	if strings.Join(evs, ",") != "started,ended" {
		t.Fatalf("登记序列：%v", evs)
	}
	st, en := ts[0], ts[1]
	if st.ID != "/"+id || st.Kind != tasks.KindShell || st.Status != tasks.Running || st.Title != "echo hi" || st.Started.IsZero() {
		t.Fatalf("started：%+v", st)
	}
	if en.ID != st.ID || en.Status != tasks.Done || en.ExitCode == nil || *en.ExitCode != 0 || en.Ended.IsZero() {
		t.Fatalf("ended：%+v", en)
	}
	// 可 JSON 序列化（为进程外插件协议留路）。
	b, err := json.Marshal(en)
	if err != nil || !strings.Contains(string(b), `"status":"done"`) || !strings.Contains(string(b), `"exit_code":0`) {
		t.Fatalf("序列化：%s %v", b, err)
	}
	var back tasks.Task
	if err := json.Unmarshal(b, &back); err != nil || back.ID != en.ID || !back.Ended.Equal(en.Ended) {
		t.Fatalf("反序列化：%+v %v", back, err)
	}
}

func TestShellJobKillReportsStopped(t *testing.T) {
	s := NewShell(t.TempDir())
	sink := &recSink{}
	s.Tasks = sink
	out, _ := callT(t, s, "shell", `{"command":`+q(sleepCmd(30))+`,"background":true}`)
	id := jobID(t, out)
	if out, isErr := callT(t, s, "job_kill", `{"id":"`+id+`"}`); isErr {
		t.Fatalf("终止失败：%q", out)
	}
	evs, ts := sink.snap()
	if strings.Join(evs, ",") != "started,ended" {
		t.Fatalf("登记序列：%v", evs)
	}
	if ts[1].Status != tasks.Stopped || !ts[1].Status.Ended() {
		t.Fatalf("终止后应为 stopped：%+v", ts[1])
	}
}
