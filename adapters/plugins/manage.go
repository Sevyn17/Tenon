package plugins

// 插件包安装、卸载、启停（客户端改进 M11 配置T22，spike-config 第六节）。只动用户级 <home>/plugins：
//
//	<home>/plugins/<名>                  已安装的包
//	<home>/plugins/.staging/pkg-*/        预览、安装时的临时副本（用完删掉）
//	<home>/plugins/.trash/<名>-<时间>     覆盖安装、卸载时挪走的旧包（可手工挪回恢复）
//	<home>/plugins-enabled.toml           启停记录：disabled = ["名", ...]（Load 跳过这些包）
//
// 以 . 开头的子目录 Discover 本来就跳过，staging 与 trash 不会被当成包加载。
// 来源是本地目录或 .zip：拒绝符号链接、绝对路径与 ..，限文件数与总大小（maxFiles / maxBytes）。

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/pkgs"
)

// 错误类别：调用方据此映射成协议错误码（invalid / not_found）。
var (
	ErrInvalid  = errors.New("插件包不合法")
	ErrNotFound = errors.New("没有这个插件包")
)

// 目录与文件名。
const (
	StagingDir  = ".staging"
	TrashDir    = ".trash"
	EnabledFile = "plugins-enabled.toml"
)

// 来源上限（测试可调）。
var (
	maxFiles       = 2000
	maxBytes int64 = 100 << 20
)

// UserDir 用户级插件包目录 <home>/plugins。
func UserDir(home string) string { return filepath.Join(home, "plugins") }

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w：%s", ErrInvalid, fmt.Sprintf(format, a...))
}

// —— 启停记录 ——

type enabledFile struct {
	Disabled []string `toml:"disabled"`
}

// enabledPath 启停记录位置：缺省 <home>/plugins-enabled.toml，宿主可改（apphost.Paths.PluginsEnabled）。
func enabledPath(home string) string { return appid.UserPathIn(home, EnabledFile) }

// Disabled 用户停用的插件包名（记录文件不存在 = 没有）。
func Disabled(home string) (map[string]bool, error) {
	out := map[string]bool{}
	if home == "" {
		return out, nil
	}
	var f enabledFile
	if _, err := toml.DecodeFile(enabledPath(home), &f); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("读 %s：%w", enabledPath(home), err)
	}
	for _, n := range f.Disabled {
		out[n] = true
	}
	return out, nil
}

// writeDisabled 写启停记录，返回把文件改回原样的 undo。
func writeDisabled(home string, set map[string]bool) (func() error, error) {
	p := enabledPath(home)
	old, err := os.ReadFile(p)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	names := make([]string, 0, len(set))
	for n, on := range set {
		if on {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	qs := make([]string, len(names))
	for i, n := range names {
		qs[i] = strconv.Quote(n)
	}
	body := "# 插件包启停记录（packages.setEnabled / plugin enable、disable 维护）\ndisabled = [" + strings.Join(qs, ", ") + "]\n"
	if err := writeAtomic(p, []byte(body)); err != nil {
		return nil, err
	}
	return func() error {
		if !existed {
			return os.Remove(p)
		}
		return writeAtomic(p, old)
	}, nil
}

func writeAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".plugins-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// —— 已安装的用户级包 ——

// UserEntry 用户级插件包目录里的一个包（含停用的与清单坏了的）。
type UserEntry struct {
	Name    string
	Dir     string
	Enabled bool
	Package *pkgs.Package // 清单解析失败时为 nil
	Err     error
}

// ScanUser 列 <home>/plugins 下每个子目录（跳过 . 开头的），按名字排序。
func ScanUser(home string) ([]UserEntry, error) {
	if home == "" {
		return nil, nil
	}
	dis, err := Disabled(home)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(UserDir(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []UserEntry
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(UserDir(home), e.Name())
		u := UserEntry{Name: e.Name(), Dir: dir, Enabled: !dis[e.Name()]}
		if _, err := os.Stat(filepath.Join(dir, pkgs.ManifestName)); err != nil {
			u.Err = fmt.Errorf("没有 %s", pkgs.ManifestName)
		} else if u.Package, u.Err = parseOne(dir, pkgs.ScopeUser); u.Err != nil {
			u.Package = nil
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// userDirOf 已安装的用户级包目录；没有返回 ErrNotFound。
func userDirOf(home, name string) (string, error) {
	if home == "" {
		return "", errors.New("取不到用户目录")
	}
	if !pkgs.ValidName(name) {
		return "", invalidf("插件包名 %q 不合法（小写字母、数字与连字符）", name)
	}
	dir := filepath.Join(UserDir(home), name)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%w：用户级插件包目录里没有 %s", ErrNotFound, name)
	}
	return dir, nil
}

// —— 预览（复制或解压到 staging 再解析）——

// Staged 复制到 staging 并解析好的包。用完调 Close 删掉临时副本（Install 成功后副本已挪走，Close 只清空壳）。
type Staged struct {
	Package *pkgs.Package
	root    string // .staging/pkg-* 临时目录
}

// Close 删掉临时副本。
func (s *Staged) Close() error {
	if s == nil || s.root == "" {
		return nil
	}
	return os.RemoveAll(s.root)
}

// Stage 把 src（本地目录或 .zip）复制 / 解压到 <home>/plugins/.staging/pkg-*/<名>，按用户级口径解析清单
// （含 [[ui]] 视图校验）。清单在来源根目录，或 zip 里唯一的顶层目录下。来源或清单不合法时错误包着 ErrInvalid。
func Stage(home, src string) (*Staged, error) {
	if home == "" {
		return nil, errors.New("取不到用户目录")
	}
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, invalidf("缺少来源路径")
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, invalidf("%v", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, invalidf("来源 %s 不存在", abs)
	}
	base := filepath.Join(UserDir(home), StagingDir)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(base, "pkg-")
	if err != nil {
		return nil, err
	}
	s := &Staged{root: root}
	fail := func(err error) (*Staged, error) {
		_ = s.Close()
		return nil, err
	}
	raw := filepath.Join(root, ".raw") // 点开头：不会和包名（小写字母、数字、连字符）撞
	switch {
	case st.IsDir():
		err = copyTree(abs, raw)
	case strings.EqualFold(filepath.Ext(abs), ".zip"):
		err = unzip(abs, raw)
	default:
		err = invalidf("来源须是目录或 .zip 文件：%s", abs)
	}
	if err != nil {
		return fail(err)
	}
	top, err := manifestRoot(raw)
	if err != nil {
		return fail(err)
	}
	var head struct {
		Name string `toml:"name"`
	}
	if _, err := toml.DecodeFile(filepath.Join(top, pkgs.ManifestName), &head); err != nil {
		return fail(invalidf("清单解析失败：%v", err))
	}
	if !pkgs.ValidName(head.Name) {
		return fail(invalidf("清单 name %q 不合法（小写字母、数字与连字符）", head.Name))
	}
	dir := filepath.Join(root, head.Name)
	if err := os.Rename(top, dir); err != nil {
		return fail(err)
	}
	p, err := parseOne(dir, pkgs.ScopeUser)
	if err != nil {
		return fail(fmt.Errorf("%w：%v", ErrInvalid, err))
	}
	s.Package = p
	return s, nil
}

// manifestRoot 清单所在目录：raw 本身，或 raw 下唯一的子目录（zip 常见的打包方式）。
func manifestRoot(raw string) (string, error) {
	if _, err := os.Stat(filepath.Join(raw, pkgs.ManifestName)); err == nil {
		return raw, nil
	}
	ents, err := os.ReadDir(raw)
	if err != nil {
		return "", err
	}
	if len(ents) == 1 && ents[0].IsDir() {
		sub := filepath.Join(raw, ents[0].Name())
		if _, err := os.Stat(filepath.Join(sub, pkgs.ManifestName)); err == nil {
			return sub, nil
		}
	}
	return "", invalidf("来源根目录（或唯一的顶层目录）里没有 %s", pkgs.ManifestName)
}

// budget 文件数与总字节数上限。
type budget struct {
	files int
	bytes int64
}

func (b *budget) add(n int64) error {
	b.files++
	b.bytes += n
	if b.files > maxFiles {
		return invalidf("文件数超过上限 %d", maxFiles)
	}
	if b.bytes > maxBytes {
		return invalidf("总大小超过上限 %d 字节", maxBytes)
	}
	return nil
}

// fileMode 复制后的权限：保留可执行位（stdio MCP 服务可能是包里的可执行文件），其余 0644。
func fileMode(m fs.FileMode) fs.FileMode {
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// copyTree 复制目录树；遇到符号链接或其它非普通文件报 invalid。
func copyTree(src, dst string) error {
	var b budget
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			return invalidf("不支持符号链接：%s", filepath.ToSlash(rel))
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case !info.Mode().IsRegular():
			return invalidf("不支持的文件类型：%s", filepath.ToSlash(rel))
		}
		if err := b.add(info.Size()); err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		return writeFile(target, in, info.Mode(), -1)
	})
}

// writeFile 把 r 写到 p；limit ≥ 0 时最多读 limit+1 字节，超出报 invalid（防 zip 头里谎报大小）。
func writeFile(p string, r io.Reader, mode fs.FileMode, limit int64) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode(mode))
	if err != nil {
		return err
	}
	if limit >= 0 {
		r = io.LimitReader(r, limit+1)
	}
	n, err := io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if limit >= 0 && n > limit {
		return invalidf("%s 解压后比声明的大小大", filepath.Base(p))
	}
	return nil
}

// zipName 校验并规范化 zip 里的路径：拒绝绝对路径、盘符、..、反斜杠。返回 "" 表示根目录本身。
func zipName(name string) (string, error) {
	if strings.Contains(name, `\`) || strings.Contains(name, ":") || strings.HasPrefix(name, "/") {
		return "", invalidf("zip 里有不安全的路径 %q", name)
	}
	c := path.Clean(name)
	if c == ".." || strings.HasPrefix(c, "../") || slices.Contains(strings.Split(name, "/"), "..") {
		return "", invalidf("zip 里有跳出目录的路径 %q", name)
	}
	if c == "." {
		return "", nil
	}
	return c, nil
}

// unzip 解压到 dst（zip-slip、符号链接、文件数与总大小都在这里拦）。
func unzip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if errors.Is(err, zip.ErrInsecurePath) { // GODEBUG zipinsecurepath=0 时标准库自己先拦下
		if zr != nil {
			zr.Close()
		}
		return invalidf("zip 里有不安全的路径（跳出目录、绝对路径或反斜杠）")
	}
	if err != nil {
		return invalidf("打不开 zip：%v", err)
	}
	defer zr.Close()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	var b budget
	for _, f := range zr.File {
		rel, err := zipName(f.Name)
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 {
			return invalidf("zip 里不支持符号链接：%s", f.Name)
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if r, err := filepath.Rel(dst, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return invalidf("zip 里有跳出目录的路径 %q", f.Name)
		}
		if mode.IsDir() || strings.HasSuffix(f.Name, "/") {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return invalidf("zip 里有不支持的文件类型：%s", f.Name)
		}
		size := int64(f.UncompressedSize64)
		if size < 0 || f.UncompressedSize64 > uint64(maxBytes) {
			return invalidf("总大小超过上限 %d 字节", maxBytes)
		}
		if err := b.add(size); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return invalidf("读 zip 里的 %s：%v", f.Name, err)
		}
		err = writeFile(target, rc, mode, size)
		rc.Close()
		if err != nil {
			if os.IsExist(err) {
				return invalidf("zip 里有重复的路径 %q", f.Name)
			}
			return err
		}
	}
	return nil
}

// —— 安装、卸载、启停 ——

// Change 一次改动的结果。Undo 把磁盘改回改动前（调用方重建运行时失败时调用）。
type Change struct {
	Name    string
	Dir     string // 安装后的包目录（Install）；被挪走的目录（Remove）
	Trashed string // 挪进 .trash 的旧包目录；空 = 没有
	Undo    func() error
}

// trashPath .trash/<名>-<时间>；同一秒重复时加序号。
func trashPath(home, name string) (string, error) {
	base := filepath.Join(UserDir(home), TrashDir)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	p := filepath.Join(base, name+"-"+stamp)
	for i := 2; ; i++ {
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		}
		p = filepath.Join(base, fmt.Sprintf("%s-%s-%d", name, stamp, i))
	}
}

// Install 把 Stage 好的包挪到 <home>/plugins/<名>。同名已存在时先把旧包挪进 .trash（Trashed，可恢复）。
// 启停记录不动（停用中的包覆盖安装后仍是停用）。
func Install(home string, s *Staged) (Change, error) {
	if s == nil || s.Package == nil {
		return Change{}, invalidf("没有可安装的包")
	}
	name := s.Package.Name
	dst := filepath.Join(UserDir(home), name)
	ch := Change{Name: name, Dir: dst}
	if _, err := os.Lstat(dst); err == nil {
		tp, err := trashPath(home, name)
		if err != nil {
			return Change{}, err
		}
		if err := os.Rename(dst, tp); err != nil {
			return Change{}, err
		}
		ch.Trashed = tp
	}
	if err := os.Rename(s.Package.Dir, dst); err != nil {
		if ch.Trashed != "" {
			_ = os.Rename(ch.Trashed, dst)
		}
		return Change{}, err
	}
	ch.Undo = func() error {
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		if ch.Trashed != "" {
			return os.Rename(ch.Trashed, dst)
		}
		return nil
	}
	return ch, nil
}

// CompareVersions 比较两个版本号（可带前缀 v；按 . 分段逐段比数字，忽略 - / + 之后的部分）。
// 返回 -1 / 0 / 1；有一方不是数字形式时 ok 为假。
func CompareVersions(a, b string) (c int, ok bool) {
	parse := func(s string) ([]int, bool) {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		if i := strings.IndexAny(s, "-+"); i >= 0 {
			s = s[:i]
		}
		if s == "" {
			return nil, false
		}
		var out []int
		for _, part := range strings.Split(s, ".") {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				return nil, false
			}
			out = append(out, n)
		}
		return out, true
	}
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			if p < q {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// Remove 把 <home>/plugins/<名> 挪进 .trash（不删除）。启停记录里的这一项一并去掉（重新安装后是启用的）。
func Remove(home, name string) (Change, error) {
	dir, err := userDirOf(home, name)
	if err != nil {
		return Change{}, err
	}
	tp, err := trashPath(home, name)
	if err != nil {
		return Change{}, err
	}
	if err := os.Rename(dir, tp); err != nil {
		return Change{}, err
	}
	ch := Change{Name: name, Dir: dir, Trashed: tp}
	var undoRec func() error
	if dis, err := Disabled(home); err == nil && dis[name] {
		delete(dis, name)
		undoRec, _ = writeDisabled(home, dis)
	}
	ch.Undo = func() error {
		if undoRec != nil {
			if err := undoRec(); err != nil {
				return err
			}
		}
		return os.Rename(tp, dir)
	}
	return ch, nil
}

// SetEnabled 写启停记录（<home>/plugins-enabled.toml）。包须已在用户级目录里（清单坏了也能停用）。
// 状态没变时 Undo 为 nil、changed 为假。
func SetEnabled(home, name string, enabled bool) (ch Change, changed bool, err error) {
	dir, err := userDirOf(home, name)
	if err != nil {
		return Change{}, false, err
	}
	dis, err := Disabled(home)
	if err != nil {
		return Change{}, false, err
	}
	ch = Change{Name: name, Dir: dir}
	if dis[name] == !enabled {
		return ch, false, nil
	}
	if enabled {
		delete(dis, name)
	} else {
		dis[name] = true
	}
	if ch.Undo, err = writeDisabled(home, dis); err != nil {
		return Change{}, false, err
	}
	return ch, true, nil
}
