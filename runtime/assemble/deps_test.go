package assemble

// 依赖规则（docs/05 - 定位与命名方案.md 3.2）：运行时（runtime/）不得 import 标准模块（modules/）、
// 应用接口层（appapi/）、SDK 与应用；标准模块之间不互相 import，也不 import appapi、SDK 与应用。
// SDK（模块根 tenon）、应用宿主（apphost/）、传输（transport/）与测试套件（tenontest/）负责装配；应用在框架之外的
// client 模块里，只经这些包与协议使用框架，可以 import 任何包。规则写成测试，防止以后退化。

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const module = "tenon"

// pending 暂留的例外（保持为空）。
var pending = map[string]bool{}

func isModule(p string) bool { return strings.HasPrefix(p, "modules/") }

func isRuntime(p string) bool { return strings.HasPrefix(p, "runtime/") }

func isAppAPI(p string) bool { return p == "appapi" || strings.HasPrefix(p, "appapi/") }

// assembler SDK、应用宿主（apphost）、传输（transport/）、测试套件与前端嵌入包：装配层，可以 import
// 任何包；运行时、标准模块、应用接口层都不得反过来 import 它们（计划/07）。
func assembler(p string) bool {
	return p == "" || p == "tenontest" || p == "web" || p == "apphost" || strings.HasPrefix(p, "transport/")
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
		for _, imp := range append(p.Imports, p.TestImports...) {
			to, ok := rel(imp)
			if !ok || to == from {
				continue
			}
			if pending[from+"->"+to] {
				continue
			}
			switch {
			case assembler(to):
				t.Errorf("%s 不得 import SDK / 测试套件 / 应用 %q", from, to)
			case strings.HasPrefix(to, "modules/internal/") && isModule(from):
				// modules/internal/*：标准模块共用的辅助包（进程启动、命令拆分），不算模块之间互相 import（05 3.2）
			case isModule(to) && isModule(from):
				t.Errorf("标准模块 %s 不得 import 标准模块 %s", from, to)
			case isModule(to):
				t.Errorf("%s 不得 import 标准模块 %s", from, to)
			case isAppAPI(to) && !isAppAPI(from):
				t.Errorf("%s 不得 import 应用接口层 %s", from, to)
			}
		}
	}
}
