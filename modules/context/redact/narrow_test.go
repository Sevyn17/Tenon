package redact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
)

func TestAssignSkipsCode(t *testing.T) {
	r := New()
	keep := []string{
		"const token = createToken(user)",
		"password: process.env.DB_PASSWORD",
		"apiKey = config.apiKey",
		"token = refreshToken",
	}
	for _, s := range keep {
		if got, n := r.String(s); n != 0 {
			t.Errorf("误伤代码 %q → %q", s, got)
		}
	}
	hit := []string{
		`password = "hunter2hunter2"`,
		"API_KEY=ab12cd34ef56gh78",
		"token: 9f8e7d6c5b4a3210",
	}
	for _, s := range hit {
		if got, n := r.String(s); n == 0 || !strings.Contains(got, Placeholder) {
			t.Errorf("漏脱敏 %q → %q", s, got)
		}
	}
}

func TestWriteGuard(t *testing.T) {
	// 04 4.3.1：判定区是纯谓词（不再包 next）。
	deny := func(tool, args string) (component.Verdict, string) {
		return WriteGuardFn(context.Background(), component.ToolCallReq{
			Tool: tool, Args: json.RawMessage(args)})
	}
	// 写文件类工具 + 含占位符 → 拒绝。
	if v, why := deny("write_file", `{"content":"x=`+Placeholder+`"}`); v != component.Deny || why == "" {
		t.Fatalf("应拒绝写回占位符：%v %q", v, why)
	}
	if v, _ := deny("edit", `{"new_string":"x=`+Placeholder+`"}`); v != component.Deny {
		t.Fatalf("edit 也应拒绝")
	}
	// 写文件类工具 + 无占位符 → 不表态（放行给其他判定与内核）。
	if v, _ := deny("write_file", `{"content":"x=1"}`); v != component.Abstain {
		t.Fatalf("无占位符应 Abstain")
	}
	// 非写文件工具 → 不表态。
	if v, _ := deny("read_file", `{"q":"`+Placeholder+`"}`); v != component.Abstain {
		t.Fatalf("非写文件工具应 Abstain")
	}
}
