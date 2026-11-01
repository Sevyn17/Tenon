// Package web 共享前端（React + TypeScript + Vite）的构建产物。桌面端嵌入 dist，以后 api-server 同样嵌入。
// 先在本目录执行 pnpm install && pnpm build 生成 dist，再编译引用本包的 Go 程序。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist 前端构建产物（根为 dist/ 目录内部，含 index.html）。
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // 嵌入路径固定，不会出错
	}
	return sub
}
