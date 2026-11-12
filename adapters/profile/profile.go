// Package profile 从磁盘读写 profile 文件。
//
// 放在内核之外：runtime/component 只解析已经读进来的文本（ParseProfile），
// 不决定路径、不打开文件。include 展开时内核通过回调要文本，真正的读在这里。
package profile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Sevyn17/tenon/runtime/component"
)

// Load 读 profile 文件并展开 include。path 不要求已经是绝对路径。
func Load(path string) (*component.Profile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return component.ParseProfile(string(b), filepath.Dir(abs), read, abs)
}

// read 给 ParseProfile 展开 include 用。循环检测在内核里，按调用方给的路径字符串做。
func read(abs string) ([]byte, error) {
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	return b, nil
}
