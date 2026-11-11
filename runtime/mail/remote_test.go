package mail

import "testing"

func TestParseRemoteAddress(t *testing.T) {
	a, err := ParseAddress("remote:host1:9000/agent:worker")
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != AddrRemote || a.Host != "host1:9000" || a.ID != "worker" {
		t.Fatalf("remote 解析 = %+v", a)
	}
	if got := a.String(); got != "remote:host1:9000/agent:worker" {
		t.Fatalf("remote 还原 = %q", got)
	}
	// 非法：缺 host、子类型不是 agent。
	for _, bad := range []string{"remote:/agent:x", "remote:host/session:x", "remote:host/agent:"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Fatalf("%q 应报错", bad)
		}
	}
}
