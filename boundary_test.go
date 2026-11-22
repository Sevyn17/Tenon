package tenon_test

// 边界守卫（Mortise docs/61 T7、docs/62 §1、§5）：
//  1. 根包 tenon 不得依赖 modules/（标准模块经 tenon/std 按需导入，K6）；
//  2. web/src（@tenon/client）非测试源码不得含中文字符串、不得引入 UI 库或其他运行时依赖；
//  3. tenon 非测试 Go 代码不得出现产品词（mortise、byok、gateway-adapter、store-sqlite），
//     字符串里不得出现产品命令根 platform.（appapi/ext.go 的冻结历史根除外）。
//     注释白名单见 productCommentAllow：只允许「引用 Mortise 设计文档」这一种写法。

import (
	"encoding/json"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

const tenonModule = "github.com/Sevyn17/tenon"

// TestRootNoModules 根包的依赖闭包里不得有 modules/ 下的包。
func TestRootNoModules(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", tenonModule).Output()
	if err != nil {
		t.Skipf("go list 不可用：%v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, tenonModule+"/modules/") {
			t.Errorf("根包依赖了标准模块 %s（应经 tenon/std 导入）", p)
		}
	}
}

// webAllowedImports web/src 允许的非相对导入（仅测试工具）。
var webAllowedImports = map[string]bool{"vitest": true}

var importRe = regexp.MustCompile(`(?m)(?:^|\s)(?:import|export)\s[^;'"]*?from\s+["']([^"']+)["']|import\(\s*["']([^"']+)["']\s*\)|^\s*import\s+["']([^"']+)["']`)

// TestWebClientBoundary web/src 不含中文字符串、不引入 UI 依赖；package.json 只有开发依赖。
func TestWebClientBoundary(t *testing.T) {
	dir := filepath.Join("web", "src")
	files, err := filepath.Glob(filepath.Join(dir, "*.ts"))
	if err != nil || len(files) == 0 {
		t.Fatalf("找不到 %s：%v", dir, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, m := range importRe.FindAllStringSubmatch(src, -1) {
			spec := m[1] + m[2] + m[3]
			if strings.HasPrefix(spec, ".") || webAllowedImports[spec] {
				continue
			}
			t.Errorf("%s 引入了 %q：@tenon/client 不依赖 UI 库或其他运行时包", f, spec)
		}
		if strings.HasSuffix(f, ".test.ts") {
			continue
		}
		for _, lit := range tsStrings(src) {
			if hasHan(lit) {
				t.Errorf("%s 含中文字符串 %s：界面文案由产品翻译", f, lit)
			}
		}
	}
	var pkg struct {
		Dependencies     map[string]string `json:"dependencies"`
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	b, err := os.ReadFile(filepath.Join("web", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatal(err)
	}
	for name := range pkg.Dependencies {
		t.Errorf("web/package.json 有运行时依赖 %s", name)
	}
	for name := range pkg.PeerDependencies {
		t.Errorf("web/package.json 有 peer 依赖 %s", name)
	}
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// tsStrings TS 源码里的字符串字面量（跳过注释；模板字符串整段算一个）。
func tsStrings(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "//"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return out
			}
			i += j
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return out
			}
			i += j + 4
		case s[i] == '"' || s[i] == '\'' || s[i] == '`':
			q, j := s[i], i+1
			for j < len(s) && s[j] != q {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) {
				return out
			}
			out = append(out, s[i:j+1])
			i = j + 1
		default:
			i++
		}
	}
	return out
}

// productWords 产品词（不分大小写）。
var productWords = regexp.MustCompile(`(?i)mortise|byok|gateway-adapter|store-sqlite`)

// productCommentAllow 注释白名单：引用 Mortise 设计文档的出处（「Mortise docs/62 K2」「Mortise 客户端改进 M11」）。
// 只认这一种写法；注释里其余的产品词一律算违规。
var productCommentAllow = regexp.MustCompile(`Mortise (docs/|客户端改进)`)

// legacyRootFile 允许出现 "platform." 字符串的文件（冻结的历史产品根表）。
var legacyRootFile = filepath.Join("appapi", "ext.go")

// TestNoProductWords tenon 非测试 Go 代码不出现产品词。
func TestNoProductWords(t *testing.T) {
	skip := map[string]bool{".git": true, "node_modules": true, "docs": true, "testdata": true, ".tenon": true}
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fs := token.NewFileSet()
		f := fs.AddFile(p, -1, len(src))
		var sc scanner.Scanner
		sc.Init(f, src, nil, scanner.ScanComments)
		for {
			pos, tok, lit := sc.Scan()
			if tok == token.EOF {
				break
			}
			at := fs.Position(pos)
			text := lit
			if tok == token.COMMENT {
				text = productCommentAllow.ReplaceAllString(text, "")
			}
			if w := productWords.FindString(text); w != "" {
				t.Errorf("%s:%d：出现产品词 %q（%s）", p, at.Line, w, strings.TrimSpace(firstLine(lit)))
			}
			if tok == token.STRING && p != legacyRootFile && strings.Contains(lit, `"platform.`) {
				t.Errorf("%s:%d：出现产品命令根 platform.（%s）", p, at.Line, lit)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
