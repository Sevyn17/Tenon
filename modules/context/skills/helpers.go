package skills

import (
	"os"
	"path/filepath"
)

// 项目根与 UTF-8 截断已移到 modules/internal/workspace（审查 P2 重复实现）；以下是本插件自己的小工具。

// userHome 用户全局目录：$TENON_HOME 或 ~/.tenon；取不到返回空。
func userHome() string {
	if h := os.Getenv("TENON_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".tenon")
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
