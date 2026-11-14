package appapi_test

// 入口无关约束（docs/02 - 开发管理/19 第五节、Mortise docs/05 第 5.3 节）：
// **核心不能知道自己被谁用**。appapi 是三个外壳（CLI / 桌面 / 网页）共用的唯一真相，
// 一旦出现 `if client == "cli"` 这类按入口分支的代码，同一个协议命令在不同入口就会有不同的
// 行为与形态，能力协商也就失去意义。
//
// 规则写成测试，防止以后退化。三条：
//  1. appapi（含 appapi/wire）不得 import 任何外壳包，也不得 import 框架的装配层；
//  2. appapi 源码里不得出现入口名字面量（cli / desktop / webhost / tui / gui）；
//  3. appapi 不得读 os.Args（那是入口的事，核心只处理命令参数）。
//
// 为什么放在 appapi_test 而不是 appapi：测试要跑 `go list` 与读源码，放在内部包会
// 把 os/exec 带进 appapi 的依赖图，反而破坏第 1 条要守的东西。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/Sevyn17/tenon"

// shellPkgs 外壳与装配层的包路径前缀：appapi 不得 import 它们。
// 外壳在框架之外（Mortise/client/{mortise,desktop,webhost}），框架内只可能出现装配层。
var shellPkgs = []string{
	module + "/apphost",
	module + "/adapters",
	module + "/transport",
	module + "/tenontest",
	module + "/examples",
}

// entryNames 入口名字面量。核心不该认识这些词。
var entryNames = []string{"cli", "desktop", "webhost", "tui", "gui"}

// appapiPkg 本包路径。
func appapiPkg(t *testing.T) (dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// 测试在 appapi/ 下跑。
	return wd
}

// TestAppAPINoShellImports appapi 与 appapi/wire 的 import 里不得出现外壳与装配层。
func TestAppAPINoShellImports(t *testing.T) {
	out, err := exec.Command("go", "list", "-json", module+"/appapi/...").Output()
	if err != nil {
		t.Skipf("go list 不可用：%v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	seen := 0
	for dec.More() {
		var p struct {
			ImportPath  string
			Imports     []string
			TestImports []string
		}
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		seen++
		for _, imp := range append(append([]string{}, p.Imports...), p.TestImports...) {
			for _, bad := range shellPkgs {
				if imp == bad || strings.HasPrefix(imp, bad+"/") {
					t.Errorf("%s 不得 import %q：核心不能依赖外壳或装配层", p.ImportPath, imp)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("go list 没列出任何 appapi 包")
	}
}

// TestAppAPINoEntryNames appapi 的**非测试**源码里不得出现入口名字面量。
// 只看非测试文件：测试里出现入口名（比如断言协议对某入口的兼容）不算腐化。
func TestAppAPINoEntryNames(t *testing.T) {
	dir := appapiPkg(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	wireFiles, err := filepath.Glob(filepath.Join(dir, "wire", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, wireFiles...)
	checked := 0
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		checked++
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, name := range entryNames {
			// 找带引号的字面量，避免把注释里的说明词也算进来。
			for _, quote := range []string{`"`, "`"} {
				lit := quote + name + quote
				if strings.Contains(text, lit) {
					t.Errorf("%s 出现入口名字面量 %s：核心不得按入口分支", rel(t, f), lit)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("没检查到任何 appapi 源文件")
	}
}

// TestAppAPINoOSArgs appapi 的非测试源码不得读 os.Args：命令行参数属于入口。
func TestAppAPINoOSArgs(t *testing.T) {
	dir := appapiPkg(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	wireFiles, err := filepath.Glob(filepath.Join(dir, "wire", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, wireFiles...)
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(filepath.Base(f), "_test.go") {
			continue
		}
		checked++
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "os.Args") {
			t.Errorf("%s 读 os.Args：命令行参数属于入口，核心只接命令参数", rel(t, f))
		}
	}
	if checked == 0 {
		t.Fatal("没检查到任何 appapi 源文件")
	}
}

// rel 相对仓库根的路径，报错时好认。
func rel(t *testing.T, abs string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		return abs
	}
	r, err := filepath.Rel(filepath.Dir(wd), abs)
	if err != nil {
		return abs
	}
	return r
}
