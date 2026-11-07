// tenon-desktop：Tenon 桌面端（Wails v3）。界面为 kernel/web 的共享前端（嵌入），
// 前端经唯一绑定服务 Service.Call 调应用接口层，事件经 Wails 事件推送；不开任何网络端口。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"

	"tenon/appapi"
	"tenon/apps/internal/boot"
	"tenon/runtime/assemble"
	"tenon/runtime/bus"
	"tenon/web"
)

func main() {
	f := boot.Flags{MaxSteps: -1}
	flag.StringVar(&f.Base, "base", os.Getenv("TENON_BASE_URL"), "模型服务地址（覆盖 profile 的 models.base_url）")
	flag.StringVar(&f.Model, "model", os.Getenv("TENON_MODEL"), "模型名（覆盖 profile 的 models.model）")
	flag.StringVar(&f.Workdir, "dir", ".", "工作目录（项目根）")
	flag.StringVar(&f.Profile, "profile", "", "额外的 profile.toml（最后叠加）")
	flag.StringVar(&f.Mode, "mode", "", "强制权限档位（覆盖 permission.force_mode）")
	flag.StringVar(&f.Sandbox, "sandbox", "", "沙箱模式（覆盖 permission.sandbox）")
	flag.StringVar(&f.MCP, "mcp", "", "MCP 配置文件（覆盖 mcp.config）")
	flag.IntVar(&f.MaxSteps, "max-steps", -1, "每回合最大步数（-1 = 用 profile 缺省，0 = 不限）")
	flag.BoolVar(&f.TrustProject, "trust-project", false, "本次运行信任项目（读 <dir>/.tenon 下的配置），不保存")
	flag.BoolVar(&f.ProjectHooks, "project-hooks", false, "本次运行启用项目钩子（隐含信任项目）")
	flag.Parse()
	if err := run(f); err != nil {
		fmt.Fprintln(os.Stderr, "tenon-desktop:", err)
		os.Exit(1)
	}
}

func run(f boot.Flags) error {
	var ui *appapi.UI
	// 桌面端没有终端可问：无信任记录的项目按不受信（需要时用 -trust-project 或 tenon trust 保存）。
	rt, err := boot.Start(boot.StartOptions{Flags: f, UI: func(b *bus.Bus) assemble.UI {
		ui = appapi.NewUI(b) // ask_user 等经 appapi 的 ask 事件 + session.answer 往返
		return ui
	}})
	if err != nil {
		return err
	}
	defer rt.Close()
	built, err := rt.BuildApp(boot.AppOptions{MaxSteps: f.MaxSteps, UI: ui, ModesWorkdir: f.Workdir, Upload: true})
	if err != nil {
		return err
	}

	var app *application.App
	svc := NewService(built.API, func(name string, data any) {
		if app != nil {
			app.Event.Emit(name, data)
		}
	})
	app = application.New(application.Options{
		Name:        "Tenon",
		Description: "Tenon 桌面端",
		Services:    []application.Service{application.NewService(svc)},
		Assets: application.AssetOptions{Handler: application.AssetFileServerFS(web.Dist()), DisableLogging: true,
			Middleware: uploadMiddleware(built.API)},
		LogLevel:   slog.LevelWarn,
		Windows:    application.WindowsOptions{},
		OnShutdown: svc.StopEvents,
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "Tenon — " + rt.Root, Width: 1280, Height: 820, MinWidth: 800, MinHeight: 500, URL: "/",
	})
	svc.StartEvents()
	for _, e := range built.Ready(context.Background()) {
		built.API.Notice("", "ready: "+e.Error())
	}
	for _, n := range rt.Notes {
		built.API.Notice("", n)
	}
	return app.Run()
}
