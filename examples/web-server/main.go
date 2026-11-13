// web-server：只用 SDK 起一个带框架工作台的网页服务（计划/07 阶段二的验收示例）。
//
//	go run ./examples/web-server -dir <项目目录>
//
// 模型端点：缺省用内置目录里的 deepseek-chat（需要 DEEPSEEK_API_KEY）；给了 -base（或 TENON_BASE_URL）时
// 走 OpenAI 兼容端点，密钥读 TENON_API_KEY。会话存在 -store 指定的库里（缺省系统临时目录）。
//
// 整个应用就是：tenon.New 装配 → Start → App.API() 交给 transport/web，静态文件用框架的前端（tenon/web）。
// 产品级的东西（profile 分层、项目信任、工作区切换）在 apphost 里，这里都没有用。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/modules/storage/sqlite"
	web "github.com/Sevyn17/tenon/transport/web"
)

func main() {
	dir := flag.String("dir", ".", "工作目录（智能体读写文件、跑命令的地方）")
	addr := flag.String("addr", "127.0.0.1:0", "监听地址（只允许回环地址）")
	storePath := flag.String("store", filepath.Join(os.TempDir(), "tenon-example", "state.db"), "会话库文件")
	base := flag.String("base", os.Getenv("TENON_BASE_URL"), "OpenAI 兼容端点（空 = 用内置模型目录）")
	model := flag.String("model", os.Getenv("TENON_MODEL"), "模型名或别名（空 = 组件缺省）")
	trust := flag.Bool("trust", false, "信任工作目录（读其中的 .tenon 配置、钩子、MCP）")
	flag.Parse()
	if err := run(*dir, *addr, *storePath, *base, *model, *trust); err != nil {
		fmt.Fprintln(os.Stderr, "web-server:", err)
		os.Exit(1)
	}
}

func run(dir, addr, storePath, base, model string, trust bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	db, err := sqlite.Open(ctx, storePath, sqlite.Options{})
	if err != nil {
		return err
	}
	opts := []tenon.Option{tenon.Default(), tenon.WithStore(db), tenon.WithWorkdir(dir),
		tenon.WithProjectTrust(trust), tenon.WithProtocolUI()}
	cfg := map[string]any{}
	if base != "" {
		cfg["base_url"] = base
	}
	if model != "" {
		cfg["model"] = model
	}
	if len(cfg) > 0 {
		opts = append(opts, tenon.WithConfig("models", cfg))
	}
	app, err := tenon.New(opts...)
	if err != nil {
		db.Close()
		return err
	}
	defer app.Close(context.Background()) // 也关掉存储
	if err := app.Start(ctx); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if ip := ln.Addr().(*net.TCPAddr).IP; !ip.IsLoopback() {
		ln.Close()
		return fmt.Errorf("示例只监听回环地址，%s 不是", addr)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	token, err := web.NewToken()
	if err != nil {
		return err
	}
	// Static 不传：框架不再内嵌前端产物（前端属应用层，见 web/README）。
	// 应用要提供界面，把自己的 dist 用 os.DirFS 或 embed.FS 传进来。
	srv := web.New(web.Config{API: app.API(), Token: token, Hosts: web.LoopbackHosts(port)})
	hs := &http.Server{Handler: srv, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { <-ctx.Done(); _ = hs.Close() }()
	fmt.Printf("打开：http://127.0.0.1:%d/?token=%s\n  会话库：%s\n  按 Ctrl+C 结束\n", port, token, storePath)
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
