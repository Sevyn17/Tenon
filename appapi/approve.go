package appapi

import "strings"

// ApproveWord 把界面上的自由文本回答规整成内核认的 "approve" / "deny"（2026-11-04 审查 P2 归属：
// 中英文"批准"词表是产品策略，从 runtime/host 移来）。JSON 回答原样返回，由内核解析。
func ApproveWord(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{") {
		return t
	}
	switch strings.ToLower(t) {
	case "y", "yes", "ok", "true", "1", "allow", "approve", "approved", "批准", "同意", "是", "允许":
		return "approve"
	}
	return "deny"
}
