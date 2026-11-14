package pkgs

// 界面契约的归属约束：**节点形状只在 appapi/wire 定义一处**。
//
// 2026-11-14 的教训：本包曾经复制了一份 DeclNode/DeclAction（与 wire 的形状"看起来一样"），
// 结果与渲染器错了四处字段名（code 用 text、keyValue 用 keyValue、button/link 用 text），
// 照契约写出的插件视图在应用里渲染成空。复制一份定义不会报错、不会让测试变红，
// 只会安静地漂移。所以用测试钉住：本包不许再定义这些类型。
//
// 本包属于 runtime/，依赖规则禁止 import appapi，所以它**不应该**知道节点形状；
// 清单层校验（大小、顶层 type、id/slot、data.cmd）之外的事情都归协议边界。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDeclarativeTypesInPkgs 本包源码里不得出现 DeclNode / DeclAction / DeclNodeTypes /
// DeclActionKinds 这类契约符号的**定义**（引用注释不算，只看声明）。
func TestNoDeclarativeTypesInPkgs(t *testing.T) {
	banned := []string{"DeclNode", "DeclAction", "DeclNodeTypes", "DeclActionKinds", "declKinds"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		checked++
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatalf("%s 解析失败：%v", f, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch d := n.(type) {
			case *ast.TypeSpec:
				for _, b := range banned {
					if d.Name.Name == b {
						t.Errorf("%s 定义了 %s：界面契约只在 appapi/wire 定义一处，本包不得复制", f, b)
					}
				}
			case *ast.ValueSpec:
				for _, name := range d.Names {
					for _, b := range banned {
						if name.Name == b {
							t.Errorf("%s 定义了 %s：界面契约只在 appapi/wire 定义一处，本包不得复制", f, b)
						}
					}
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("没检查到任何源文件")
	}
}
