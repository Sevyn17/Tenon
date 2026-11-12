package assemble

// 依赖规则（docs/01 - 设计文档/05 - 定位与命名方案.md 3.2）：运行时（runtime/）不得 import 标准模块（modules/）、
// 应用接口层（appapi/）、SDK 与应用；标准模块之间不互相 import，也不 import appapi、SDK 与应用。
// SDK（模块根 tenon）、应用宿主（apphost/）、传输（transport/）与测试套件（tenontest/）负责装配；应用在框架之外的
// client 模块里，只经这些包与协议使用框架，可以 import 任何包。规则写成测试，防止以后退化。

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/Sevyn17/tenon"

// pending 暂留的例外（保持为空）。
var pending = map[string]bool{}

func isModule(p string) bool { return strings.HasPrefix(p, "modules/") }

func isRuntime(p string) bool { return strings.HasPrefix(p, "runtime/") }

func isAppAPI(p string) bool { return p == "appapi" || strings.HasPrefix(p, "appapi/") }

// assembler SDK、应用宿主（apphost）、协议适配（adapters）、传输（transport/）、示例（examples/）、测试套件与
// 前端嵌入包：装配层，可以 import
// 任何包；运行时、标准模块、应用接口层都不得反过来 import 它们（计划/07）。
func assembler(p string) bool {
	return p == "" || p == "tenontest" || p == "web" || p == "apphost" || p == "adapters" || strings.HasPrefix(p, "adapters/") || strings.HasPrefix(p, "transport/") ||
		strings.HasPrefix(p, "examples/")
}

// rel 本模块内的相对路径（模块根 = ""）；不是本模块的包返回 false。
func rel(imp string) (string, bool) {
	if imp == module {
		return "", true
	}
	return strings.CutPrefix(imp, module+"/")
}

func TestDependencyRule(t *testing.T) {
	out, err := exec.Command("go", "list", "-json", module+"/...").Output()
	if err != nil {
		t.Skipf("go list 不可用：%v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p struct {
			ImportPath  string
			Imports     []string
			TestImports []string
		}
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		from, _ := rel(p.ImportPath)
		if assembler(from) {
			continue
		}
		if !isRuntime(from) && !isModule(from) && !isAppAPI(from) {
			t.Errorf("包 %s 不在 runtime/、modules/、appapi/ 之下", from)
			continue
		}
		for _, imp := range p.Imports {
			checkDep(t, from, imp, false)
		}
		for _, imp := range p.TestImports {
			checkDep(t, from, imp, true)
		}
	}
}

// checkDep 检查一条引用。test 为真表示来自测试文件。
func checkDep(t *testing.T, from, imp string, test bool) {
	t.Helper()
	to, ok := rel(imp)
	if !ok || to == from {
		return
	}
	if pending[from+"->"+to] {
		return
	}
	switch {
	case assembler(to):
		t.Errorf("%s 不得 import SDK / 测试套件 / 应用 %q", from, to)
	case strings.HasPrefix(to, "modules/internal/") && isModule(from):
		// modules/internal/*：标准模块共用的辅助包（进程启动、命令拆分），不算模块之间互相 import（05 3.2）
	case isModule(to) && isModule(from):
		// 两个例外，都只针对 SQLite 实现这一个包：
		// 1. store-sqlite 是它的组件外壳，引用自己的实现是职责，不是模块之间互相依赖；
		// 2. 测试文件用它验证「关掉再打开仍在」，内存实现做不到（18 方案第九节）。
		if to == "modules/storage/sqlite" && (from == "modules/storage/store-sqlite" || test) {
			return
		}
		t.Errorf("标准模块 %s 不得 import 标准模块 %s", from, to)
	case isModule(to):
		// 测试例外：runtime/ 与 appapi/ 的测试可以用 SQLite 实现验证「关掉再打开仍在」这类内存实现做不到的事。
		// 只放行这一个包，生产代码不受影响（18 方案第九节）。
		if test && to == "modules/storage/sqlite" && (isRuntime(from) || isAppAPI(from)) {
			return
		}
		t.Errorf("%s 不得 import 标准模块 %s", from, to)
	case isAppAPI(to) && !isAppAPI(from):
		t.Errorf("%s 不得 import 应用接口层 %s", from, to)
	}
}
