package permission

import "testing"

// Windows 上规则不分大小写：write_file(.env) 也要拦住 .ENV / .Env，命令首词同理（审查 P1-13）。
func TestGlobFoldCase(t *testing.T) {
	old := foldCase
	defer func() { foldCase = old }()
	foldCase = true
	for _, c := range [][2]string{{".env", ".ENV"}, {".env", ".Env"}, {"rm *", "RM -rf x"}, {"src/*.go", "SRC/a.GO"}} {
		if !Glob(c[0], c[1]) {
			t.Errorf("不分大小写时 %q 应匹配 %q", c[0], c[1])
		}
	}
	foldCase = false
	if Glob(".env", ".ENV") {
		t.Error("区分大小写的平台上 .env 不应匹配 .ENV")
	}
}
