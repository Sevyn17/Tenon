// ybase-server：YBase 网页服务。把 kernel/web 的共享前端（嵌入）和应用接口层（appapi）
// 经 HTTP + WebSocket 提供给浏览器、IDE 等本机客户端。缺省只监听 127.0.0.1，启动时生成随机令牌。
// 做成应用而非插件：appapi.App 在应用层装配（boot.BuildApp），插件拿不到 App；见 docs/90 - 归档/10 - 框架模块/14 - 网页服务.md。
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ybase/appapi"
	"ybase/apps/internal/boot"
	"ybase/runtime/assemble"
	"ybase/runtime/bus"
	"ybase/web"
)

type options struct {
	boot        boot.Flags
	addr        string
	allowRemote bool
	cert, key   string
	hosts       string
}

func main() {
	o := options{boot: boot.Flags{MaxSteps: -1}}
	f := &o.boot
	flag.StringVar(&f.Base, "base", os.Getenv("YBASE_BASE_URL"), "模型服务地址（覆盖 profile 的 models.base_url）")
	flag.StringVar(&f.Model, "model", os.Getenv("YBASE_MODEL"), "模型名（覆盖 profile 的 models.model）")
	flag.StringVar(&f.Workdir, "dir", ".", "工作目录（项目根）")
	flag.StringVar(&f.Profile, "profile", "", "额外的 profile.toml（最后叠加）")
	flag.StringVar(&f.Mode, "mode", "", "强制权限档位（覆盖 permission.force_mode）")
	flag.StringVar(&f.Sandbox, "sandbox", "", "沙箱模式（覆盖 permission.sandbox）")
	flag.StringVar(&f.MCP, "mcp", "", "MCP 配置文件（覆盖 mcp.config）")
	flag.IntVar(&f.MaxSteps, "max-steps", -1, "每回合最大步数（-1 = 用 profile 缺省，0 = 不限）")
	flag.BoolVar(&f.TrustProject, "trust-project", false, "本次运行信任项目（读 <dir>/.ybase 下的配置），不保存")
	flag.BoolVar(&f.ProjectHooks, "project-hooks", false, "本次运行启用项目钩子（隐含信任项目）")
	flag.StringVar(&o.addr, "addr", "127.0.0.1:0", "监听地址（缺省 127.0.0.1 随机端口）；非回环地址需 -allow-remote 且给 TLS 证书")
	flag.BoolVar(&o.allowRemote, "allow-remote", false, "允许监听非回环地址（必须同时给 -tls-cert / -tls-key）")
	flag.StringVar(&o.cert, "tls-cert", "", "TLS 证书文件（PEM）")
	flag.StringVar(&o.key, "tls-key", "", "TLS 私钥文件（PEM）")
	flag.StringVar(&o.hosts, "host", "", "远程访问时额外允许的 Host 名（逗号分隔，不含端口），如 my.lan")
	flag.Parse()
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "ybase-server:", err)
		os.Exit(1)
	}
}

// checkAddr 校验监听地址：回环地址直接通过；其余要 -allow-remote 且有 TLS。返回是否回环。
func checkAddr(o options) (loopback bool, err error) {
	host, _, err := net.SplitHostPort(o.addr)
	if err != nil {
		return false, fmt.Errorf("-addr %q 无效：%w", o.addr, err)
	}
	if host == "localhost" {
		return true, nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true, nil
	}
	if !o.allowRemote {
		return false, fmt.Errorf("-addr %q 不是回环地址：如确需远程访问，加 -allow-remote 并给 -tls-cert / -tls-key", o.addr)
	}
	if o.cert == "" || o.key == "" {
		return false, errors.New("-allow-remote 必须同时给 -tls-cert 和 -tls-key（令牌不能明文过网）")
	}
	return false, nil
}

// allowedHosts 允许的 Host 头：回环名 + 实际端口；远程时再加监听 IP 与 -host 名。
func allowedHosts(o options, loopback bool, port int) []string {
	hs := LoopbackHosts(port)
	if loopback {
		return hs
	}
	p := strconv.Itoa(port)
	host, _, _ := net.SplitHostPort(o.addr)
	if ip := net.ParseIP(host); host != "" && (ip == nil || !ip.IsUnspecified()) {
		hs = append(hs, net.JoinHostPort(host, p))
	}
	for _, h := range strings.Split(o.hosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hs = append(hs, net.JoinHostPort(h, p))
		}
	}
	return hs
}

func run(o options) error {
	loopback, err := checkAddr(o)
	if err != nil {
		return err
	}
	var cert tls.Certificate
	useTLS := o.cert != "" || o.key != ""
	if useTLS {
		if cert, err = tls.LoadX509KeyPair(o.cert, o.key); err != nil {
			return fmt.Errorf("读取 TLS 证书：%w", err)
		}
	}
	token, err := NewToken()
	if err != nil {
		return fmt.Errorf("生成令牌：%w", err)
	}

	var ui *appapi.UI
	// 网页端没有终端可问：无信任记录的项目按不受信（同桌面端）。
	rt, err := boot.Start(boot.StartOptions{Flags: o.boot, UI: func(b *bus.Bus) assemble.UI {
		ui = appapi.NewUI(b)
		return ui
	}})
	if err != nil {
		return err
	}
	defer rt.Close()
	built, err := rt.BuildApp(boot.AppOptions{MaxSteps: o.boot.MaxSteps, UI: ui, ModesWorkdir: o.boot.Workdir, Upload: true})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", o.addr)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if useTLS {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	srv := New(Config{API: built.API, Token: token, Hosts: allowedHosts(o, loopback, port), TLS: useTLS, Static: web.Dist()})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	hs := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return base }, // 关闭时取消，WebSocket 连接随之结束
	}

	for _, e := range built.Ready(context.Background()) {
		built.API.Notice("", "ready: "+e.Error())
	}
	for _, n := range rt.Notes {
		built.API.Notice("", n)
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	// 令牌只打印到本终端（stdout），不写任何文件或日志。
	fmt.Printf("YBase 网页服务：%s\n  打开：%s://127.0.0.1:%d/?token=%s\n  按 Ctrl+C 结束\n", rt.Root, scheme, port, token)

	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	cancelBase()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}
