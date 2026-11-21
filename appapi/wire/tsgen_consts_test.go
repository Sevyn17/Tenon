package wire

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// commands.go里的 ChangeScope*常量必须全部登记到 tsConsts，避免 TestTypeScript
// 只验证「生成器与产物一致」而漏掉生成器清单本身少常量。
func TestTypeScriptChangeScopesComplete(t *testing.T) {
	registered := map[string]bool{}
	for _, group := range tsConsts {
		if group.Name == "ChangeScopes" {
			for _, value := range group.Values {
				registered[value] = true
			}
		}
	}
	if len(registered) == 0 {
		t.Fatal("tsConsts缺少 ChangeScopes清单")
	}

	f, err := parser.ParseFile(token.NewFileSet(), "commands.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range values.Names {
				if !strings.HasPrefix(name.Name, "ChangeScope") {
					continue
				}
				found++
				if i >= len(values.Values) {
					t.Errorf("%s没有可检查的字面量值", name.Name)
					continue
				}
				lit, ok := values.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("%s不是字符串字面量，无法核对生成清单", name.Name)
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Errorf("%s的值无法解析：%v", name.Name, err)
					continue
				}
				if !registered[value] {
					t.Errorf("%s = %q未登记到 tsConsts ChangeScopes", name.Name, value)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("commands.go里没找到 ChangeScope*常量")
	}
}
