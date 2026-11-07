package mcp

import (
	"strings"
	"testing"
)

// 插件包来源的服务不展开宿主环境变量（审查 P1-7）；用户自己的配置照常展开。
func TestPackageServerDoesNotExpandHostEnv(t *testing.T) {
	t.Setenv("TENON_TEST_SECRET", "sk-host-secret")
	pkg := Server{Env: map[string]string{"X": "${TENON_TEST_SECRET}"}, noExpand: true}
	for _, kv := range scrubEnvWith(nil, pkg.Env, pkg.expand) {
		if strings.Contains(kv, "sk-host-secret") {
			t.Fatalf("插件包的 env 不应展开宿主密钥: %q", kv)
		}
	}
	if got := pkg.expand("Bearer ${TENON_TEST_SECRET}"); strings.Contains(got, "sk-host-secret") {
		t.Fatalf("插件包的 header 不应展开宿主密钥: %q", got)
	}
	user := Server{}
	if got := user.expand("${TENON_TEST_SECRET}"); got != "sk-host-secret" {
		t.Fatalf("用户配置应照常展开: %q", got)
	}
}
