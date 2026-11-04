package prompt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ybase-kernel/core/agentdef"
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store/memstore"
	"ybase-kernel/core/toolhub"
)

// authLayer 测试用的授权层（toolhub 要求恰好一个）。
func authLayer(*kr.SessionCx) ([]toolhub.Layer, error) {
	return []toolhub.Layer{{Name: "perm", Guard: true, Authorizes: true,
		Call: func(ctx context.Context, cx *toolhub.Cx, a json.RawMessage, n toolhub.Next) (toolhub.Output, error) {
			return n(ctx, cx, a)
		}}}, nil
}

// 定义里的 prompt 进系统段，排在基础提示词之后；无定义的会话不多块
// （2026-11-04 审查 P0-2：原先定义的 prompt 没有任何代码读，被静默丢弃）。
func TestDefinitionPromptJoinsSystem(t *testing.T) {
	asm, err := plugin.Assemble(plugin.Spec{Plugins: []plugin.Entry{{Name: "prompt"}},
		Inject: map[string]any{kr.ServiceWorkdir: t.TempDir()}, Defaults: kr.SlotDefaults()})
	if err != nil {
		t.Fatal(err)
	}
	col, err := kr.Collect(asm)
	if err != nil {
		t.Fatal(err)
	}
	defs := agentdef.Mem()
	if err := defs.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: "support"}, agentdef.Files{
		"agent.toml": []byte("format = \"ybase/v1\"\nid = \"support\"\nprompt = \"@AGENT.md\"\n"),
		"AGENT.md":   []byte("你是售后客服，只回答订单问题。"),
	}); err != nil {
		t.Fatal(err)
	}
	b := &kr.Builder{DB: memstore.New(), Blocks: col.Blocks, Layers: []kr.LayerFunc{authLayer}, Definitions: defs}
	_, cfg, err := b.Build("s1", "support", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.System) != 2 || !strings.Contains(cfg.System[0].Text, "工作目录") ||
		cfg.System[1].Text != "你是售后客服，只回答订单问题。" || cfg.System[1].Source != "agent-def:support" {
		t.Fatalf("系统段应为 基础提示词 + 定义 prompt：%+v", blocks(cfg.System))
	}
	_, cfg2, err := b.Build("s2", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.System) != 1 {
		t.Fatalf("无定义会话只有基础提示词：%+v", blocks(cfg2.System))
	}
}

func blocks(bs []session.SystemBlock) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Source + ":" + b.Text
	}
	return out
}
