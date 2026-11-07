package sandbox

import (
	"context"
	"encoding/json"
	"testing"

	"tenon/runtime/session"
	"tenon/runtime/toolhub"
)

func TestEffective(t *testing.T) {
	cases := []struct {
		args     string
		approved bool
		want     Mode
		fail     bool
	}{
		{`{"command":"x"}`, false, WorkspaceWrite, false},
		{`{"sandbox_permissions":"read-only"}`, false, ReadOnly, false},
		{`{"sandbox_permissions":"danger-full-access"}`, false, "", true},
		{`{"sandbox_permissions":"danger-full-access"}`, true, Full, false},
		{`{"sandbox_permissions":"bogus"}`, true, "", true},
	}
	for _, c := range cases {
		m, err := Effective(WorkspaceWrite, json.RawMessage(c.args), c.approved)
		if (err != nil) != c.fail || m != c.want {
			t.Errorf("%s approved=%v → %q %v", c.args, c.approved, m, err)
		}
	}
}

func TestLayerAsksForWider(t *testing.T) {
	l := Layer(func(string) Mode { return WorkspaceWrite })
	tool := &toolhub.Tool{Spec: session.ToolSpec{Name: "shell"}, Attrs: map[string]any{AttrSandboxed: true}}
	ran := false
	next := func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
		ran = true
		return toolhub.Text("ok"), nil
	}
	call := func(args string, approved bool) toolhub.Output {
		ran = false
		out, err := l.Call(context.Background(), &toolhub.Cx{Tool: tool, Approved: approved}, json.RawMessage(args), next)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := call(`{"sandbox_permissions":"danger-full-access"}`, false); out.Kind != toolhub.Confirm || ran {
		t.Fatal("更宽的申请应要求确认")
	}
	if call(`{"sandbox_permissions":"danger-full-access"}`, true); !ran {
		t.Fatal("已批准应放行")
	}
	if call(`{"sandbox_permissions":"read-only"}`, false); !ran {
		t.Fatal("更窄的申请应直接放行")
	}
	plain := &toolhub.Tool{Spec: session.ToolSpec{Name: "read_file"}}
	ran = false
	if _, _ = l.Call(context.Background(), &toolhub.Cx{Tool: plain}, json.RawMessage(`{"sandbox_permissions":"danger-full-access"}`), next); !ran {
		t.Fatal("非沙箱工具不受影响")
	}
}
