package agentdef

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const basicToml = `format      = "ybase/v1"
id          = "support"
description = "回答订单和售后问题"
model       = "fast"
prompt      = "@AGENT.md"
tools       = ["order_query", "ask"]
resident    = false

[use.context]
files  = ["SOUL.md", "AGENT.md"]

[use.budget]
tokens_per_day = 200000

[x.shop]
display_name = "小布"
`

func basicFiles() Files {
	return Files{
		ManifestName:   []byte(basicToml),
		"AGENT.md":     []byte("工作规则"),
		"SOUL.md":      []byte("性格"),
		"faq/order.md": []byte("订单问答"),
	}
}

// pkg 生成只有 agent.toml 的最小定义包。
func pkg(id, desc string) Files {
	return Files{ManifestName: []byte("id = \"" + id + "\"\ndescription = \"" + desc + "\"\n")}
}

func TestParseOK(t *testing.T) {
	a, err := Parse("", basicFiles())
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "support" || a.Description != "回答订单和售后问题" || a.Model != "fast" || a.Prompt != "@AGENT.md" {
		t.Fatalf("内核字段不对：%+v", a)
	}
	if strings.Join(a.Tools, ",") != "order_query,ask" || a.Resident {
		t.Fatalf("tools / resident 不对：%+v", a)
	}
	if a.Format != FormatV1 {
		t.Fatalf("format = %q", a.Format)
	}
	if got := a.Use["budget"]["tokens_per_day"]; got != int64(200000) {
		t.Fatalf("use.budget.tokens_per_day = %#v", got)
	}
	if _, ok := a.Use["context"]["files"]; !ok {
		t.Fatal("use.context.files 丢失")
	}
	if a.X["shop"]["display_name"] != "小布" {
		t.Fatalf("x.shop = %#v", a.X["shop"])
	}
	if _, ok := a.Files[ManifestName]; ok {
		t.Fatal("Files 不应含 agent.toml")
	}
	if len(a.Files) != 3 || string(a.Files["faq/order.md"]) != "订单问答" {
		t.Fatalf("Files = %v", a.Files.Names())
	}
	if a.PromptText() != "工作规则" {
		t.Fatalf("PromptText = %q", a.PromptText())
	}
	if len(a.Version) != 64 {
		t.Fatalf("Version = %q", a.Version)
	}
}

func TestParseDefaultsFormatAndInlinePrompt(t *testing.T) {
	a, err := Parse("", Files{ManifestName: []byte("id = \"a\"\nprompt = \"你好\"\n")})
	if err != nil {
		t.Fatal(err)
	}
	if a.Format != FormatV1 || a.PromptText() != "你好" {
		t.Fatalf("%+v", a)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct {
		files Files
		want  string
	}{
		"未知顶层字段":          {Files{ManifestName: []byte("id = \"a\"\nname = \"a\"\n")}, "未知字段 name"},
		"旧字段 permissions": {Files{ManifestName: []byte("id = \"a\"\npermissions = [\"x\"]\n")}, "未知字段 permissions"},
		"未知表":             {Files{ManifestName: []byte("id = \"a\"\n[memory]\nstore = \"m\"\n")}, "未知字段 memory"},
		"缺 id":            {Files{ManifestName: []byte("model = \"fast\"\n")}, "缺少 id"},
		"id 含斜杠":          {Files{ManifestName: []byte("id = \"a/b\"\n")}, "不合法"},
		"缺清单":             {Files{"AGENT.md": []byte("x")}, "缺少 agent.toml"},
		"未知 format":       {Files{ManifestName: []byte("format = \"ybase/v9\"\nid = \"a\"\n")}, "不支持"},
		"prompt 文件缺失":     {Files{ManifestName: []byte("id = \"a\"\nprompt = \"@AGENT.md\"\n")}, "不在定义包里"},
		"路径逃逸":            {Files{ManifestName: []byte("id = \"a\"\n"), "../x": []byte("x")}, "相对路径"},
		"空工具名":            {Files{ManifestName: []byte("id = \"a\"\ntools = [\"\"]\n")}, "tools[0]"},
		"use 不是表":         {Files{ManifestName: []byte("id = \"a\"\nuse = 1\n")}, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("", c.files)
			if err == nil {
				t.Fatal("应报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误 %q 不含 %q", err, c.want)
			}
		})
	}
}

func TestResidentUnsupported(t *testing.T) {
	_, err := Parse("", Files{ManifestName: []byte("id = \"a\"\nresident = true\n")})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "未支持") {
		t.Fatalf("err = %v", err)
	}
}

func TestHashStableAndSensitive(t *testing.T) {
	base := Hash(basicFiles())
	if Hash(basicFiles()) != base {
		t.Fatal("同一份内容哈希不稳定")
	}
	a, _ := Parse("", basicFiles())
	if a.Version != base {
		t.Fatal("Version 应等于 Hash(整包)")
	}
	mut := map[string]func(Files){
		"改 agent.toml": func(f Files) { f[ManifestName] = append(f[ManifestName], '\n') },
		"改 SOUL.md":    func(f Files) { f["SOUL.md"] = []byte("性格2") },
		"加文件":          func(f Files) { f["STYLE.md"] = nil },
		"删文件":          func(f Files) { delete(f, "faq/order.md") },
		"改名":           func(f Files) { f["faq/order2.md"] = f["faq/order.md"]; delete(f, "faq/order.md") },
		"内容挪到别的文件":     func(f Files) { f["AGENT.md"] = []byte("工作规则性格"); f["SOUL.md"] = nil },
	}
	for name, fn := range mut {
		f := basicFiles()
		fn(f)
		if Hash(f) == base {
			t.Errorf("%s：哈希未变", name)
		}
	}
}

func TestTenantReplacesPlatformWholesale(t *testing.T) {
	ctx := context.Background()
	m := Mem()
	must(t, m.Put(Ref{Namespace: Platform, Name: "support"}, basicFiles()))
	// 租户定义只写 id 与 description：不能从平台继承 model / tools / use / 文件。
	must(t, m.Put(Ref{Namespace: Tenant, Tenant: "t1", Name: "support"}, pkg("support", "租户版")))

	a, ref, err := Resolve(ctx, m, Scope{Tenant: "t1"}, "support")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Namespace != Tenant || a.Description != "租户版" {
		t.Fatalf("应解析到租户定义：%s %+v", ref, a)
	}
	if a.Model != "" || a.Tools != nil || a.Use != nil || len(a.Files) != 0 || a.Prompt != "" {
		t.Fatalf("不应合并平台字段：%+v", a)
	}

	// 别的租户仍看到平台定义。
	a, ref, err = Resolve(ctx, m, Scope{Tenant: "t2"}, "support")
	if err != nil || ref.Namespace != Platform || a.Model != "fast" {
		t.Fatalf("t2 应解析到平台定义：%s %+v %v", ref, a, err)
	}

	vis, err := Visible(ctx, m, Scope{Tenant: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vis) != 1 || vis[0].Namespace != Tenant {
		t.Fatalf("Visible 应只剩租户定义：%v", vis)
	}
}

func TestUserNamespaceIsolation(t *testing.T) {
	ctx := context.Background()
	m := Mem()
	must(t, m.Put(Ref{Namespace: Platform, Name: "helper"}, pkg("helper", "平台版")))
	must(t, m.Put(Ref{Namespace: User, Tenant: "t1", User: "alice", Name: "helper"}, pkg("helper", "alice 版")))
	must(t, m.Put(Ref{Namespace: User, Tenant: "t1", User: "alice", Name: "mine"}, pkg("mine", "alice 私有")))

	// 本人写全名能解析到。
	a, ref, err := Resolve(ctx, m, Scope{Tenant: "t1", User: "alice"}, "user/alice/mine")
	if err != nil || ref.Namespace != User || a.Description != "alice 私有" {
		t.Fatalf("本人全名解析失败：%s %+v %v", ref, a, err)
	}
	// 别人、匿名、其他租户都解析不到。
	for _, sc := range []Scope{{Tenant: "t1", User: "bob"}, {Tenant: "t1"}, {Tenant: "t2", User: "alice"}} {
		if _, _, err := Resolve(ctx, m, sc, "user/alice/mine"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%+v 不应解析到 alice 的定义：%v", sc, err)
		}
	}
	// List / Visible 只给本人列出用户层。
	vis, err := Visible(ctx, m, Scope{Tenant: "t1", User: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vis) != 1 || vis[0].Namespace != Platform {
		t.Fatalf("bob 只应看到平台定义：%v", vis)
	}
	vis, err = Visible(ctx, m, Scope{Tenant: "t1", User: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range vis {
		names = append(names, r.String())
	}
	if strings.Join(names, ",") != "platform:helper,tenant:t1/user/alice/helper,tenant:t1/user/alice/mine" {
		t.Fatalf("alice Visible = %v", names)
	}
}

func TestBareNameSkipsUserLayer(t *testing.T) {
	ctx := context.Background()
	m := Mem()
	must(t, m.Put(Ref{Namespace: Platform, Name: "helper"}, pkg("helper", "平台版")))
	must(t, m.Put(Ref{Namespace: User, Tenant: "t1", User: "alice", Name: "helper"}, pkg("helper", "alice 版")))
	must(t, m.Put(Ref{Namespace: User, Tenant: "t1", User: "alice", Name: "mine"}, pkg("mine", "alice 私有")))
	sc := Scope{Tenant: "t1", User: "alice"}

	a, ref, err := Resolve(ctx, m, sc, "helper")
	if err != nil || ref.Namespace != Platform || a.Description != "平台版" {
		t.Fatalf("裸名应解析到平台定义，用户层不能遮住：%s %+v %v", ref, a, err)
	}
	if _, _, err := Resolve(ctx, m, sc, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("只有用户层的名字，裸名不应解析到：%v", err)
	}
}

func TestParseName(t *testing.T) {
	r, bare, err := ParseName("user/alice/mine")
	if err != nil || bare || r.Namespace != User || r.User != "alice" || r.Name != "mine" {
		t.Fatalf("%+v %v %v", r, bare, err)
	}
	for _, bad := range []string{"", "user/alice", "user//x", "user/a/b/c", "a/b", "../x"} {
		if _, _, err := ParseName(bad); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
	if (Ref{Namespace: User, Tenant: "t", User: "u", Name: "n"}).FullName() != "user/u/n" {
		t.Fatal("FullName")
	}
}

func TestMemPutValidates(t *testing.T) {
	m := Mem()
	if err := m.Put(Ref{Namespace: Platform, Name: "a"}, pkg("b", "")); err == nil {
		t.Fatal("id 与名字不符应报错")
	}
	if err := m.Put(Ref{Namespace: Platform, Tenant: "t", Name: "a"}, pkg("a", "")); err == nil {
		t.Fatal("平台层带租户应报错")
	}
	if err := m.Put(Ref{Namespace: Platform, Name: "a"}, Files{ManifestName: []byte("id = \"a\"\nresident = true\n")}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("resident 应报未支持：%v", err)
	}
	// Put 后改调用方的切片不影响已存内容。
	f := pkg("a", "原")
	must(t, m.Put(Ref{Namespace: Platform, Name: "a"}, f))
	copy(f[ManifestName], "XX")
	if _, err := m.Load(context.Background(), Ref{Namespace: Platform, Name: "a"}); err != nil {
		t.Fatalf("已存内容被外部修改：%v", err)
	}
}

func writePkg(t *testing.T, dir string, files Files) {
	t.Helper()
	for n, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, b, 0o644))
	}
}

func TestDirSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writePkg(t, filepath.Join(root, "platform", "support"), basicFiles())
	writePkg(t, filepath.Join(root, "platform", "helper"), pkg("helper", "平台版"))
	writePkg(t, filepath.Join(root, "tenant", "t1", "support"), pkg("support", "租户版"))
	writePkg(t, filepath.Join(root, "user", "t1", "alice", "mine"), pkg("mine", "alice 私有"))
	writePkg(t, filepath.Join(root, "user", "t1", "alice", "helper"), pkg("helper", "alice 版"))
	// 噪声：没有清单的目录、隐藏文件。
	must(t, os.MkdirAll(filepath.Join(root, "platform", "empty"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "platform", "support", ".DS_Store"), []byte("x"), 0o644))

	d := Dir(root)

	a, ref, err := Resolve(ctx, d, Scope{Tenant: "t1", User: "alice"}, "support")
	if err != nil || ref.Namespace != Tenant || a.Description != "租户版" || a.Model != "" {
		t.Fatalf("租户替换：%s %+v %v", ref, a, err)
	}
	a, ref, err = Resolve(ctx, d, Scope{Tenant: "t1", User: "alice"}, "helper")
	if err != nil || ref.Namespace != Platform {
		t.Fatalf("裸名不查用户层：%s %+v %v", ref, a, err)
	}
	if _, _, err := Resolve(ctx, d, Scope{Tenant: "t1", User: "bob"}, "user/alice/mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob 不应解析到 alice 的定义：%v", err)
	}
	a, _, err = Resolve(ctx, d, Scope{Tenant: "t1", User: "alice"}, "user/alice/mine")
	if err != nil || a.Description != "alice 私有" {
		t.Fatalf("alice 全名解析：%+v %v", a, err)
	}

	p, _, err := Resolve(ctx, d, Scope{Tenant: "t2"}, "support")
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != Hash(basicFiles()) {
		t.Fatal("目录来源的哈希应与同内容内存包一致（隐藏文件不计入）")
	}
	if p.PromptText() != "工作规则" || string(p.Files["faq/order.md"]) != "订单问答" {
		t.Fatalf("文件读取不对：%v", p.Files.Names())
	}

	// 改包里任一文件 → 新 Version。
	must(t, os.WriteFile(filepath.Join(root, "platform", "support", "faq", "order.md"), []byte("改了"), 0o644))
	p2, _, err := Resolve(ctx, d, Scope{Tenant: "t2"}, "support")
	if err != nil || p2.Version == p.Version {
		t.Fatalf("改文件后 Version 应变化：%v", err)
	}

	vis, err := Visible(ctx, d, Scope{Tenant: "t1", User: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range vis {
		names = append(names, r.String())
	}
	if strings.Join(names, ",") != "platform:helper,tenant:t1/support,tenant:t1/user/alice/helper,tenant:t1/user/alice/mine" {
		t.Fatalf("Visible = %v", names)
	}

	// id 与目录名不符报错。
	writePkg(t, filepath.Join(root, "platform", "wrong"), pkg("other", ""))
	if _, err := d.Load(ctx, Ref{Namespace: Platform, Name: "wrong"}); err == nil || !strings.Contains(err.Error(), "须与名字") {
		t.Fatalf("id 不符应报错：%v", err)
	}
	if _, err := d.Load(ctx, Ref{Namespace: Platform, Name: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound：%v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestHashNormalizesCRLF(t *testing.T) {
	a := Files{"agent.toml": []byte("id = \"a\"\r\nprompt = \"x\"\r\n")}
	b := Files{"agent.toml": []byte("id = \"a\"\nprompt = \"x\"\n")}
	if Hash(a) != Hash(b) {
		t.Fatal("CRLF 与 LF 应得到同一个哈希")
	}
	bin1 := Files{"x.bin": []byte("a\x00\r\n")}
	bin2 := Files{"x.bin": []byte("a\x00\n")}
	if Hash(bin1) == Hash(bin2) {
		t.Fatal("含 NUL 的文件应按原始字节计算")
	}
}

// Encode / Decode 是可逆的规范形式：入库内容就是它，所以存储算的 sha256 与 Hash 一致。
func TestEncodeDecodeRoundTrip(t *testing.T) {
	f := basicFiles()
	f["bin/logo.png"] = append([]byte{0x89, 0x50, 0x4e, 0x47, 0x00}, 13, 10)
	b := Encode(f)
	back, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode：%v", err)
	}
	for name, want := range f {
		got, ok := back[name]
		if !ok {
			t.Fatalf("丢了 %s", name)
		}
		// 文本文件按 LF 规范化，二进制按原字节。
		if bytes.IndexByte(want, 0) >= 0 {
			if string(got) != string(want) {
				t.Fatalf("%s 二进制内容变了", name)
			}
			continue
		}
		lf := bytes.ReplaceAll(want, []byte{13, 10}, []byte{10})
		if string(got) != string(lf) {
			t.Fatalf("%s = %q", name, got)
		}
	}
	if Hash(back) != Hash(f) {
		t.Fatal("往返之后哈希应不变（规范化后的内容才是规范形式）")
	}
	// 同一份内容编两次得到同一串字节（没有时间戳等易变信息）。
	if string(Encode(basicFiles())) != string(Encode(basicFiles())) {
		t.Fatal("Encode 不稳定")
	}
	sum := sha256.Sum256(Encode(f))
	if hex.EncodeToString(sum[:]) != Hash(f) {
		t.Fatal("Hash 应等于 sha256(Encode)")
	}
}

func TestDecodeRejectsBroken(t *testing.T) {
	good := Encode(basicFiles())
	for name, b := range map[string][]byte{
		"路径长度被截断": good[:4],
		"路径被截断":   good[:12],
		"缺清单":     Encode(Files{"x.md": []byte("x")}),
		"空":       {},
	} {
		if _, err := Decode(b); err == nil {
			t.Fatalf("%s：应当报错", name)
		}
	}
}
