package apphost

import "testing"

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"标题：写 README。":         "写 README",
		"“修复登录 bug”\n多余解释":     "修复登录 bug",
		"  Fix flaky tests.  ": "Fix flaky tests",
		"":                     "",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q，期望 %q", in, got, want)
		}
	}
}
