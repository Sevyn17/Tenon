package component

// 改写一个 profile 文件里某个插件的开关与配置（plugins.set）：只动这个插件的 [[plugin]] 行，
// include、mandatory、[kernel] 与其他插件原样保留。TOML 库不保留注释，所以整份重写。
//
// 真正的读和写在 adapters/profile：本文件只保留 appapi 要用的类型与错误。
// appapi 不能引用 adapters，所以 ErrRevision、PluginEdit 留在内核。
// 并发：调用方拿文件内容的 sha256 做条件写，文件在两次之间被改过返回 ErrRevision。

import "errors"

// ErrRevision profile 文件在读取之后被改过（条件写失败）。
var ErrRevision = errors.New("profile 文件已被修改")

// PluginEdit 一次修改：Enabled 非 nil 时改开关；Config 里的键合并进 [plugin.config]（值为 nil 删除该键）。
type PluginEdit struct {
	Name    string
	Enabled *bool
	Config  map[string]any
}
