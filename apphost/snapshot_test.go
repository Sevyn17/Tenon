package apphost

// 真实装配回归快照（计划/01 T1b-0）：走 apphost.Start + BuildApp（profile 缺省插件 + subagent + workgroup，
// 即 assemble.Builder 与全部会话点位），只把模型换成假模型，录四种场景的完整模型请求：
// 主会话、子智能体、工作组组长、工作组成员。T1b-1 改插件装配时靠它证明"模型看到的内容不变"。
// 有意变化时用 `go test ./apphost（在 framework/ 下） -run TestAssembledSnapshot -update` 重录。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"tenon/runtime/agent"
	"tenon/runtime/agentdef"
	"tenon/runtime/host"
	"tenon/runtime/session"
	"tenon/runtime/store"
)

var update = flag.Bool("update", false, "重录回归快照")

// scripted 假模型：按会话角色记请求；回复按最后一条消息决定（见 reply）。
type scripted struct {
	mu   sync.Mutex
	reqs map[string][]session.Request
}

type roleModel struct {
	s    *scripted
	role string
}

func role(id string) string {
	switch {
	case strings.Contains(id, "/sub-"):
		return "subagent"
	case strings.Contains(id, "/m-"):
		return "member"
	}
	return id
}

func (m roleModel) Stream(_ context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	m.s.mu.Lock()
	m.s.reqs[m.role] = append(m.s.reqs[m.role], req)
	m.s.mu.Unlock()
	last := req.Messages[len(req.Messages)-1]
	txt := ""
	for _, p := range last.Content {
		txt += p.Text
	}
	call := func(name, args string) (session.AssistantItem, session.StopReason, error) {
		return session.AssistantItem{ToolCalls: []session.ToolCall{{Name: name, Args: json.RawMessage(args)}}},
			session.StopReason{V: "tool_calls"}, nil
	}
	switch {
	case last.Role == "tool":
		txt = "done"
	case strings.Contains(txt, "派子智能体"):
		return call("subagent", `{"task":"子任务"}`)
	case strings.Contains(txt, "派成员"):
		return call("team_spawn", `{"name":"alice","task":"成员任务"}`)
	default:
		txt = "re:" + txt
	}
	if onText != nil {
		onText(txt)
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart(txt)}}, session.StopReason{V: "stop"}, nil
}

func (s *scripted) count(r string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs[r])
}

func TestAssembledSnapshot(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	extra := filepath.Join(home, "extra.toml")
	if err := os.WriteFile(extra, []byte("[[plugin]]\nname = \"subagent\"\nenabled = true\n\n[[plugin]]\nname = \"workgroup\"\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Start(StartOptions{Flags: Flags{Workdir: root, Profile: extra, Yes: true, MaxSteps: -1},
		App: AppOptions{MaxSteps: -1, ModesWorkdir: root}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	built, err := rt.BuildApp()
	if err != nil {
		t.Fatal(err)
	}
	s := &scripted{reqs: map[string][]session.Request{}}
	wrap := func(f host.Factory) host.Factory {
		return func(id, def string) (*agent.Agent, *session.Config, error) {
			a, cfg, err := f(id, def)
			if a != nil {
				a.Model = roleModel{s: s, role: role(id)}
			}
			return a, cfg, err
		}
	}
	rt.Host.New, rt.Host.Child = wrap(rt.Host.New), wrap(rt.Host.Child)
	for _, e := range built.Ready(context.Background()) {
		t.Fatal(e)
	}

	ctx := context.Background()
	send := func(id, text string) {
		t.Helper()
		hd, err := rt.Host.Open(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := hd.Do(host.Command{Kind: host.Send, ItemID: host.NewItemID(), Text: text}); err != nil {
			t.Fatal(err)
		}
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	// 1. 主会话普通对话；2. 主会话派子智能体（同步，子会话一回合）
	send("main", "你好")
	send("main", "请派子智能体")
	// 3. 组长派成员（异步）：成员做完后结果作为新消息送回组长，组长再调一次模型
	send("lead", "请派成员")
	deadline := time.Now().Add(20 * time.Second)
	for s.count("lead") < 3 || s.count("member") < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("等不到成员结果送回组长：lead=%d member=%d", s.count("lead"), s.count("member"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hd, ok := rt.Host.Get("lead"); ok {
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}

	s.mu.Lock()
	keys := make([]string, 0, len(s.reqs))
	for k := range s.reqs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type rec struct {
		Role     string
		Requests []session.Request
	}
	var out []rec
	for _, k := range keys {
		out = append(out, rec{k, normalizeIDs(s.reqs[k])})
	}
	s.mu.Unlock()
	got, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(scrub(got, root, home), '\n')
	compare(t, filepath.Join("testdata", "snapshot_assembled.json"), got)

	// T1b-2c：角色改由内置定义模板判定（不再按会话 id 猜），会话行记定义名与哈希；
	// 模型请求与 T1b-1 完全一致（上面的快照无差异即证明「行为不变」）。
	if rt.Rt.Defs == nil {
		t.Fatal("装配结果缺定义来源")
	}
	for _, name := range []string{"subagent", "workgroup-leader", "workgroup-member"} {
		if _, _, err := agentdef.Resolve(ctx, rt.Rt.Defs, agentdef.Scope{Tenant: store.Tenant}, name); err != nil {
			t.Fatalf("内置定义 %s：%v", name, err)
		}
	}
	infos, err := rt.Host.DB.ListSessions(ctx, 0, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, info := range infos {
		l, err := rt.Host.DB.Load(ctx, info.ID, nil)
		if err != nil {
			t.Fatalf("读会话 %s：%v", info.ID, err)
		}
		switch l.Meta.Origin {
		case "subagent":
			if l.Meta.AgentID != "subagent" || l.Meta.AgentHash == "" {
				t.Fatalf("子智能体会话 %s 的定义 = %q / %q", info.ID, l.Meta.AgentID, l.Meta.AgentHash)
			}
			seen["subagent"] = true
		case "member":
			if l.Meta.AgentID != "workgroup-member" || l.Meta.AgentHash == "" {
				t.Fatalf("成员会话 %s 的定义 = %q / %q", info.ID, l.Meta.AgentID, l.Meta.AgentHash)
			}
			seen["member"] = true
		}
	}
	if !seen["subagent"] || !seen["member"] {
		t.Fatalf("没跑到子智能体 / 成员场景：%v", seen)
	}
}

// normalizeIDs 把工具调用 id 换成按出现顺序的 idN（id 是 uuid，每次不同）。
func normalizeIDs(reqs []session.Request) []session.Request {
	ids := map[string]string{}
	id := func(s string) string {
		if s == "" {
			return ""
		}
		if v, ok := ids[s]; ok {
			return v
		}
		v := fmt.Sprintf("id%d", len(ids)+1)
		ids[s] = v
		return v
	}
	out := make([]session.Request, len(reqs))
	for i, r := range reqs {
		b, _ := json.Marshal(r)
		var c session.Request
		_ = json.Unmarshal(b, &c)
		for j := range c.Messages {
			m := &c.Messages[j]
			m.ToolCallID = id(m.ToolCallID)
			for k := range m.ToolCalls {
				m.ToolCalls[k].ID = id(m.ToolCalls[k].ID)
				m.ToolCalls[k].WireID = id(m.ToolCalls[k].WireID)
			}
		}
		out[i] = c
	}
	return out
}

var subID = regexp.MustCompile(`/sub-[0-9A-Za-z_-]{1,8}`)

// scrub 去掉随运行变化的部分：临时目录路径、子智能体会话 id 尾段。
func scrub(b []byte, paths ...string) []byte {
	s := string(b)
	for i, p := range paths {
		name := fmt.Sprintf("<dir%d>", i)
		esc, _ := json.Marshal(p)
		s = strings.ReplaceAll(s, strings.Trim(string(esc), `"`), name)
		s = strings.ReplaceAll(s, filepath.ToSlash(p), name)
	}
	return []byte(subID.ReplaceAllString(s, "/sub-X"))
}

func compare(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("缺快照，先用 -update 录制: %v", err)
	}
	if string(want) != string(got) {
		_ = os.WriteFile(path+".got", got, 0o644)
		t.Fatalf("模型请求与快照不同（有意变化用 -update 重录；本次结果写在 %s.got）", path)
	}
}
