package appapi

import (
	"fmt"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// session.locate（Mortise docs/72 B2）：只给 session 时报所属工作区与 id；给 entry 时报回合，回退后 rewound=true；
// 未知条目 not_found、未知会话报错；hello 与 workspace.list 带工作区 id。
func TestLocate(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	for i, text := range []string{"a", "b"} {
		if _, e := call2(t, app, "session.send", fmt.Sprintf(`{"session":"s","text":%q,"client_id":"c%d"}`, text, i)); e != nil {
			t.Fatal(e)
		}
		hd, _ := app.Host().Get("s")
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	er, e := call2(t, app, "session.entries", `{"session":"s"}`)
	if e != nil {
		t.Fatal(e)
	}
	var second wire.Entry
	for _, en := range er.(wire.EntriesResult).Entries {
		if en.Turn == 2 {
			second = en
			break
		}
	}
	if second.ID == "" {
		t.Fatal("没有第 2 回合的条目")
	}

	r, e := call2(t, app, "session.locate", `{"session":"s"}`)
	if e != nil {
		t.Fatal(e)
	}
	lr := r.(wire.LocateResult)
	if lr.Turn != 0 || lr.Rewound || lr.WorkspaceID != wire.WorkspaceID(lr.Workspace) {
		t.Fatalf("只给 session：%+v", lr)
	}

	r, e = call2(t, app, "session.locate", fmt.Sprintf(`{"session":"s","entry":%q}`, second.ID))
	if e != nil || r.(wire.LocateResult).Turn != 2 || r.(wire.LocateResult).Rewound {
		t.Fatalf("第 2 回合条目：%+v %v", r, e)
	}

	_, e = call2(t, app, "session.locate", `{"session":"s","entry":"nope"}`)
	wantCode(t, e, wire.CodeNotFound, "")
	_, e = call2(t, app, "session.locate", `{"session":""}`)
	wantCode(t, e, wire.CodeBadRequest, "")
	if _, e = call2(t, app, "session.locate", `{"session":"missing"}`); e == nil {
		t.Fatal("未知会话应报错")
	}

	// 回退到第 2 回合起点：条目进归档，仍能找到，rewound=true。
	if _, e := call2(t, app, "session.rewind", `{"session":"s","to_turn":2}`); e != nil {
		t.Fatal(e)
	}
	r, e = call2(t, app, "session.locate", fmt.Sprintf(`{"session":"s","entry":%q}`, second.ID))
	if e != nil || r.(wire.LocateResult).Turn != 2 || !r.(wire.LocateResult).Rewound {
		t.Fatalf("回退后：%+v %v", r, e)
	}

	// 工作区 id：hello 与 workspace.list 一致、16 位十六进制。
	h, e := call2(t, app, "hello", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	wl, e := call2(t, app, "workspace.list", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	item := wl.(wire.WorkspaceListResult).Items[0]
	if len(item.ID) != 16 {
		t.Fatalf("workspace.list id：%q", item.ID)
	}
	if hr := h.(wire.HelloResult); hr.Workspace != "" && hr.WorkspaceID != item.ID {
		t.Fatalf("hello 与 list 的工作区 id 不一致：%q / %q", hr.WorkspaceID, item.ID)
	}
	if wire.WorkspaceID("") != "" || wire.WorkspaceID("a") == wire.WorkspaceID("b") {
		t.Fatal("WorkspaceID：空键应为空，不同键应不同")
	}
}
