package appapi

import (
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
)

func TestTitleFromText(t *testing.T) {
	long := strings.Repeat("字", AutoTitleRunes+5)
	for _, c := range []struct{ in, want string }{
		{"  帮我  写个 README \n第二行", "帮我 写个 README"},
		{"\n\n  \t\n看看目录", "看看目录"},
		{"   \n\t", ""},
		{long, strings.Repeat("字", AutoTitleRunes) + "…"},
	} {
		if got := TitleFromText(c.in); got != c.want {
			t.Errorf("TitleFromText(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 第一条消息给无标题会话起标题；之后的消息和用户改过的标题都不被覆盖。
func TestSendAutoTitle(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	title := func() string {
		out, e := call2(t, app, "session.get", `{"session":"s"}`)
		if e != nil {
			t.Fatal(e)
		}
		return out.(wire.SessionSnapshot).Title
	}
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"看看这个目录\n里有什么","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	if got := title(); got != "看看这个目录" {
		t.Fatalf("自动标题 = %q", got)
	}
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"另一件事","client_id":"c2"}`); e != nil {
		t.Fatal(e)
	}
	if got := title(); got != "看看这个目录" {
		t.Fatalf("后续消息不应改标题：%q", got)
	}
	if _, e := call2(t, app, "session.update", `{"session":"s","title":"我起的名"}`); e != nil {
		t.Fatal(e)
	}
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"再一件","client_id":"c3"}`); e != nil {
		t.Fatal(e)
	}
	if got := title(); got != "我起的名" {
		t.Fatalf("用户标题被覆盖：%q", got)
	}
}
