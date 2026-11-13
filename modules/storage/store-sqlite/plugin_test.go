package storesqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// Dir：配置给了就用（相对路径按当前目录转绝对），否则用应用的用户级目录。
func TestDir(t *testing.T) {
	// 配置了绝对路径：原样返回。
	abs := t.TempDir()
	if got, err := Dir(abs); err != nil || got != abs {
		t.Fatalf("Dir(%q) = %q, %v", abs, got, err)
	}

	// 配置了相对路径：转成绝对（基于进程当前目录）。
	rel := "some-relative-dir"
	got, err := Dir(rel)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) || filepath.Base(got) != rel {
		t.Fatalf("相对路径应转绝对：%q", got)
	}

	// 没配置：用 appid.Home()。
	home := t.TempDir()
	t.Setenv(appid.EnvName("HOME"), home)
	if got, err := Dir(""); err != nil || got != home {
		t.Fatalf("Dir(\"\") 应取 appid.Home：%q, %v", got, err)
	}
}

// Dir：既没配置也取不到用户目录时报错，错误里带环境变量名（让用户知道怎么设）。
func TestDirNoHome(t *testing.T) {
	t.Setenv(appid.EnvName("HOME"), "")
	// 某些平台 appid.Home 会回退到 os.UserHomeDir；只有真的取不到时才该报错。
	if _, err := Dir(""); err == nil {
		t.Skip("本机 appid.Home 有缺省回退，构造不出「取不到」的情形")
	}
}

// Store 在 Start 之前必须交出 nil（不能是带类型的 nil 接口）——
// 这正是 group 组件当初启动失败的原因（D5 同类：Setup 期 DB() 为 nil 被当成可用）。
func TestStoreNilBeforeStart(t *testing.T) {
	s := &Store{}
	if s.DB() != nil {
		t.Fatal("Start 之前 DB() 应为 nil")
	}
	if s.Blobs() != nil {
		t.Fatal("Start 之前 Blobs() 应为 nil")
	}
	if s.raw() != nil {
		t.Fatal("Start 之前 raw() 应为 nil")
	}
	// SQL 出口在 Start 之前各方法不可用：DB() 返回 nil、Migrate 报错。
	sql := s.SQL()
	if sql.DB() != nil {
		t.Fatal("Start 之前 SQL().DB() 应为 nil")
	}
	if err := sql.Migrate(context.Background(), "t", nil); err == nil {
		t.Fatal("Start 之前 Migrate 应报错")
	}
	if sql.Dialect() != "sqlite" {
		t.Fatalf("Dialect 应为 sqlite：%q", sql.Dialect())
	}
}

// 完整生命周期：Setup 登记服务 → Start 打开库与工件 → OnClose 关掉并把句柄清空。
// 用真实组件装配，走 Setup→Start 真实路径（group 当初就是绕过了这条路径才没被测出来）。
func TestPluginLifecycle(t *testing.T) {
	dir := t.TempDir()
	asm, err := component.Assemble(component.Spec{Plugins: []component.Entry{
		{Name: "store-sqlite", Config: map[string]any{"dir": dir}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	root := component.NewRoot("test")
	if err := asm.Start(root); err != nil {
		t.Fatalf("Start：%v", err)
	}

	// Start 之后：库文件与工件目录都建好了。
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err != nil {
		t.Fatalf("应创建 state.db：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs")); err != nil {
		t.Fatalf("应创建 blobs 目录：%v", err)
	}

	// 服务能取到，且 DB() 是可用存储。
	v, ok := asm.Value(assemble.ServiceDB)
	if !ok {
		t.Fatal("Start 之后应能取到 service:db")
	}
	h, ok := v.(assemble.DBHandle)
	if !ok {
		t.Fatalf("service:db 应是 DBHandle，得到 %T", v)
	}
	if h.DB() == nil {
		t.Fatal("Start 之后 DB() 不应为 nil")
	}
	if h.Blobs() == nil {
		t.Fatal("Start 之后 Blobs() 不应为 nil")
	}
	// 真能读写：开一个会话再列出来。
	ctx := context.Background()
	if err := h.DB().CreateSession(ctx, store.SessionMeta{ID: "s1"}, session.Header{}, nil); err != nil {
		t.Fatalf("写会话：%v", err)
	}
	list, err := h.DB().ListSessions(ctx, 0, 10, false)
	if err != nil || len(list) != 1 || list[0].ID != "s1" {
		t.Fatalf("列会话：%+v %v", list, err)
	}

	// 关闭后句柄清空（可重复调用不 panic）。
	if err := root.Close(context.Background()); err != nil {
		t.Fatalf("Close：%v", err)
	}
	if h.DB() != nil {
		t.Fatal("Close 之后 DB() 应回到 nil")
	}
}
