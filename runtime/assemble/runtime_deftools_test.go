package assemble

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ybase/runtime/agentdef"
	"ybase/runtime/callctx"
	"ybase/runtime/session"
	"ybase/runtime/store/memstore"
	"ybase/runtime/toolhub"
)

func defWithTools(t *testing.T, name string, tools ...string) agentdef.Source {
	t.Helper()
	m := agentdef.Mem()
	q := make([]string, len(tools))
	for i, s := range tools {
		q[i] = `"` + s + `"`
	}
	manifest := "format = \"ybase/v1\"\nid = \"" + name + "\"\ndescription = \"测试\"\ntools = [" + strings.Join(q, ", ") + "]\n"
	if err := m.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: name}, agentdef.Files{"agent.toml": []byte(manifest)}); err != nil {
		t.Fatal(err)
	}
	return m
}

func toolsBuilder(t *testing.T, defs agentdef.Source, ran *int) *Builder {
	t.Helper()
	mk := func(name string) *toolhub.Tool {
		return &toolhub.Tool{Spec: session.ToolSpec{Name: name, Schema: json.RawMessage(`{"type":"object"}`)},
			Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
				*ran++
				return toolhub.Text("ran " + name), nil
			}}
	}
	set := []*toolhub.Tool{mk("read_file"), mk("grep"), mk("shell")}
	return &Builder{DB: memstore.New(), Layers: []LayerFunc{permLayer()}, Definitions: defs,
		Tools: []ToolSet{{Tools: func() []*toolhub.Tool { return set }}}}
}

// 08 8.3：定义的 tools 是会话可用工具的白名单（支持通配）；白名单外的工具不显露、不可调用。
func TestDefinitionToolsRestrictSession(t *testing.T) {
	ran := 0
	b := toolsBuilder(t, defWithTools(t, "reader", "read_file", "gr*"), &ran)
	a, cfg, err := b.Build("s1", "reader", false)
	if err != nil {
		t.Fatal(err)
	}
	avail := "," + strings.Join(a.Toolset.Available(), ",") + ","
	if !strings.Contains(avail, ",read_file,") || !strings.Contains(avail, ",grep,") {
		t.Fatalf("白名单里的工具应可用：%s", avail)
	}
	if strings.Contains(avail, ",shell,") {
		t.Fatalf("白名单外的 shell 不应可用：%s", avail)
	}
	for _, s := range cfg.Tools {
		if s.Name == "shell" {
			t.Fatal("白名单外的 shell 不应显露给模型")
		}
	}
	run := a.Hub.ExecuteIn(context.Background(), a.Toolset, callctx.New(callctx.Options{SessionID: "s1"}),
		[]session.ToolCall{{ID: "c1", Name: "shell", Args: json.RawMessage(`{}`)}}, nil)
	res, _ := run.Next()
	if ran != 0 || !strings.Contains(string(res.Output.Content[0].Text), "不存在") {
		t.Fatalf("调用白名单外的工具应按不存在处理：ran=%d out=%+v", ran, res.Output)
	}

	// 不写 tools 的定义、无定义会话：不收窄（行为不变）。
	b2 := toolsBuilder(t, defsSource(t, "plain"), &ran)
	for _, def := range []string{"plain", ""} {
		a2, _, err := b2.Build("s-"+def, def, false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(","+strings.Join(a2.Toolset.Available(), ",")+",", ",shell,") {
			t.Fatalf("定义 %q 不写 tools 时不应收窄：%v", def, a2.Toolset.Available())
		}
	}
}

// 写了 tools 但一个都没匹配上：报错，不悄悄给一个空工具集（多半是拼错了工具名）。
func TestDefinitionToolsNoMatch(t *testing.T) {
	ran := 0
	b := toolsBuilder(t, defWithTools(t, "typo", "raed_file"), &ran)
	if _, _, err := b.Build("s1", "typo", false); err == nil || !strings.Contains(err.Error(), "raed_file") {
		t.Fatalf("白名单项没匹配到任何工具应报错并列出该项，得 %v", err)
	}
}
