package appapi

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
)

type stateMCP struct {
	enabled bool
	sets    int
}

func (m *stateMCP) List(context.Context) (wire.MCPListResult, error) {
	return wire.MCPListResult{
		Servers:  []wire.MCPServerInfo{{Name: "server", Enabled: m.enabled}},
		Revision: "revision",
	}, nil
}

func (*stateMCP) Test(context.Context, wire.MCPServerDraft) ([]wire.MCPTool, error) {
	return nil, nil
}

func (m *stateMCP) SetEnabled(_ context.Context, _ string, enabled bool, _ string) error {
	m.sets++
	m.enabled = enabled
	return nil
}

func TestMCPSetEnabledNoopSkipsWriteAndReload(t *testing.T) {
	m := &stateMCP{}
	rl := &fakeReloader{}
	app := newAppOpts(t, func(o *Options) {
		o.MCP = m
		o.Workspaces = rl
	})

	out, err := app.MCPSetEnabled(context.Background(), wire.MCPSetEnabledArgs{Name: "server", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if out.Reloaded || out.Revision != "revision" || m.sets != 0 || rl.reloads != 0 {
		t.Fatalf("状态未变不应写入或重建：out=%+v sets=%d reloads=%d", out, m.sets, rl.reloads)
	}

	out, err = app.MCPSetEnabled(context.Background(), wire.MCPSetEnabledArgs{Name: "server", Enabled: true})
	if err != nil || !out.Reloaded || m.sets != 1 || rl.reloads != 1 {
		t.Fatalf("状态变化应写入并重建一次：out=%+v err=%v sets=%d reloads=%d", out, err, m.sets, rl.reloads)
	}

	out, err = app.MCPSetEnabled(context.Background(), wire.MCPSetEnabledArgs{Name: "server", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Reloaded || m.sets != 1 || rl.reloads != 1 {
		t.Fatalf("重复启用不应再次写入或重建：out=%+v sets=%d reloads=%d", out, m.sets, rl.reloads)
	}
}
