// Package fs：文件与搜索工具（read_file / write_file / edit / grep / glob / list_dir）。
//
// 文件类工具限制在工作目录之内（resolve）。
package fs

import "github.com/Sevyn17/tenon/modules/internal/wsfs"

// resolve 把相对路径解析到 root 下，拒绝越界。字面检查之后再按真实路径检查一次：
// 工作目录里的符号链接 / junction 可能指向外面（克隆来的仓库就能带一个），只看字面会被绕过。
// 实现在 modules/internal/wsfs（文件检查点共用）。
func resolve(root, p string) (string, error) { return wsfs.Resolve(root, p) }

// within p 是否在 root 之内（含 root 本身）。
func within(root, p string) bool { return wsfs.Within(root, p) }

// realPath 解析绝对路径 p 的真实路径（跟随符号链接与 junction；不存在的尾部原样接上）。
func realPath(p string) (string, error) { return wsfs.RealPath(p) }
