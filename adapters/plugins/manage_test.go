package plugins

import (
	"archive/zip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeZip 按 files（路径 → 内容）写一个 zip；路径原样写进 zip 头（用来造 zip-slip）。
func writeZip(t *testing.T, files map[string]string, symlink string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pkg.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if symlink != "" {
		h := &zip.FileHeader{Name: symlink}
		h.SetMode(fs.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("/etc/passwd"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func wantInvalid(t *testing.T, err error, sub string) {
	t.Helper()
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), sub) {
		t.Fatalf("应为 invalid（含 %q）：%v", sub, err)
	}
}

// staging 用完不留东西。
func stagingEmpty(t *testing.T, home string) {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(UserDir(home), StagingDir))
	if len(ents) != 0 {
		t.Fatalf("staging 应清空：%v", ents)
	}
}

// 目录与 zip（含一层顶层目录）都能 Stage；名字与目录名不同也行（按清单 name 放）。
func TestStageDirAndZip(t *testing.T) {
	home := t.TempDir()
	src := mk(t, t.TempDir(), "whatever", "name = \"demo\"\nversion = \"1.2.0\"\nprompt = [\"p.md\"]\n", "p.md")
	s, err := Stage(home, src)
	if err != nil {
		t.Fatal(err)
	}
	if s.Package.Name != "demo" || s.Package.Version != "1.2.0" || filepath.Base(s.Package.Dir) != "demo" {
		t.Fatalf("目录来源：%+v", s.Package)
	}
	s.Close()
	stagingEmpty(t, home)

	z := writeZip(t, map[string]string{"demo-1.2/tenon-plugin.toml": "name = \"demo\"\nprompt = [\"p.md\"]\n", "demo-1.2/p.md": "hi"}, "")
	s, err = Stage(home, z)
	if err != nil {
		t.Fatal(err)
	}
	if s.Package.Name != "demo" || len(s.Package.Prompts) != 1 {
		t.Fatalf("zip 来源：%+v", s.Package)
	}
	s.Close()
	stagingEmpty(t, home)
}

// zip-slip、绝对路径、符号链接、超大、文件数超限、坏清单、来源不存在都报 invalid，且不留临时文件。
func TestStageRejects(t *testing.T) {
	home := t.TempDir()
	ok := "name = \"demo\"\n"
	cases := []struct {
		name, sub string
		src       func() string
	}{
		{"zip-slip", "跳出目录", func() string {
			return writeZip(t, map[string]string{"tenon-plugin.toml": ok, "../../evil.txt": "x"}, "")
		}},
		{"中间的 ..", "跳出目录", func() string {
			return writeZip(t, map[string]string{"tenon-plugin.toml": ok, "a/../../evil.txt": "x"}, "")
		}},
		{"绝对路径", "不安全", func() string {
			return writeZip(t, map[string]string{"tenon-plugin.toml": ok, "/tmp/evil.txt": "x"}, "")
		}},
		{"反斜杠", "不安全", func() string {
			return writeZip(t, map[string]string{"tenon-plugin.toml": ok, `..\evil.txt`: "x"}, "")
		}},
		{"zip 里的符号链接", "符号链接", func() string {
			return writeZip(t, map[string]string{"tenon-plugin.toml": ok}, "link")
		}},
		{"超大", "总大小", func() string {
			return writeZip(t, map[string]string{"tenon-plugin.toml": ok, "big.bin": strings.Repeat("x", 2048)}, "")
		}},
		{"文件太多", "文件数", func() string {
			fs := map[string]string{"tenon-plugin.toml": ok}
			for i := 0; i < 5; i++ {
				fs[filepath.ToSlash(filepath.Join("f", string(rune('a'+i))))] = "x"
			}
			return writeZip(t, fs, "")
		}},
		{"坏清单", "未知字段", func() string { return mk(t, t.TempDir(), "demo", "name = \"demo\"\nbogus = 1\n") }},
		{"清单 TOML 语法错", "清单解析失败", func() string { return mk(t, t.TempDir(), "demo", "name = \n") }},
		{"name 不合法", "不合法", func() string { return mk(t, t.TempDir(), "demo", "name = \"Bad_Name\"\n") }},
		{"没有清单", "没有 tenon-plugin.toml", func() string { d := t.TempDir(); os.WriteFile(filepath.Join(d, "x"), nil, 0o644); return d }},
		{"不是 zip 的文件", "目录或 .zip", func() string { p := filepath.Join(t.TempDir(), "a.tar"); os.WriteFile(p, nil, 0o644); return p }},
		{"来源不存在", "不存在", func() string { return filepath.Join(t.TempDir(), "nope") }},
	}
	oldF, oldB := maxFiles, maxBytes
	maxFiles, maxBytes = 4, 1024
	t.Cleanup(func() { maxFiles, maxBytes = oldF, oldB })
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Stage(home, c.src())
			wantInvalid(t, err, c.sub)
			stagingEmpty(t, home)
		})
	}
	if _, err := os.Stat(filepath.Join(home, "evil.txt")); err == nil {
		t.Fatal("zip-slip 不应写出包目录")
	}
}

// 目录来源里的符号链接也拒绝（Windows 上建符号链接要权限，跳过）。
func TestStageDirSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 建符号链接要权限")
	}
	src := mk(t, t.TempDir(), "demo", "name = \"demo\"\n")
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	_, err := Stage(t.TempDir(), src)
	wantInvalid(t, err, "符号链接")
}

// 安装 → 覆盖安装（旧包进 .trash）→ Undo 复原；Remove 进 .trash、手工挪回即恢复；停用后 Load 不再返回。
func TestInstallRemoveSetEnabled(t *testing.T) {
	home := t.TempDir()
	install := func(version string) Change {
		t.Helper()
		src := mk(t, t.TempDir(), "demo", "name = \"demo\"\nversion = \""+version+"\"\n")
		s, err := Stage(home, src)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ch, err := Install(home, s)
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
	loadedVersion := func() string {
		t.Helper()
		ps, _, err := Load(home, t.TempDir(), false)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.Name == "demo" {
				return p.Version
			}
		}
		return ""
	}

	if ch := install("1.0.0"); ch.Trashed != "" || loadedVersion() != "1.0.0" {
		t.Fatalf("首次安装：%+v %q", ch, loadedVersion())
	}
	ch := install("2.0.0")
	if ch.Trashed == "" || !strings.HasPrefix(filepath.Base(ch.Trashed), "demo-") || loadedVersion() != "2.0.0" {
		t.Fatalf("覆盖安装：%+v %q", ch, loadedVersion())
	}
	if b, err := os.ReadFile(filepath.Join(ch.Trashed, "tenon-plugin.toml")); err != nil || !strings.Contains(string(b), "1.0.0") {
		t.Fatalf("旧包应在 .trash：%s %v", b, err)
	}
	if err := ch.Undo(); err != nil || loadedVersion() != "1.0.0" {
		t.Fatalf("Undo 应复原旧包：%v %q", err, loadedVersion())
	}
	stagingEmpty(t, home)

	// 停用 → Load 不返回、ScanUser 仍列出；状态没变时 changed = false
	if _, changed, err := SetEnabled(home, "demo", false); err != nil || !changed {
		t.Fatalf("停用：%v %v", changed, err)
	}
	if loadedVersion() != "" {
		t.Fatal("停用后 Load 不应返回")
	}
	if us, _ := ScanUser(home); len(us) != 1 || us[0].Enabled || us[0].Package == nil {
		t.Fatalf("ScanUser 应列出停用的包：%+v", us)
	}
	if _, changed, _ := SetEnabled(home, "demo", false); changed {
		t.Fatal("已停用再停用不应算改动")
	}
	ch2, changed, err := SetEnabled(home, "demo", true)
	if err != nil || !changed || loadedVersion() != "1.0.0" {
		t.Fatalf("启用：%v %v", changed, err)
	}
	if err := ch2.Undo(); err != nil || loadedVersion() != "" {
		t.Fatalf("启用的 Undo 应回到停用：%v", err)
	}
	SetEnabled(home, "demo", true)

	// Remove → .trash；手工挪回即恢复
	rm, err := Remove(home, "demo")
	if err != nil || rm.Trashed == "" || loadedVersion() != "" {
		t.Fatalf("卸载：%+v %v", rm, err)
	}
	if err := os.Rename(rm.Trashed, filepath.Join(UserDir(home), "demo")); err != nil {
		t.Fatal(err)
	}
	if loadedVersion() != "1.0.0" {
		t.Fatal("从 .trash 挪回后应恢复")
	}
	if _, err := Remove(home, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("卸载不存在的应 ErrNotFound：%v", err)
	}
	if _, _, err := SetEnabled(home, "../x", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("非法名字应 ErrInvalid：%v", err)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.2.0", "1.10.0", -1, true}, {"v2", "1.9.9", 1, true}, {"1.0", "1.0.0", 0, true},
		{"1.0.0-beta", "1.0.0", 0, true}, {"x", "1.0", 0, false}, {"", "1", 0, false},
	} {
		if got, ok := CompareVersions(c.a, c.b); got != c.want || ok != c.ok {
			t.Errorf("%s vs %s：%d %v", c.a, c.b, got, ok)
		}
	}
}
