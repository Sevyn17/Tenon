// Package tools：内置工具实现（文件、搜索、shell、web、todo、技能、AGENTS.md）。
//
// 文件类工具限制在工作目录之内（resolve）。
package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// resolve 把相对路径解析到 root 下，拒绝越界。字面检查之后再按真实路径检查一次：
// 工作目录里的符号链接 / junction 可能指向外面（克隆来的仓库就能带一个），只看字面会被绕过。
func resolve(root, p string) (string, error) {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	if !within(root, p) {
		return "", errors.New("路径超出工作目录: " + p)
	}
	realRoot, err := realPath(root)
	if err != nil {
		realRoot = root
	}
	realP, err := realPath(p)
	if err != nil {
		return "", err
	}
	if !within(realRoot, realP) {
		return "", errors.New("路径经符号链接指向工作目录之外: " + p)
	}
	return p, nil
}

// maxLinks 解析一条路径时最多跟随的链接数（防环）。
const maxLinks = 40

// within p 是否在 root 之内（含 root 本身）。
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// realPath 解析绝对路径 p 的真实路径：逐段往下走，遇到链接就换成它指向的位置；
// 还不存在的尾部原样接上（写新文件时目标本身还不存在）。
//
// 不用 filepath.EvalSymlinks：Windows 上它不跟随 junction（Go 把 junction 报成
// ModeIrregular），而普通用户不用管理员权限就能建 junction。os.Readlink 两种都能读。
func realPath(p string) (string, error) {
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
