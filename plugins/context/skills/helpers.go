package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// 以下小工具与 instructions 插件同名函数逻辑相同；插件之间不互相 import，故各留一份。

// projectRoot 向上找 .git；找不到返回工作目录。“不存在”以外的错误直接返回。
func projectRoot(dir string) (string, error) {
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

// userHome 用户全局目录：$YBASE_HOME 或 ~/.ybase；取不到返回空。
func userHome() string {
	if h := os.Getenv("YBASE_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".ybase")
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func clipUTF8(s string, n int) string {
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
