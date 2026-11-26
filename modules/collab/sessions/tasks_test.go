package sessions

// K2（Mortise docs/62）：委派开始 / 结束向 tasks.Sink 登记。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/tasks"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

type recSink struct {
	mu sync.Mutex
	ev []string
	ts []tasks.Task
}

func (r *recSink) add(e string, t tasks.Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ev, r.ts = append(r.ev, e), append(r.ts, t)
}
func (r *recSink) Started(t tasks.Task) { r.add("started", t) }
func (r *recSink) Updated(t tasks.Task) { r.add("updated", t) }
func (r *recSink) Ended(t tasks.Task)   { r.add("ended", t) }

type failModel struct{}

func (failModel) Stream(context.Context, session.Request, func(string)) (session.AssistantItem, session.StopReason, error) {
	return session.AssistantItem{}, session.StopReason{}, errors.New("boom")
}

func taskHost(t *testing.T, m agent.Model) *host.Host {
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	return &host.Host{Workspace: "w", DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		return &agent.Agent{Store: db, SessionID: id, Model: m, Hub: hub}, &session.Config{Model: "m", Policy: session.DefaultPolicy()}, nil
	}}
}

func TestSubagentReportsTasks(t *testing.T) {
	h := taskHost(t, echoModel{})
	sink := &recSink{}
	tool := ToolWithTasks(h, h.New, sink)
	cx := &toolhub.Cx{SessionID: "p", CallID: "call-0123456789ab", Tool: tool}
	if _, err := tool.Call(context.Background(), cx, json.RawMessage(`{"task":"做事\n第二行"}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sink.ev, ",") != "started,ended" {
		t.Fatalf("登记序列：%v", sink.ev)
	}
	st, en := sink.ts[0], sink.ts[1]
	if st.ID != "p/sub-456789ab" || st.Session != "p" || st.Kind != tasks.KindSession || st.Title != "做事" || st.Status != tasks.Running {
		t.Fatalf("started：%+v", st)
	}
	if en.ID != st.ID || en.Status != tasks.Done || en.Summary != "re:做事" || en.Ended.IsZero() || en.ExitCode != nil {
		t.Fatalf("ended：%+v", en)
	}
}

func TestSubagentReportsFailed(t *testing.T) {
	h := taskHost(t, failModel{})
	sink := &recSink{}
	tool := ToolWithTasks(h, h.New, sink)
	cx := &toolhub.Cx{SessionID: "p", CallID: "call-0000000000ff", Tool: tool}
	_, _ = tool.Call(context.Background(), cx, json.RawMessage(`{"task":"x"}`))
	if len(sink.ts) != 2 || sink.ts[1].Status != tasks.Failed || sink.ts[1].Summary == "" {
		t.Fatalf("失败应登记 failed：%v %+v", sink.ev, sink.ts)
	}
}
