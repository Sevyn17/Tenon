package sandbox

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
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

func TestAuthorizerAsksForWider(t *testing.T) {
	// 04 4.3.1：判定从 toolhub.Layer 改成 Authorizer（决策服务）。
	a := Authorizer(func(string) Mode { return WorkspaceWrite })
	tool := &toolhub.Tool{Spec: session.ToolSpec{Name: "shell"}, Attrs: map[string]any{AttrSandboxed: true}}
	call := func(args string, approved bool) toolhub.Decision {
		d, err := a.Authorize(context.Background(), &toolhub.Cx{Tool: tool, Approved: approved},
			json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := call(`{"sandbox_permissions":"danger-full-access"}`, false); d.Action != toolhub.DecisionConfirm {
		t.Fatalf("更宽的申请应要求确认：%+v", d)
	}
	if d := call(`{"sandbox_permissions":"danger-full-access"}`, true); d.Action != toolhub.DecisionAllow {
		t.Fatalf("已批准应放行：%+v", d)
	}
	if d := call(`{"sandbox_permissions":"read-only"}`, false); d.Action != toolhub.DecisionAllow {
		t.Fatalf("更窄的申请应直接放行：%+v", d)
	}
	plain := &toolhub.Tool{Spec: session.ToolSpec{Name: "read_file"}}
	d, _ := a.Authorize(context.Background(), &toolhub.Cx{Tool: plain},
		json.RawMessage(`{"sandbox_permissions":"danger-full-access"}`))
	if d.Action != toolhub.DecisionAllow {
		t.Fatalf("不需要沙箱的工具应放行：%+v", d)
	}
}
