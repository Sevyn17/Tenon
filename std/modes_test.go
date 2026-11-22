package std

// 客户端改进 M10：PermModes.List 带档位说明（四个内置档位都有），DefaultMode 给新会话的缺省档位。

import (
	"testing"

	"github.com/Sevyn17/tenon/modules/security/permission"
)

func TestPermModesDescriptionAndDefault(t *testing.T) {
	m := PermModes{P: &permission.Sessions{Default: permission.Ask}}
	l := m.List()
	if len(l) < len(permission.Builtins) {
		t.Fatalf("档位：%+v", l)
	}
	for _, d := range l {
		if d.Builtin && d.Description == "" {
			t.Errorf("内置档位 %s 没有说明", d.ID)
		}
	}
	if got := m.DefaultMode(); got != string(permission.Ask) {
		t.Fatalf("缺省档位应为 ask（permissions.toml 的 mode）：%q", got)
	}
	if got := (PermModes{P: &permission.Sessions{}}).DefaultMode(); got != string(permission.Default) {
		t.Fatalf("没配时应为内置缺省 %s：%q", permission.Default, got)
	}
}
