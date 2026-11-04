package permission

import (
	"encoding/json"
	"testing"
)

// 启动器类命令（npm run / python / bash -c …）"本次会话内都允许"不生成前缀规则：前缀规则会放行
// 启动器执行的任意内容（2026-11-04 审查 P1-61：原先 permission 自己那份拆分代码没有这条判断，
// 约束清单写的停止线从未生效，`npm run test` 生成 `npm run:*`）。
func TestSessionAllowStopsAtLaunchers(t *testing.T) {
	for _, c := range []string{"npm run test", "python x.py", "bash -c ls", "cd x; npx vitest", "go run ./cmd"} {
		args, _ := json.Marshal(map[string]string{"command": c})
		if rs := SessionAllow("shell", args); len(rs) != 0 {
			t.Errorf("%q 不应生成会话规则，得 %+v", c, rs)
		}
	}
	if rs := SessionAllow("shell", json.RawMessage(`{"command":"go test ./..."}`)); len(rs) != 1 || rs[0].Pattern != "go test:*" {
		t.Fatalf("普通命令仍应生成前缀规则：%+v", rs)
	}
}
