package sessions

// 委派参数开关（Mortise docs/62 K5、docs/56 第 2 节）：开着的参数才出现在 schema 里；关着的即使模型传了也不生效；
// 打开的参数经 host.SpawnWith 交给子会话构造，收窄说明写进工具结果。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func schemaProps(t *testing.T, tool *toolhub.Tool) map[string]any {
	t.Helper()
	var s struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(tool.Spec.Schema, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Required) != 1 || s.Required[0] != "task" {
		t.Fatalf("required = %v", s.Required)
	}
	return s.Properties
}

func TestParamsSchema(t *testing.T) {
	h := &host.Host{}
	// 缺省开关（docs/56 决定 2）：system_prompt 开、model 关、mode 开、tools 开；没有 goal。
	p := schemaProps(t, ToolWithParams(h, nil, nil, (*PluginConfig)(nil).Params()))
	for _, k := range []string{"task", "system_prompt", "mode", "tools"} {
		if _, ok := p[k]; !ok {
			t.Errorf("缺省开关下应有 %s：%v", k, p)
		}
	}
	for _, k := range []string{"model", "goal"} {
		if _, ok := p[k]; ok {
			t.Errorf("缺省开关下不应有 %s", k)
		}
	}
	// 全关 = 原来的 schema（Tool / ToolWithTasks 兼容）。
	p = schemaProps(t, Tool(h, nil))
	for _, k := range []string{"system_prompt", "model", "mode", "tools"} {
		if _, ok := p[k]; ok {
			t.Errorf("Tool() 不应开放 %s", k)
		}
	}
	p = schemaProps(t, ToolWithParams(h, nil, nil, Params{Model: true}))
	if _, ok := p["model"]; !ok {
		t.Error("开了 model 应出现")
	}
}

func TestParamsPassedToSpawn(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	var got []host.SpawnOptions
	h := &host.Host{DB: db}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		if o, ok := h.SpawnOptionsFor(id); ok {
			got = append(got, o)
			if o.OnNarrow != nil && o.Mode == "full" {
				o.OnNarrow("mode 已收窄为 workspace（主会话当前档位）")
			}
		} else {
			got = append(got, host.SpawnOptions{})
		}
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		return &agent.Agent{Store: db, SessionID: id, Model: echoModel{}, Hub: hub},
			&session.Config{Model: "m", Policy: session.DefaultPolicy()}, nil
	}
	call := func(tool *toolhub.Tool, cid, args string) string {
		t.Helper()
		out, err := tool.Call(ctx, &toolhub.Cx{SessionID: "p", CallID: cid, Tool: tool}, json.RawMessage(args))
		if err != nil || out.Status.State != "ok" {
			t.Fatalf("委派失败：%v %+v", err, out)
		}
		return contentText(out.Content)
	}
	args := `{"task":"查","system_prompt":"审查员","model":"fast","mode":"full","tools":["read_file"]}`

	// 全开：四个参数都传到构造；收窄说明进结果。
	txt := call(ToolWithParams(h, h.New, nil, Params{SystemPrompt: true, Model: true, Mode: true, Tools: true}), "call-00000001", args)
	o := got[len(got)-1]
	if o.SystemPrompt != "审查员" || o.Model != "fast" || o.Mode != "full" || len(o.Tools) != 1 || o.Tools[0] != "read_file" {
		t.Fatalf("参数没有传到构造：%+v", o)
	}
	if !strings.Contains(txt, "[mode 已收窄为 workspace") || !strings.Contains(txt, "re:查") {
		t.Fatalf("结果应写明已收窄：%q", txt)
	}

	// 全关：模型传了也不生效。
	call(Tool(h, h.New), "call-00000002", args)
	if o := got[len(got)-1]; !o.IsZero() {
		t.Fatalf("关着的参数不应生效：%+v", o)
	}

	// 新建时选项记进子会话行，重开时构造方可读回。
	l, err := db.Load(ctx, "p/sub-00000001", nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]host.SpawnOptions
	_ = json.Unmarshal(l.Meta.Meta, &m)
	if s := m[host.MetaSpawn]; s.Mode != "full" || s.SystemPrompt != "审查员" {
		t.Fatalf("会话行应记创建时的覆盖：%s", l.Meta.Meta)
	}
	if _, ok := h.SpawnOptionsFor("p/sub-00000001"); ok {
		t.Fatal("构造完应清掉登记")
	}
}

func contentText(c session.Content) string {
	s := ""
	for _, p := range c {
		s += p.Text
	}
	return s
}
