//go:build !windows

package web

import "os"

// restrict 只本人可读写（0600；WriteFile / OpenFile 不改已存在文件的权限，这里显式改）。
func restrict(f *os.File, _ string) error { return f.Chmod(0o600) }
