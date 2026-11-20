package pkgs

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// 风险推断：[config.*] → config；提示词片段 / 技能 → prompt；[[ui]] 里有 kind = call 的按钮 → commands（数据来源不算）；
// 没有登记 Risks 的段（测试段 widget）不带风险；结果按 RiskOrder 去重排序。
func TestPackageRisks(t *testing.T) {
	root := t.TempDir()
	plain := mk(t, root, "plain", "name = \"plain\"\n[[widget]]\nname = \"w\"\n")
	p, err := Parse(plain, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if rs := p.Risks(); rs == nil || len(rs) != 0 {
		t.Fatalf("只有无风险的段时应为空数组：%#v", rs)
	}

	full := mk(t, root, "full", `name = "full"
skills = "skills"
prompt = ["p.md"]
[config.pkgs-test-target]
x = 1
[[ui]]
id = "panel"
slot = "session.sidebar"
view_file = "ui/panel.json"
data = { cmd = "plugins.list" }
`, "skills/", "p.md", "ui/")
	view := `{"type":"stack","children":[{"type":"button","label":"关 shell","action":{"kind":"call","cmd":"plugins.set","args":{"name":"shell"}}},{"type":"link","label":"看","action":{"kind":"openPanel","panel":"x"}}]}`
	if err := os.WriteFile(filepath.Join(full, "ui", "panel.json"), []byte(view), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err = Parse(full, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if rs := p.Risks(); !slices.Equal(rs, []string{RiskConfig, RiskPrompt, RiskCommands}) {
		t.Fatalf("风险：%v", rs)
	}
	if cs := p.UICommands(); !slices.Equal(cs, []string{"plugins.set"}) {
		t.Fatalf("界面能调的命令（不含数据来源、不含 openPanel）：%v", cs)
	}
}

func TestSortRisks(t *testing.T) {
	got := SortRisks([]string{"zzz", RiskCommands, RiskExec, "", RiskWrite, RiskExec, "aaa", RiskNetwork})
	want := []string{RiskExec, RiskNetwork, RiskWrite, RiskCommands, "aaa", "zzz"}
	if !slices.Equal(got, want) {
		t.Fatalf("SortRisks：%v", got)
	}
}
