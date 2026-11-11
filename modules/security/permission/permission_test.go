package permission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func cx(name string, r toolhub.Risk) *toolhub.Cx {
	return &toolhub.Cx{SessionID: "s", CallID: "c1", Tool: &toolhub.Tool{Spec: session.ToolSpec{Name: name}, Risk: r}}
}

func cxAttr(name string, r toolhub.Risk, key string) *toolhub.Cx {
	c := cx(name, r)
	c.Tool.Attrs = map[string]any{key: true}
	return c
}

func eff(name string, r toolhub.Risk, e string, attrs ...string) *toolhub.Cx {
	c := cx(name, r)
	// 测试工具声明一个与效果同类别的 switch 项（无建议值 → 按封顶表取值）
	it := toolhub.Item{ID: "t." + name, Title: name, Type: toolhub.ItemSwitch, Class: e, Owner: "test"}
	c.Tool.Items = []string{it.ID}
	c.Items = []toolhub.Item{it}
	c.Tool.Attrs = map[string]any{}
	for _, a := range attrs {
		c.Tool.Attrs[a] = true
	}
	return c
}

func TestDecideModesAndRules(t *testing.T) {
	args := json.RawMessage(`{"command":"rm -rf /"}`)
	rd := eff("read_file", toolhub.Read, toolhub.EffectRead)
	wr := eff("write_file", toolhub.Write, toolhub.EffectWrite, AttrWorkspace)
	sh := eff("shell", toolhub.Dangerous, toolhub.EffectExec, toolhub.AttrSandboxed)
	net := eff("web_fetch", toolhub.Write, toolhub.EffectNetwork)
	mcp := eff("mcp__x__y", toolhub.Write, toolhub.EffectExternal)
	cases := []struct {
		name string
		p    Policy
		c    *toolhub.Cx
		want Action
	}{
		{"read-only 读放行", Policy{Mode: ReadOnly}, rd, Allow},
		{"read-only 拒写", Policy{Mode: ReadOnly}, wr, Deny},
		{"read-only 命令询问（封顶表 exec=ask）", Policy{Mode: ReadOnly}, sh, AskUser},
		{"未声明权限项一律询问", Policy{Mode: Full}, cx("x_tool", toolhub.Write), AskUser},
		{"read-only 联网询问", Policy{Mode: ReadOnly}, net, AskUser},
		{"ask 写要确认", Policy{Mode: Ask}, wr, AskUser},
		{"ask 命令要确认", Policy{Mode: Ask}, sh, AskUser},
		{"workspace 写放行", Policy{Mode: Workspace}, wr, Allow},
		{"workspace 命令放行", Policy{Mode: Workspace}, sh, Allow},
		{"workspace MCP 询问", Policy{Mode: Workspace}, mcp, AskUser},
		{"缺省档位为 workspace", Policy{}, wr, Allow},
		{"full 放行", Policy{Mode: Full}, mcp, Allow},
		{"deny 规则在 full 下仍生效", Policy{Mode: Full, Rules: []Rule{{Tool: "shell", Pattern: "rm -rf:*", Action: Deny}}}, sh, Deny},
		{"allow 规则免确认", Policy{Mode: Ask, Rules: []Rule{{Tool: "shell", Action: Allow}}}, sh, Allow},
		{"ask 优先于 allow", Policy{Mode: Ask, Rules: []Rule{{Tool: "shell", Action: Allow}, {Tool: "*", Action: AskUser}}}, sh, AskUser},
		{"deny 优先于 allow", Policy{Mode: Full, Rules: []Rule{{Tool: "shell", Action: Allow}, {Tool: "shell", Pattern: "rm:*", Action: Deny}}}, sh, Deny},
		{"不可信输入 allow 降级", Policy{Mode: Full, Untrusted: func(*toolhub.Cx) bool { return true }}, sh, AskUser},
		{"不可信输入规则 allow 降级", Policy{Mode: Ask, Rules: []Rule{{Tool: "shell", Action: Allow}},
			Untrusted: func(*toolhub.Cx) bool { return true }}, sh, AskUser},
		{"destructive 降级", Policy{Mode: Full}, eff("mcp__x__rm", toolhub.Write, toolhub.EffectExternal, AttrDestructive), AskUser},
	}
	for _, tc := range cases {
		if got := tc.p.Decide(tc.c, args).Action; got != tc.want {
			t.Errorf("%s: 得到 %s，期望 %s（%s）", tc.name, got, tc.want, tc.p.Decide(tc.c, args).Reason)
		}
	}
	if (Policy{Mode: ReadOnly}).SandboxOf("shell") != toolhub.SandboxReadOnly || (Policy{}).SandboxOf("shell") != toolhub.SandboxWorkspaceWrite ||
		(Policy{Mode: Full}).SandboxOf("shell") != toolhub.SandboxFull || (Policy{Mode: Full, Sandbox: toolhub.SandboxReadOnly}).SandboxOf("shell") != toolhub.SandboxReadOnly {
		t.Fatal("档位沙箱映射错误")
	}
}

// B7：公共 deny 规则不能靠换个写法绕过——多余空白、启动器包一层、藏进命令替换 / 后台 / 重定向。
// 拆不开的命令里看不到藏了什么，有 deny / ask 规则时至少询问。
func TestShellDenyRuleVariants(t *testing.T) {
	sh := eff("shell", toolhub.Dangerous, toolhub.EffectExec, toolhub.AttrSandboxed)
	p := Policy{Mode: Workspace, Rules: []Rule{{Tool: "shell", Pattern: "rm -rf:*", Action: Deny}}}
	for cmd, want := range map[string]Action{
		"rm -rf x":                Deny,
		"rm  -rf x":               Deny, // 多余空白
		"\trm -rf x":              Deny,
		"cmd /c rm -rf x":         Deny, // 启动器包一层
		`bash -c "rm -rf x"`:      Deny,
		"pwsh -Command rm -rf x":  Deny,
		"git status && rm -rf x":  Deny,
		"echo $(rm -rf x)":        AskUser, // 命令替换：看不到里面
		"rm -rf x &":              Deny,    // 前缀照样命中
		"sleep 1 & rm -rf x":      AskUser, // 单个 &：拆不开
		"go test ./... > out.txt": AskUser, // 写文件重定向：拆不开，有 deny 规则就问
		"go test ./...":           Allow,   // 正常命令不受影响
	} {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		if got := p.Decide(sh, args); got.Action != want {
			t.Errorf("%q: 得到 %s，期望 %s（%s）", cmd, got.Action, want, got.Reason)
		}
	}
	// 没有命令类 deny / ask 规则时，拆不开的命令仍按档位判定（不平白多问）。
	args, _ := json.Marshal(map[string]string{"command": "go test ./... > out.txt"})
	if got := (Policy{Mode: Workspace}).Decide(sh, args).Action; got != Allow {
		t.Fatalf("无规则时按档位放行，得到 %s", got)
	}
}

// B1：[use.permission] 上限只收窄——会话档位再宽，也按上限档位判定；上限更宽时不放宽。
func TestPolicyCapOnlyNarrows(t *testing.T) {
	wr := eff("write_file", toolhub.Write, toolhub.EffectWrite, AttrWorkspace)
	args := json.RawMessage(`{"path":"a.txt"}`)
	full := Policy{Mode: Full, Cap: &Policy{Mode: ReadOnly}}
	if got := full.Decide(wr, args); got.Action != Deny || !strings.Contains(got.Reason, "智能体权限上限") {
		t.Fatalf("上限 read-only 应拒写：%+v", got)
	}
	ro := Policy{Mode: ReadOnly, Cap: &Policy{Mode: Full}}
	if got := ro.Decide(wr, args).Action; got != Deny {
		t.Fatalf("上限更宽不能放宽会话档位，得到 %s", got)
	}
}

// 上限可以串成链（子会话：自己的定义 + 父会话），每一层都判：第二层是 read-only 时照样拒写。
func TestPolicyCapChain(t *testing.T) {
	wr := eff("write_file", toolhub.Write, toolhub.EffectWrite, AttrWorkspace)
	args := json.RawMessage(`{"path":"a.txt"}`)
	p := Policy{Mode: Full, Cap: &Policy{Mode: Full, Cap: &Policy{Mode: ReadOnly}}}
	if got := p.Decide(wr, args).Action; got != Deny {
		t.Fatalf("链上任一层上限 read-only 都应拒写，得到 %s", got)
	}
}

func TestWiderSandboxAsks(t *testing.T) {
	c := eff("shell", toolhub.Dangerous, toolhub.EffectExec, toolhub.AttrSandboxed)
	args := json.RawMessage(`{"command":"go build","sandbox_permissions":"danger-full-access"}`)
	if got := (Policy{Mode: Workspace}).Decide(c, args).Action; got != AskUser {
		t.Fatalf("申请更宽沙箱应确认，得到 %s", got)
	}
	ok := Policy{Mode: Workspace, Rules: []Rule{{Tool: "shell", Pattern: "go build:*", Action: Allow, Sandbox: toolhub.SandboxFull}}}
	if got := ok.Decide(c, args).Action; got != Allow {
		t.Fatalf("带 @沙箱 的规则应放行，得到 %s", got)
	}
	if got := (Policy{Mode: Full}).Decide(c, args).Action; got != Allow {
		t.Fatalf("full 档不算更宽，得到 %s", got)
	}
}

func TestPatternRules(t *testing.T) {
	sh := cx("shell", toolhub.Dangerous)
	root := t.TempDir()
	absArg, _ := json.Marshal(map[string]string{"path": filepath.Join(root, "src", "b.go")})
	p := Policy{Mode: Ask, Root: root, Rules: []Rule{
		{Tool: "shell", Pattern: "npm run test:*", Action: Allow},
		{Tool: "edit", Pattern: "src/**", Action: Allow},
	}}
	cases := []struct {
		c    *toolhub.Cx
		args string
		want Action
	}{
		{sh, `{"command":"npm run test"}`, Allow},
		{sh, `{"command":"npm run test -- -u"}`, Allow},
		{sh, `{"command":"npm run build"}`, AskUser},
		{sh, `{"command":"npm run test && rm -rf ."}`, AskUser},
		{sh, `{"command":"npm run test > out.txt"}`, AskUser},
		{cx("edit", toolhub.Write), `{"path":"src/a/b.go"}`, Allow},
		{cx("edit", toolhub.Write), string(absArg), Allow},
		{cx("edit", toolhub.Write), `{"path":"docs/b.md"}`, AskUser},
	}
	for _, tc := range cases {
		if got := p.Decide(tc.c, json.RawMessage(tc.args)).Action; got != tc.want {
			t.Errorf("%s %s: 得到 %s，期望 %s", tc.c.Tool.Spec.Name, tc.args, got, tc.want)
		}
	}
}

func TestParseRuleAndGlob(t *testing.T) {
	r, err := ParseRule("shell(git status:*)", Allow)
	if err != nil || r.Tool != "shell" || r.Pattern != "git status:*" || r.String() != "shell(git status:*)" {
		t.Fatalf("ParseRule: %+v %v", r, err)
	}
	if r, err := ParseRule("edit", Deny); err != nil || r.Tool != "edit" || r.Pattern != "" {
		t.Fatalf("ParseRule 无模式: %+v %v", r, err)
	}
	for _, bad := range []string{"", "(x)", "shell(x"} {
		if _, err := ParseRule(bad, Allow); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
	globs := []struct {
		pat, s string
		ok     bool
	}{
		{"go test:*", "go test ./...", true},
		{"go test", "go test ./...", false},
		{"*.md", "docs/a.md", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxc", false},
		{"ab*ba", "aba", false},
	}
	for _, g := range globs {
		if Glob(g.pat, g.s) != g.ok {
			t.Errorf("Glob(%q,%q) 应为 %v", g.pat, g.s, g.ok)
		}
	}
}

func TestSessionRule(t *testing.T) {
	sh := eff("shell", toolhub.Dangerous, toolhub.EffectExec, toolhub.AttrSandboxed)
	dec := func(rules []Rule, sb toolhub.SandboxMode, cmd string) Action {
		p := Policy{Mode: Ask, Def: builtinDef(Ask), Rules: rules, Sandbox: sb}
		return p.Decide(sh, json.RawMessage(cmd)).Action
	}
	// 简单命令 → 前缀规则
	rs := SessionAllow("shell", json.RawMessage(`{"command":"go test ./..."}`))
	if len(rs) != 1 || rs[0].Pattern != "go test:*" {
		t.Fatalf("前缀规则: %+v", rs)
	}
	if dec(rs, "", `{"command":"go test ./x"}`) != Allow {
		t.Fatal("同前缀命令应放行")
	}
	if dec(rs, "", `{"command":"go build"}`) != AskUser {
		t.Fatal("别的命令仍要确认")
	}
	// 复合命令：cd / Select-Object / echo 是安全段，git push 生成规则
	c := `{"command":"cd C:\\x; git push origin dev 2>&1 | Select-Object -Last 10; echo \"EXIT=$LASTEXITCODE\"","sandbox_permissions":"danger-full-access"}`
	rs = SessionAllow("shell", json.RawMessage(c))
	if len(rs) != 1 || rs[0].Pattern != "git push:*" || rs[0].Sandbox != toolhub.SandboxFull {
		t.Fatalf("复合命令规则: %+v", rs)
	}
	if dec(rs, toolhub.SandboxWorkspaceWrite, c) != Allow {
		t.Fatal("同一复合命令（含更宽沙箱申请）应放行")
	}
	if dec(rs, toolhub.SandboxWorkspaceWrite, `{"command":"git push origin main","sandbox_permissions":"danger-full-access"}`) != Allow {
		t.Fatal("同类命令同等沙箱申请应放行")
	}
	if dec(rs, toolhub.SandboxWorkspaceWrite, `{"command":"git push; rm -rf x"}`) != AskUser {
		t.Fatal("夹带别的命令仍要确认")
	}
	if dec(rs, toolhub.SandboxWorkspaceWrite, `{"command":"git push > out.txt"}`) != AskUser {
		t.Fatal("写文件重定向仍要确认")
	}
	// 不带沙箱的规则不批准更宽沙箱
	plain := []Rule{{Tool: "shell", Pattern: "git push:*", Action: Allow}}
	if dec(plain, toolhub.SandboxWorkspaceWrite, `{"command":"git push","sandbox_permissions":"danger-full-access"}`) != AskUser {
		t.Fatal("无沙箱模式的规则不应批准更宽沙箱")
	}
	// 规则字符串可往返
	for _, r := range []Rule{rs[0], {Tool: "shell", Pattern: "a*b", Action: Allow}, {Tool: "edit", Action: Allow}} {
		back, err := ParseRule(r.String(), Allow)
		if err != nil || back != r {
			t.Fatalf("往返 %q: %+v %v", r.String(), back, err)
		}
	}
	if _, err := ParseRule("shell[rm]", Deny); err == nil {
		t.Fatal("旧写法 工具[子串] 应报错")
	}
	// 含命令替换无法拆分 → 不生成会话规则（只批准本次）
	if rs = SessionAllow("shell", json.RawMessage(`{"command":"echo $(date)"}`)); len(rs) != 0 {
		t.Fatalf("无法拆分的命令不应生成规则: %+v", rs)
	}
	if r := SessionAllow("edit", json.RawMessage(`{"path":"a"}`)); r[0].Tool != "edit" || r[0].Pattern != "" {
		t.Fatalf("非命令工具应整工具放行: %+v", r)
	}
}

// 已批准（cx.Approved：toolhub 按挂起记录核对参数哈希后置位）的确认类调用直接执行；拒绝类不受批准影响。
//
// 04 4.3.1：判定从 toolhub.Layer 改成 Authorizer（决策服务），这里直接测判定结论。
func TestAuthorizerConfirmAndApproved(t *testing.T) {
	a := Authorizer(func(string) Policy { return Policy{Mode: Ask} })
	c := eff("write_file", toolhub.Write, toolhub.EffectWrite)

	d, err := a.Authorize(context.Background(), c, json.RawMessage(`{"path":"b.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != toolhub.DecisionConfirm {
		t.Fatalf("未批准应返回 Confirm: %+v", d)
	}
	// 已批准：工具管理器会传 Approved=true（见 exec.go），判定应放行。
	c.Approved = true
	d, _ = a.Authorize(context.Background(), c, json.RawMessage(`{"path":"a.txt"}`))
	if d.Action != toolhub.DecisionAllow {
		t.Fatalf("已批准应放行: %+v", d)
	}
	// 拒绝类不受批准影响。
	a2 := Authorizer(func(string) Policy { return Policy{Mode: ReadOnly} })
	d, _ = a2.Authorize(context.Background(), c, json.RawMessage(`{"path":"a.txt"}`))
	if d.Action != toolhub.DecisionDeny {
		t.Fatalf("拒绝类不受批准影响: %+v", d)
	}
}

// B10：不可信输入回合里，allow 降为询问（由内核按回合在 Ctx 上标记，权限层读它）。
func TestUntrustedTurnDowngradesAllow(t *testing.T) {
	sh := eff("shell", toolhub.Dangerous, toolhub.EffectExec, toolhub.AttrSandboxed)
	sh.Ctx = callctx.New(callctx.Options{SessionID: "s"})
	p := Policy{Mode: Workspace, Untrusted: func(c *toolhub.Cx) bool { return c.Ctx != nil && c.Ctx.Untrusted() }}
	args := json.RawMessage(`{"command":"go test ./..."}`)
	if got := p.Decide(sh, args).Action; got != Allow {
		t.Fatalf("可信回合按档位放行，得 %s", got)
	}
	sh.Ctx.SetUntrusted(true)
	if got := p.Decide(sh, args).Action; got != AskUser {
		t.Fatalf("不可信回合应降为询问，得 %s", got)
	}
}

// B9：受信项目的档位不能悄悄生效——同名覆盖用户档位报错；放宽档位当新会话缺省要用户确认过。
func TestProjectModesNeedApproval(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	t.Setenv("TENON_HOME", home)
	mk := func(dir, name, body string) {
		t.Helper()
		d := filepath.Join(dir, "modes")
		if dir == proj {
			d = filepath.Join(dir, ".tenon", "modes")
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name+".toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 1. 同名：项目的 strict 顶替用户的 strict → 拒绝。
	mk(home, "strict", "base = \"read-only\"\n")
	mk(proj, "strict", "base = \"full\"\n")
	if _, err := NewRegistry(ModeDirs(proj)...); err == nil || !strings.Contains(err.Error(), "同名") {
		t.Fatalf("项目档位与用户档位同名应报错，得 %v", err)
	}
	os.Remove(filepath.Join(proj, ".tenon", "modes", "strict.toml"))

	// 2. 项目放宽档位当缺省：没确认 → 退回内置缺省；确认后 → 生效。
	mk(proj, "yolo", "base = \"full\"\n")
	if err := os.WriteFile(filepath.Join(proj, ".tenon", "permissions.toml"), []byte("mode = \"yolo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(proj, proj, "", "", func() store.Store { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if m := s.Mode("s1"); m != Default {
		t.Fatalf("未确认的项目放宽档位不应成为缺省，得 %s", m)
	}
	s2, err := Open(proj, proj, "", "", func() store.Store { return nil })
	if err != nil {
		t.Fatal(err)
	}
	s2.ModeApproved = func(m Mode, w []string) bool { return m == "yolo" && len(w) > 0 }
	if m := s2.Mode("s1"); m != "yolo" {
		t.Fatalf("确认过的项目档位应成为缺省，得 %s", m)
	}
}

func TestRegistryCustomModes(t *testing.T) {
	user, proj := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(user, "dev", `name = "开发"
base = "workspace"
[items]
"t.web_fetch" = { default = "allow" }
"sandbox.level" = { level = "read-only" }
"t.cmd" = { deny = ["git push:*"] }
`)
	write(user, "dev2", `base = "dev"
[items]
"t.web_fetch" = { default = "deny" }
"t.cmd" = { allow = ["go test:*"] }
`)
	r, err := NewRegistry(user, proj)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := r.Get("dev2")
	if !ok || d.Root != Workspace || d.Title != "dev2" {
		t.Fatalf("继承: %+v", d)
	}
	if s, _ := d.ItemOf("t.web_fetch"); s.Default != toolhub.ActionDeny {
		t.Fatal("[items] 覆盖")
	}
	if s, _ := d.ItemOf("t.cmd"); len(s.Deny) != 1 || len(s.Allow) != 1 {
		t.Fatalf("模式清单应追加: %+v", s)
	}
	if d.SandboxLevel() != toolhub.SandboxReadOnly {
		t.Fatal("sandbox.level 继承")
	}
	// match 项判定：deny 模式继承，allow 模式生效，未命中按建议值
	it := toolhub.Item{ID: "t.cmd", Title: "命令", Type: toolhub.ItemMatch, Class: toolhub.EffectExec, Owner: "test",
		Subject: func(_ string, args json.RawMessage) ([]string, error) {
			var a struct{ Command string }
			err := json.Unmarshal(args, &a)
			return []string{a.Command}, err
		},
		Suggest: map[string]toolhub.ItemAction{"workspace": toolhub.ActionAsk}}
	sh := cx("shell", toolhub.Dangerous)
	sh.Tool.Items, sh.Items = []string{it.ID}, []toolhub.Item{it}
	p := Policy{Mode: "dev2", Def: d}
	for cmd, want := range map[string]Action{"git push": Deny, "go test ./...": Allow, "make": AskUser} {
		if got := p.Decide(sh, json.RawMessage(`{"command":"`+cmd+`"}`)).Action; got != want {
			t.Errorf("%s: 得到 %s，期望 %s", cmd, got, want)
		}
	}
	// 旧段 [effects] / [tools] / [sandbox] / [rules] 不再认
	bad := t.TempDir()
	write(bad, "old", `base = "ask"
[effects]
write = "allow"
`)
	if _, err := NewRegistry(bad); err == nil {
		t.Fatal("旧写法 [effects] 应报错")
	}
	// 项目级同名覆盖用户级；不能覆盖内置名
	write(proj, "dev", `base = "ask"
`)
	write(proj, "full", `base = "read-only"
`)
	if _, err := NewRegistry(user, proj); err == nil {
		t.Fatal("覆盖内置档位应报错")
	}
	os.Remove(filepath.Join(proj, "full.toml"))
	// 项目级与用户级同名：报错，不让项目悄悄顶替用户自己的档位（B9，2026-11-02 起）。
	if _, err := NewRegistry(user, proj); err == nil || !strings.Contains(err.Error(), "同名") {
		t.Fatalf("项目档位与用户档位同名应报错，得 %v", err)
	}
	os.Remove(filepath.Join(proj, "dev.toml"))
	write(proj, "projdev", `base = "ask"
`)
	r2, err := NewRegistry(user, proj)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := r2.Get("projdev"); d.Root != Ask || !d.Project {
		t.Fatal("项目级档位应可用并标为项目来源")
	}
	// 循环继承
	cyc := t.TempDir()
	write(cyc, "a", `base = "b"
`)
	write(cyc, "b", `base = "a"
`)
	if _, err := NewRegistry(cyc); err == nil {
		t.Fatal("循环继承应报错")
	}
	// 复制
	p2, err := r.Copy(ReadOnly, "mine", user)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := r.Get("mine"); !ok || d.Root != ReadOnly || filepath.Dir(p2) != user {
		t.Fatalf("复制: %+v %s", d, p2)
	}
	if _, err := r.Copy(ReadOnly, "full", user); err == nil {
		t.Fatal("复制到已有名字应报错")
	}
}

func TestSessionsNotice(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "dev.toml")
	os.WriteFile(f, []byte("base = \"ask\"\n"), 0o644)
	r, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Sessions{Reg: r}
	if s.Mode("x") != Workspace {
		t.Fatal("缺省档位应为 workspace")
	}
	if err := s.SetMode("x", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMode("x", "nope"); err == nil {
		t.Fatal("不存在的档位应报错")
	}
	if n := s.Notice("x"); n != "" {
		t.Fatalf("未改动不应提示: %s", n)
	}
	wr := eff("write_file", toolhub.Write, toolhub.EffectWrite)
	if s.Policy("x").Decide(wr, nil).Action != AskUser {
		t.Fatal("dev 基于 ask")
	}
	later := time.Now().Add(2 * time.Second)
	os.WriteFile(f, []byte("base = \"workspace\"\n"), 0o644)
	os.Chtimes(f, later, later)
	if n := s.Notice("x"); !strings.Contains(n, "已更新") {
		t.Fatalf("应提示更新: %q", n)
	}
	if s.Policy("x").Decide(wr, nil).Action != Allow {
		t.Fatal("新配置应生效")
	}
	if s.Mode("x/sub") != "dev" {
		t.Fatal("子会话跟随顶层")
	}
	os.Remove(f)
	if n := s.Notice("x"); !strings.Contains(n, "不存在") || s.Def("x").ID != Ask {
		t.Fatalf("档位删除后按 ask: %q", n)
	}
}

func TestShortLabel(t *testing.T) {
	r, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	// 内置档位：简称用显示名（中文短名）；完整标签是 "id 显示名"
	for id, want := range map[Mode]string{
		ReadOnly:  "仅可查看",
		Ask:       "每次询问",
		Workspace: "工作区允许",
		Full:      "完全权限",
	} {
		d, ok := r.Get(id)
		if !ok {
			t.Fatalf("缺内置档位 %s", id)
		}
		if got := Short(d); got != want {
			t.Errorf("Short(%s) = %q, 要 %q", id, got, want)
		}
		if got := Label(d, nil); got != string(id)+" "+want {
			t.Errorf("Label(%s) = %q, 要 %q", id, got, string(id)+" "+want)
		}
	}
	// 自定义档位：没写 name 时显示名就是 id，简称回 id
	user := t.TempDir()
	if err := os.WriteFile(filepath.Join(user, "dev.toml"), []byte("base = \"workspace\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r2, err := NewRegistry(user)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := r2.Get("dev")
	if got := Short(d); got != "dev" {
		t.Errorf("无 name 的自定义档位 Short = %q, 要 \"dev\"", got)
	}
	// 写了 name 时简称用 name
	if err := os.WriteFile(filepath.Join(user, "dev.toml"), []byte("name = \"开发\"\nbase = \"workspace\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(filepath.Join(user, "dev.toml"), later, later)
	r3, err := NewRegistry(user)
	if err != nil {
		t.Fatal(err)
	}
	d3, _ := r3.Get("dev")
	if got := Short(d3); got != "开发" {
		t.Errorf("带 name 的自定义档位 Short = %q, 要 \"开发\"", got)
	}
	if Short(nil) != "" {
		t.Error("nil 定义简称应为空")
	}
}
