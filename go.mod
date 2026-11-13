module github.com/Sevyn17/tenon

go 1.26.0

// v0.1.0 的文档文件名含全角标点，Go 模块 zip 不接受，go get 下载不了。
retract v0.1.0

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/dop251/goja v0.0.0-20261028213108-85be1416108e
	github.com/google/uuid v1.6.0
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
)

require (
	github.com/dlclark/regexp2/v2 v2.11.0 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20260802141513-ef3492d7dac3 // indirect
)
