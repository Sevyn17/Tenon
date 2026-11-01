// Package tools：内置工具实现（文件、搜索、shell、web、todo、技能、AGENTS.md）。
//
// 文件类工具限制在工作目录之内（resolve）。
package fs

import (
	"errors"
	"path/filepath"
	"strings"
)

// resolve 把相对路径解析到 root 下，拒绝越界。
func resolve(root, p string) (string, error) {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("路径超出工作目录: " + p)
	}
	return p, nil
}
