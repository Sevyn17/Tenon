package appid

import (
	"path/filepath"
	"testing"
)

func restore(t *testing.T) {
	old := Get()
	t.Cleanup(func() { Set(old) })
}

func TestDefault(t *testing.T) {
	restore(t)
	Set(ID{})
	if Name() != "tenon" || DirName() != ".tenon" || EnvName("HOME") != "TENON_HOME" || Command("trust") != "tenon trust" {
		t.Fatalf("缺省身份不对：%+v", Get())
	}
	if got := Project("/w"); got != filepath.Join("/w", ".tenon") {
		t.Fatalf("项目目录 %q", got)
	}
}

func TestSetPartialAndHome(t *testing.T) {
	restore(t)
	Set(ID{Name: "mortise", Env: "MORTISE_"})
	if Name() != "mortise" || DirName() != ".tenon" || EnvName("API_KEY") != "MORTISE_API_KEY" {
		t.Fatalf("部分设置：%+v", Get())
	}
	t.Setenv("MORTISE_HOME", "/h")
	t.Setenv("TENON_HOME", "/wrong")
	if Home() != "/h" {
		t.Fatalf("Home 应读 MORTISE_HOME：%q", Home())
	}
	t.Setenv("MORTISE_HOME", "")
	if h := Home(); h != "" && filepath.Base(h) != ".tenon" {
		t.Fatalf("没设环境变量时应为 ~/<Dir>：%q", h)
	}
}
