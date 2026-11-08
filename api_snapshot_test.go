package tenon_test

// 公开面快照（计划/08 3.7）：承诺包的全部导出名与签名写进 testdata/api.txt。
// 不一致时失败——公开接口变了就更新快照（go test -run TestAPISnapshot -update-api）并写决策日志。
// 只用标准库：go list -export 取编译缓存里的导出数据，go/importer 读出类型；不依赖要联网安装的工具。

import (
	"flag"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateAPI = flag.Bool("update-api", false, "重录 testdata/api.txt")

// promised 在稳定承诺之内的包（docs/框架设计/07 - SDK 接口.md）。
var promised = []string{
	"github.com/Sevyn17/tenon",
	"github.com/Sevyn17/tenon/tenontest",
	"github.com/Sevyn17/tenon/transport/web",
	"github.com/Sevyn17/tenon/transport/bind",
	"github.com/Sevyn17/tenon/adapters",
	"github.com/Sevyn17/tenon/apphost",
	"github.com/Sevyn17/tenon/appapi/wire",
}

func TestAPISnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过公开面快照（要编译全部承诺包）")
	}
	imp, err := exportImporter(promised)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, path := range promised {
		pkg, err := imp.Import(path)
		if err != nil {
			t.Fatalf("类型检查 %s：%v", path, err)
		}
		writeAPI(&b, pkg)
	}
	got := b.String()
	file := filepath.Join("testdata", "api.txt")
	if *updateAPI {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读快照：%v（第一次用 -update-api 生成）", err)
	}
	// Windows 上 checkout 会把快照换成 CRLF（.gitattributes 的 text=auto），比较前统一成 LF。
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); w != got {
		t.Fatalf("公开接口变了：更新快照并写决策日志（go test -run TestAPISnapshot -update-api .）\n%s", apiDiff(w, got))
	}
}

// exportImporter 用 go list -export 拿到承诺包及其依赖的导出数据文件，交给 gc 导入器读。
func exportImporter(pkgs []string) (types.Importer, error) {
	args := append([]string{"list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}"}, pkgs...)
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list -export：%v\n%s", err, ee.Stderr)
		}
		return nil, fmt.Errorf("go list -export：%v", err)
	}
	files := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if path, file, ok := strings.Cut(strings.TrimSpace(l), "="); ok && file != "" {
			files[path] = file
		}
	}
	lookup := func(path string) (io.ReadCloser, error) {
		f, ok := files[path]
		if !ok {
			return nil, fmt.Errorf("没有 %s 的导出数据", path)
		}
		return os.Open(f)
	}
	return importer.ForCompiler(token.NewFileSet(), "gc", lookup), nil
}

func writeAPI(b *strings.Builder, pkg *types.Package) {
	q := func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		return p.Path()
	}
	scope := pkg.Scope()
	names := scope.Names()
	sort.Strings(names)
	for _, n := range names {
		obj := scope.Lookup(n)
		if !obj.Exported() {
			continue
		}
		switch o := obj.(type) {
		case *types.TypeName:
			fmt.Fprintf(b, "%s: type %s%s %s\n", pkg.Path(), n, typeParams(o.Type(), q), declType(o, q))
			for _, m := range methods(o.Type(), q) {
				fmt.Fprintf(b, "%s: method (%s) %s\n", pkg.Path(), n, m)
			}
		case *types.Const:
			fmt.Fprintf(b, "%s: const %s %s\n", pkg.Path(), n, types.TypeString(o.Type(), q))
		default:
			fmt.Fprintf(b, "%s: %s\n", pkg.Path(), types.ObjectString(obj, q))
		}
	}
}

func typeParams(t types.Type, q types.Qualifier) string {
	var tps *types.TypeParamList
	switch n := t.(type) {
	case *types.Named:
		tps = n.TypeParams()
	case *types.Alias:
		tps = n.TypeParams()
	}
	if tps.Len() == 0 {
		return ""
	}
	var ps []string
	for i := 0; i < tps.Len(); i++ {
		p := tps.At(i)
		ps = append(ps, p.Obj().Name()+" "+types.TypeString(p.Constraint(), q))
	}
	return "[" + strings.Join(ps, ", ") + "]"
}

// declType 类型的定义：别名写 "= 目标"；结构体只列导出字段（未导出字段不在承诺里）。
func declType(o *types.TypeName, q types.Qualifier) string {
	u := o.Type().Underlying()
	if o.IsAlias() {
		// 别名的目标在不承诺的包里也算进来：别名把它的导出字段一起带进了公开面。
		d := "= " + types.TypeString(types.Unalias(o.Type()), q)
		if s, ok := u.(*types.Struct); ok {
			d += " " + structFields(s, q)
		}
		return d
	}
	if s, ok := u.(*types.Struct); ok {
		return structFields(s, q)
	}
	return types.TypeString(u, q)
}

func structFields(s *types.Struct, q types.Qualifier) string {
	{
		var fs []string
		for i := 0; i < s.NumFields(); i++ {
			f := s.Field(i)
			if !f.Exported() {
				continue
			}
			if f.Embedded() {
				fs = append(fs, types.TypeString(f.Type(), q))
			} else {
				fs = append(fs, f.Name()+" "+types.TypeString(f.Type(), q))
			}
		}
		return "struct{" + strings.Join(fs, "; ") + "}"
	}
}

// methods 值与指针方法集里的导出方法（含嵌入提升的）；接口的方法已经在定义里。
func methods(t types.Type, q types.Qualifier) []string {
	if _, ok := t.Underlying().(*types.Interface); ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, ms := range []*types.MethodSet{types.NewMethodSet(t), types.NewMethodSet(types.NewPointer(t))} {
		for i := 0; i < ms.Len(); i++ {
			f := ms.At(i).Obj().(*types.Func)
			if !f.Exported() || seen[f.Name()] {
				continue
			}
			seen[f.Name()] = true
			sig := types.TypeString(f.Type(), q)
			out = append(out, f.Name()+strings.TrimPrefix(sig, "func"))
		}
	}
	sort.Strings(out)
	return out
}

// apiDiff 只列多出来和少掉的行，方便看出改了什么。
func apiDiff(want, got string) string {
	w, g := lineSet(want), lineSet(got)
	var b strings.Builder
	for _, l := range strings.Split(want, "\n") {
		if l != "" && !g[l] {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if l != "" && !w[l] {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}

func lineSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(s, "\n") {
		m[l] = true
	}
	return m
}
