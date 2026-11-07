package pkgs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUISection(t *testing.T) {
	root := t.TempDir()
	dir := mk(t, root, "ui-pkg", `name = "ui-pkg"

[[ui]]
id = "status"
slot = "conversation.card"
title = "状态"
order = 5
requires = "mcp"
view_file = "ui/status.json"
[ui.data]
cmd = "session.stats"
args = { session = "{{session}}" }

[[ui]]
id = "hello"
slot = "sidebar.footer"
view = { type = "text", text = "你好", tone = "muted" }
`)
	if err := os.MkdirAll(filepath.Join(dir, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui", "status.json"), []byte(`[{"type":"text","text":"请求 {{attempts}} 次"},{"type":"divider"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Parse(dir, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	us := p.UISlots()
	if len(us) != 2 || us[0].ID != "status" || us[0].Requires != "mcp" || us[0].Data == nil || us[0].Data.Cmd != "session.stats" || us[0].Data.Args["session"] != "{{session}}" {
		t.Fatalf("解析：%+v", us)
	}
	var v map[string]any
	if json.Unmarshal(us[1].View, &v) != nil || v["type"] != "text" || v["text"] != "你好" {
		t.Fatalf("内联视图：%s", us[1].View)
	}
	if !strings.Contains(p.Contents(), "界面 conversation.card、sidebar.footer") {
		t.Fatalf("Contents：%s", p.Contents())
	}
	for name, body := range map[string]string{
		"bad-cmd":    "[[ui]]\nid = \"a\"\nslot = \"x.y\"\nview = { type = \"text\", text = \"t\" }\n[ui.data]\ncmd = \"session.delete\"\n",
		"bad-id":     "[[ui]]\nid = \"A B\"\nslot = \"x.y\"\nview = { type = \"text\", text = \"t\" }\n",
		"dup-id":     "[[ui]]\nid = \"a\"\nslot = \"x\"\nview = { type = \"text\", text = \"t\" }\n[[ui]]\nid = \"a\"\nslot = \"x\"\nview = { type = \"text\", text = \"t\" }\n",
		"no-view":    "[[ui]]\nid = \"a\"\nslot = \"x\"\n",
		"no-type":    "[[ui]]\nid = \"a\"\nslot = \"x\"\nview = { text = \"t\" }\n",
		"escape":     "[[ui]]\nid = \"a\"\nslot = \"x\"\nview_file = \"../x.json\"\n",
		"both-views": "[[ui]]\nid = \"a\"\nslot = \"x\"\nview_file = \"v.json\"\nview = { type = \"text\", text = \"t\" }\n",
		"bad-field":  "[[ui]]\nid = \"a\"\nslot = \"x\"\nview = { type = \"text\", text = \"t\" }\nslto = \"y\"\n",
	} {
		d := mk(t, root, name, "name = \""+name+"\"\n\n"+body)
		if _, err := Parse(d, ScopeUser); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
}
