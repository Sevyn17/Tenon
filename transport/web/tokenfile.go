package web

// 令牌文件（穿插项 8）：网页服务启动时把带令牌的地址写进只本人可读的文件，脚本和别的工具从这里取，
// 不必去翻终端输出。退出时删掉。Unix 上是 0600；Windows 上把 DACL 换成只有当前用户（见 tokenfile_windows.go）。

import (
	"fmt"
	"os"
	"path/filepath"
)

// TokenFileName 缺省令牌文件名（放在哪由应用决定，命令行程序放在用户目录下）。
const TokenFileName = "server.token"

// WriteTokenFile 把带令牌的地址写进只本人可读的文件：先建空文件、收紧权限，再写内容：令牌不会出现在权限还没收紧的文件里。
// 已有的旧文件同样先收紧再覆盖。返回删除函数。
func WriteTokenFile(path, url string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := restrict(f, path); err != nil {
		f.Close()
		return nil, fmt.Errorf("收紧令牌文件权限：%w", err)
	}
	if err := f.Truncate(0); err == nil {
		_, err = f.WriteString(url + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
	} else {
		f.Close()
		return nil, err
	}
	return func() { os.Remove(path) }, nil
}
