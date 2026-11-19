// Package wsfs 标准模块共用的工作区文件操作：路径解析（越界、符号链接 / junction 检查）与原子写。
//
// fs 工具与文件检查点（docs/50 M9）共用这一份实现：标准模块之间不能互相 import，
// 所以放在 modules/internal 下（05 3.2）。fs 包的导出函数是这里的薄包装。
package wsfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Resolve 把 p（相对 root 或绝对路径）解析到 root 之内，拒绝越界。字面检查之后再按真实路径检查一次：
// 工作目录里的符号链接 / junction 可能指向外面（克隆来的仓库就能带一个），只看字面会被绕过。
// 目标可以还不存在（新建文件）。root 须为绝对路径。返回清理后的绝对路径。
func Resolve(root, p string) (string, error) {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	if !Within(root, p) {
		return "", errors.New("路径超出工作目录: " + p)
	}
	realRoot, err := RealPath(root)
	if err != nil {
		realRoot = root
	}
	realP, err := RealPath(p)
	if err != nil {
		return "", err
	}
	if !Within(realRoot, realP) {
		return "", errors.New("路径经符号链接指向工作目录之外: " + p)
	}
	return p, nil
}

// maxLinks 解析一条路径时最多跟随的链接数（防环）。
const maxLinks = 40

// Within p 是否在 root 之内（含 root 本身），只做字面比较。
// Windows 上 filepath.Rel 按不区分大小写比较卷名与路径段。
func Within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// RealPath 解析绝对路径 p 的真实路径：逐段往下走，遇到链接就换成它指向的位置；
// 还不存在的尾部原样接上（写新文件时目标本身还不存在）。
//
// 不用 filepath.EvalSymlinks：Windows 上它不跟随 junction（Go 把 junction 报成
// ModeIrregular），而普通用户不用管理员权限就能建 junction。os.Readlink 两种都能读。
func RealPath(p string) (string, error) {
	p = filepath.Clean(p)
	vol := filepath.VolumeName(p)
	parts := strings.Split(strings.TrimPrefix(p[len(vol):], string(filepath.Separator)), string(filepath.Separator))
	cur := vol + string(filepath.Separator)
	links := 0
	for i := 0; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		next := filepath.Join(cur, parts[i])
		fi, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) { // 余下部分还不存在：原样接上
				return filepath.Join(append([]string{next}, parts[i+1:]...)...), nil
			}
			return "", err
		}
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
			cur = next
			continue
		}
		target, err := os.Readlink(next)
		if err != nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				cur = next // 不是链接的特殊文件（如设备）：当作普通段
				continue
			}
			return "", err
		}
		if links++; links > maxLinks {
			return "", errors.New("路径里的链接过多或成环: " + p)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(cur, target)
		}
		// 链接目标可能本身还含链接：从头解析「目标 + 余下部分」。
		rest := filepath.Join(append([]string{target}, parts[i+1:]...)...)
		target = filepath.Clean(rest)
		vol = filepath.VolumeName(target)
		parts = strings.Split(strings.TrimPrefix(target[len(vol):], string(filepath.Separator)), string(filepath.Separator))
		cur = vol + string(filepath.Separator)
		i = -1
	}
	return cur, nil
}

// WriteAtomic 同目录临时文件 + rename，保留原文件权限，自动建父目录。
func WriteAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tenon-edit-*")
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
	_ = os.Chmod(tmp, mode)
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
