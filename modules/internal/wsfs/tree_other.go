//go:build !windows && !linux

package wsfs

// 其他平台（macOS 等）：没有接变更通知，每次全量遍历。

func openWatcher(string) (watcher, error) { return nil, errNoWatch }
