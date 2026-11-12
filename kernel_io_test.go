package tenon

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 内核禁令（docs/02 - 开发管理/18 - 内核与 I/O 分离方案 第六步 0）：
// runtime/ 不许引用这些包——内核只定义接口，不接触文件、数据库和网络。
// 现有违规列在 kernelIOAllow 里，只许删不许加：删掉一处引用就从名单里去掉对应行。
var kernelIOBanned = []string{
	"os",
	"os/exec",
	"database/sql",
	"net/http",
	"modernc.org/sqlite",
}

// kernelIOAllow 允许名单：文件（相对 runtime/）→ 暂时允许的包。
// 每条都要有理由；理由与清理计划见 18 方案第五、六节。
var kernelIOAllow = map[string][]string{
	// 第 4 步移到 apphost / adapters：读文件、扫目录、用户目录
	"agentdef/sources.go":       {"os"},
	"appid/appid.go":            {"os"},
	"assemble/services.go":      {"os"},
	"component/profile.go":      {"os"},
	"component/profile_edit.go": {"os"},
	"pkgs/pkgs.go":              {"os"},
	"pkgs/ui.go":                {"os"},
	// 第 3 步评估后保留：store.SQL 的方法签名用到 *sql.DB，是类型引用不是读写。
	// 去掉它要另造一套不带 *sql.DB 的迁移接口，而现在只有 SQLite 一个实现，没有第二个方言来证明新接口。
	"store/sql.go": {"database/sql"},
	// 第 5 步移出内核
	"modelhub/helpers.go": {"net/http"},
	"modelhub/sse.go":     {"net/http"},
	// 例外：沙箱接口签名只用到 exec.Cmd 类型，没有实际读写（18 方案第五节）
	"toolhub/attrs.go": {"os/exec"},
}

// TestKernelNoIO 内核不许新增对文件、数据库、网络包的引用。
func TestKernelNoIO(t *testing.T) {
	fset := token.NewFileSet()
	got := map[string][]string{}
	err := filepath.WalkDir("runtime", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("解析 %s：%v", path, err)
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, "runtime"+string(filepath.Separator)))
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if containsStr(kernelIOBanned, p) {
				got[rel] = append(got[rel], p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var problems []string
	for rel, pkgs := range got {
		allowed := kernelIOAllow[rel]
		for _, p := range pkgs {
			if !containsStr(allowed, p) {
				problems = append(problems, rel+" 新增了对 "+p+" 的引用（内核禁令，见 18 方案）")
			}
		}
	}
	for rel, allowed := range kernelIOAllow {
		for _, p := range allowed {
			if !containsStr(got[rel], p) {
				problems = append(problems, rel+" 已经不再引用 "+p+"：从 kernelIOAllow 里删掉这一条")
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
