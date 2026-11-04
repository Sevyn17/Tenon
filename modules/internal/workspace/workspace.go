// Package workspace 标准模块共用的工作区小工具（instructions、skills 原先各留一份，2026-11-04 审查 P2 重复实现）。
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ProjectRoot 从 dir 向上找 .git；找不到返回 dir。"不存在"以外的错误直接返回。
func ProjectRoot(dir string) (string, error) {
	for d := dir; ; {
		_, err := os.Stat(filepath.Join(d, ".git"))
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("探测项目根 %s: %w", d, err)
		}
		p := filepath.Dir(d)
		if p == d {
			return dir, nil
		}
		d = p
	}
}

// ClipUTF8 截到至多 n 字节，回退到 UTF-8 字符边界。
func ClipUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
