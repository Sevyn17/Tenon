package permission

import (
	"fmt"
	"testing"
)

// 会话权限状态有上限（2026-11-04 审查 P2 资源：原先每个打开过的会话都留一份，只增不减）。
// 淘汰掉的会话再用到时经 Load 从持久状态读回。
func TestSessionStateBounded(t *testing.T) {
	old := MaxCached
	MaxCached = 8
	defer func() { MaxCached = old }()
	loads := map[string]int{}
	s := &Sessions{Default: Workspace, Load: func(id string) (Mode, []string) {
		loads[id]++
		if id == "s0" {
			return ReadOnly, []string{"shell(go test:*)"}
		}
		return "", nil
	}}
	for i := 0; i < 100; i++ {
		s.Mode(fmt.Sprintf("s%d", i))
	}
	if n := len(s.state); n > MaxCached {
		t.Fatalf("缓存应不超过 %d，得 %d", MaxCached, n)
	}
	if m := s.Mode("s0"); m != ReadOnly || loads["s0"] != 2 {
		t.Fatalf("淘汰后应经 Load 读回：mode=%s loads=%d", m, loads["s0"])
	}
	if r := s.SessionRules("s0"); len(r) != 1 {
		t.Fatalf("会话规则应读回：%v", r)
	}
}

// 没有 Load（状态只在内存）时不淘汰，否则会丢会话规则。
func TestSessionStateNoLoadKept(t *testing.T) {
	old := MaxCached
	MaxCached = 4
	defer func() { MaxCached = old }()
	s := &Sessions{Default: Workspace}
	for i := 0; i < 20; i++ {
		s.Mode(fmt.Sprintf("s%d", i))
	}
	if len(s.state) != 20 {
		t.Fatalf("没有 Load 时不应淘汰：%d", len(s.state))
	}
}
