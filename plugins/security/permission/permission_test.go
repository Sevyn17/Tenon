package permission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
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
	c.Tool.WithEffect(e)
	// 测试工具声明一个与效果同类别的 switch 项（无建议值 → 按封顶表取值）
	it := toolhub.Item{ID: "t." + name, Title: name, Type: toolhub.ItemSwitch, Class: e, Owner: "test"}
	c.Tool.Items = []string{it.ID}
	c.Items = []toolhub.Item{it}
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

func TestLayerConfirmAndApprovedHashLock(t *testing.T) {
	ran := 0
	next := func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
		ran++
		return toolhub.Text("ok"), nil
	}
	approvedHash := toolhub.ArgsHash(json.RawMessage(`{"path":"a.txt"}`))
	l := Layer(func(string) Policy { return Policy{Mode: Ask} },
		func(_, _, h string) bool { return h == approvedHash })
	c := eff("write_file", toolhub.Write, toolhub.EffectWrite)

	out, _ := l.Call(context.Background(), c, json.RawMessage(`{"path":"b.txt"}`), next)
	if out.Kind != toolhub.Confirm || ran != 0 {
		t.Fatalf("未批准应返回 Confirm 且不执行: %+v ran=%d", out, ran)
	}
	out, _ = l.Call(context.Background(), c, json.RawMessage(`{"path":"a.txt"}`), next)
	if out.Kind != toolhub.Done || ran != 1 {
		t.Fatalf("批准且参数一致应执行: %+v ran=%d", out, ran)
	}
	l2 := Layer(func(string) Policy { return Policy{Mode: ReadOnly} }, func(_, _, _ string) bool { return true })
	out, _ = l2.Call(context.Background(), c, json.RawMessage(`{"path":"a.txt"}`), next)
	if out.Status != session.ToolError || ran != 1 {
		t.Fatalf("拒绝类不受批准影响: %+v", out)
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
	r2, err := NewRegistry(user, proj)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := r2.Get("dev"); d.Root != Ask || !d.Project {
		t.Fatal("项目级应覆盖用户级")
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
