package appapi

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
)

type fakeMCP struct{ set []string }

func (f *fakeMCP) List(context.Context) (wire.MCPListResult, error) {
	return wire.MCPListResult{Servers: []wire.MCPServerInfo{{Name: "pkg", Source: "package:p"}}, Editable: true}, nil
}

func (f *fakeMCP) Test(context.Context, wire.MCPServerDraft) ([]wire.MCPTool, error) {
	return nil, errors.New("mcp: 启动失败")
}

func (f *fakeMCP) SetEnabled(_ context.Context, name string, enabled bool, scope string) error {
	f.set = append(f.set, name+"/"+scope)
	return nil
}

// 没装 mcp：命令 unsupported，不报能力 mcp。
func TestMCPUnsupported(t *testing.T) {
	app := newApp(t)
	if slices.Contains(app.capabilities(), CapMCP) {
		t.Fatal("没装 mcp 不应报 mcp")
	}
	for _, cmd := range []string{"mcp.list", "mcp.test", "mcp.save", "mcp.remove", "mcp.setEnabled"} {
		_, e := call2(t, app, cmd, `{}`)
		wantCode(t, e, wire.CodeUnsupported, "")
	}
}

// 能力、列表数组不为 null、没有编辑器时 editable=false 且 save unsupported、test 失败给 ok=false、
// setEnabled 缺省 global / 非法 scope、viewer 只能 list。
func TestMCPRolesAndArgs(t *testing.T) {
	fm := &fakeMCP{}
	app := newAppOpts(t, func(o *Options) { o.MCP = fm })
	if !slices.Contains(app.capabilities(), CapMCP) {
		t.Fatal("应报 mcp 能力")
	}
	out, e := call2(t, app, "mcp.list", ``)
	if e != nil {
		t.Fatal(e)
	}
	r := out.(wire.MCPListResult)
	if r.Editable || r.Servers[0].Tools == nil || r.Servers[0].Args == nil || r.Servers[0].EnvKeys == nil {
		t.Fatalf("没有编辑器时 editable 应为 false、列表字段应为 []：%+v", r)
	}
	_, e = call2(t, app, "mcp.save", `{"server":{"name":"x","command":"y"}}`)
	wantCode(t, e, wire.CodeUnsupported, "")
	tr, e := call2(t, app, "mcp.test", `{"server":{"name":"x","command":"y"}}`)
	if e != nil || tr.(wire.MCPTestResult).OK || tr.(wire.MCPTestResult).Error != "启动失败" {
		t.Fatalf("连不上应 ok=false：%+v %v", tr, e)
	}
	if _, e := call2(t, app, "mcp.setEnabled", `{"name":" x ","enabled":false}`); e != nil {
		t.Fatal(e)
	}
	if !slices.Equal(fm.set, []string{"x/global"}) {
		t.Fatalf("scope 缺省 global、名字去空白：%v", fm.set)
	}
	for _, args := range []string{`{}`, `{"name":"x","scope":"project"}`} {
		_, e := call2(t, app, "mcp.setEnabled", args)
		wantCode(t, e, wire.CodeInvalid, "")
	}
	v := app.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	if _, err := v.Call(context.Background(), "mcp.list", nil); err != nil {
		t.Fatalf("viewer mcp.list：%v", err)
	}
	for _, cmd := range []string{"mcp.test", "mcp.save", "mcp.remove", "mcp.setEnabled"} {
		_, err := v.Call(context.Background(), cmd, []byte(`{"name":"x","server":{"name":"x","command":"y"}}`))
		var pe *Error
		if !errors.As(err, &pe) || pe.Code != wire.CodeForbidden {
			t.Fatalf("viewer 不能 %s：%v", cmd, err)
		}
	}
	if len(fm.set) != 1 {
		t.Fatalf("viewer 的调用不应到达实现：%v", fm.set)
	}
}
