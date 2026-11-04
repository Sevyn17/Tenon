package fs

// 权限项声明（06 - 工具权限设计方案 §9.3）。
//
// 两项：fs.read（读文件 / 搜索 / 列目录）与 fs.write（edit / write_file）。
// fs.write 是 match 型且 Bounded：参数里的路径是数据，规范化到工作目录内比对即构成边界。

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"ybase/runtime/toolhub"
)

// 权限项 id。
const (
	ItemRead  = "fs.read"
	ItemWrite = "fs.write"
)

// Items 本插件声明的权限项。root 为工作目录（Subject 把路径规范化到它之下）。
func Items(root string) []toolhub.Item {
	root, _ = filepath.Abs(root)
	return []toolhub.Item{
		{
			ID:    ItemRead,
			Title: "读取工作目录内的文件",
			Type:  toolhub.ItemSwitch,
			Class: toolhub.EffectRead,
			Suggest: map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionAllow,
				"ask":       toolhub.ActionAllow,
				"workspace": toolhub.ActionAllow,
				"full":      toolhub.ActionAllow,
			},
		},
		{
			ID:      ItemWrite,
			Title:   "修改工作目录内的文件",
			Type:    toolhub.ItemMatch,
			Class:   toolhub.EffectWrite,
			Bounded: true,
			Subject: func(_ string, args json.RawMessage) ([]string, error) { return writeSubjects(root, args) },
			Suggest: map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionDeny,
				"ask":       toolhub.ActionAsk,
				"workspace": toolhub.ActionAllow,
				"full":      toolhub.ActionAllow,
			},
		},
	}
}

// writeSubjects 从 edit / write_file 的参数里取出被写的路径，统一成相对工作目录的斜杠路径。
// 解析失败或路径为空时返回空列表：匹配不到任何规则，判定方按未命中缺省处理。
func writeSubjects(root string, args json.RawMessage) ([]string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, nil // 参数不合法，工具自己会报错
		}
	}
	if a.Path == "" {
		return nil, nil
	}
	// 权限按真实位置判定：经链接指到工作目录外的路径要报成外部的绝对路径，
	// 不能按字面上的 lnk/... 命中「工作目录内」的规则（工具本身也会拒绝，这里是第二道）。
	if q, err := resolve(root, a.Path); err != nil || q == "" {
		if rp, err := realPath(absUnder(root, a.Path)); err == nil {
			return []string{filepath.ToSlash(rp)}, nil
		}
	}
	return []string{relSlash(root, a.Path)}, nil
}

func absUnder(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(root, p))
}

// relSlash 把路径规范化成相对 root 的斜杠路径；越界或无法相对化时返回清理过的原路径。
func relSlash(root, p string) string {
	q := p
	if !filepath.IsAbs(q) {
		q = filepath.Join(root, q)
	}
	q = filepath.Clean(q)
	rel, err := filepath.Rel(root, q)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(q)
	}
	return filepath.ToSlash(rel)
}
