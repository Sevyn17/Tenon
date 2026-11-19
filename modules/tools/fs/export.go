package fs

// 给文件检查点（docs/50 M9）用的导出面：路径解析、原子写、工作区遍历。
// 实现在 modules/internal/wsfs（标准模块之间不能互相 import，检查点直接用 wsfs）；
// 这里是给框架外调用方的薄包装，规则与工具内部一致（越界、符号链接 / junction、.gitignore）。

import (
	"context"
	"path/filepath"

	"github.com/Sevyn17/tenon/modules/internal/wsfs"
)

// Resolve 把 p（相对 root 或绝对路径）解析到 root 之内，拒绝越界：字面检查一次，
// 再按真实路径（跟随符号链接与 Windows junction）检查一次。返回清理后的绝对路径。
// 目标可以还不存在（新建文件）。
func Resolve(root, p string) (string, error) {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return wsfs.Resolve(root, p)
}

// Within 报告绝对路径 p 是否在 root 之内（含 root 本身），只做字面比较。
// Windows 上 filepath.Rel 按不区分大小写比较卷名与路径段。
func Within(root, p string) bool { return wsfs.Within(filepath.Clean(root), filepath.Clean(p)) }

// RealPath 解析绝对路径的真实位置（逐段跟随符号链接与 junction；不存在的尾部原样接上）。
func RealPath(p string) (string, error) { return wsfs.RealPath(p) }

// WriteAtomic 原子写文件：同目录临时文件 + rename，保留原文件权限，自动建父目录。
func WriteAtomic(p string, data []byte) error { return wsfs.WriteAtomic(p, data) }

// FileStat 一次遍历里一个文件的 mtime 与大小。
type FileStat = wsfs.FileStat

// ErrTooManyFiles Walk 遇到的文件数超过上限。
var ErrTooManyFiles = wsfs.ErrTooManyFiles

// Walk 遍历 root 下的普通文件（遵守各级 .gitignore，跳过 .git / node_modules / vendor 与符号链接），
// 返回 相对路径（正斜杠）→ mtime/size。不排序、不截断：文件数超过 max（>0 时生效）返回 ErrTooManyFiles。
func Walk(ctx context.Context, root string, max int) (map[string]FileStat, error) {
	return wsfs.Walk(ctx, root, max)
}
