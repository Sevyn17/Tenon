package runtime

// 依赖规则（00 - 总体架构 第六节）：内核包（core/）不得 import 插件包（plugins/）；插件之间不互相 import。
// 应用（apps/）负责装配，可以 import 任何包。规则写成测试，防止以后退化。

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const module = "ybase-kernel/"

// pending 暂留的例外（保持为空）。
var pending = map[string]bool{}

func isPlugin(p string) bool { return strings.HasPrefix(p, "plugins/") }

func isKernel(p string) bool { return strings.HasPrefix(p, "core/") }

func TestDependencyRule(t *testing.T) {
	out, err := exec.Command("go", "list", "-json", "ybase-kernel/...").Output()
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
		from := strings.TrimPrefix(p.ImportPath, module)
		if strings.HasPrefix(from, "apps/") || from == "web" { // web：前端构建产物的嵌入包，只供应用 import
			continue
		}
		if !isKernel(from) && !isPlugin(from) {
			t.Errorf("包 %s 不在 core/、plugins/、apps/ 之下", from)
			continue
		}
		for _, imp := range append(p.Imports, p.TestImports...) {
			to, ok := strings.CutPrefix(imp, module)
			if !ok || to == from {
				continue
			}
			key := from + "->" + to
			if pending[key] {
				continue
			}
			switch {
			case strings.HasPrefix(to, "apps/"):
				t.Errorf("%s 不得 import 应用 %s", from, to)
			case isPlugin(to) && isPlugin(from):
				t.Errorf("插件 %s 不得 import 插件 %s", from, to)
			case isPlugin(to):
				t.Errorf("内核包 %s 不得 import 插件 %s", from, to)
			}
		}
	}
}
