package appapi

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
)

type fakeWorkspaces struct{ opened []string }

func (f *fakeWorkspaces) List() []wire.WorkspaceInfo {
	return []wire.WorkspaceInfo{{Path: "w", Name: "w", Current: true}}
}

func (f *fakeWorkspaces) Open(_ context.Context, path string) (wire.WorkspaceInfo, error) {
	f.opened = append(f.opened, path)
	return wire.WorkspaceInfo{Path: path, Name: path, Current: true}, nil
}

// workspace.list / open：没有管理器时 list 只报当前目录、open 报 unsupported；
// 有会话在跑回合（这里停在工具确认上）时 open 拒绝（not_idle），回合结束后放行。
func TestV2Workspaces(t *testing.T) {
	app := newApp(t)
	r, e := call2(t, app, "workspace.list", `{}`)
	if e != nil || len(r.(wire.WorkspaceListResult).Items) != 1 || !r.(wire.WorkspaceListResult).Items[0].Current {
		t.Fatalf("无管理器的 list：%+v %v", r, e)
	}
	_, e = call2(t, app, "workspace.open", `{"path":"x"}`)
	wantCode(t, e, wire.CodeUnsupported, "")

	ws := &fakeWorkspaces{}
	app.o.Workspaces = ws
	_, e = call2(t, app, "workspace.open", `{"path":"  "}`)
	wantCode(t, e, wire.CodeInvalid, "")

	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"call tool","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	opened := waitKind(t, c, wire.EvInteractionOpen)
	_, e = call2(t, app, "workspace.open", `{"path":"other"}`)
	wantCode(t, e, wire.CodeNotIdle, "")
	if len(ws.opened) != 0 {
		t.Fatal("回合进行中不应切换")
	}

	it := opened[len(opened)-1].Data.(wire.Interaction)
	if _, e := call2(t, app, "interaction.respond", `{"interaction":"`+it.ID+`","response":{"approve":false}}`); e != nil {
		t.Fatal(e)
	}
	// 回合结束、运行状态落库后放行（state_changed → idle 是持久事件，落库之后才发）。
	for {
		evs := waitKind(t, c, wire.EvStateChanged)
		if evs[len(evs)-1].Data.(wire.StateChanged).RunState == wire.RunIdle {
			break
		}
	}
	if _, e := call2(t, app, "workspace.open", `{"path":"other"}`); e != nil || len(ws.opened) != 1 {
		t.Fatalf("空闲后应放行：%v %v", e, ws.opened)
	}
}
