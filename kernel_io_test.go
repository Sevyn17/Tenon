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
	// 第 4 步核实后留在内核，理由见 18 方案第十节：
	// appid 被十来个标准模块引用，只拼目录名、读一个环境变量，不是读写文件。
	"appid/appid.go": {"os"},
	// EnvSecrets 是 env: 凭证引用的缺省实现，只读一个环境变量。
	"assemble/services.go": {"os"},
	// Parse 校验清单里写的路径确实存在；PromptText 读清单点名的片段。
	// 四个标准模块都调 Parse，不能把读文件搬到 adapters。
	"pkgs/pkgs.go": {"os"},
	// [[ui]] 的 view_file 在解析清单时读，和 Parse 是同一件事。
	"pkgs/ui.go": {"os"},
	// 插件包带定义时，把清单点名的目录交给 agentdef.FS。目录由调用方给，不是内核自己扫。
	"pkgs/agents.go": {"os"},
	// 第 3 步评估后保留：store.SQL 的方法签名用到 *sql.DB，是类型引用不是读写。
	// 去掉它要另造一套不带 *sql.DB 的迁移接口，而现在只有 SQLite 一个实现，没有第二个方言来证明新接口。
	"store/sql.go": {"database/sql"},
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
