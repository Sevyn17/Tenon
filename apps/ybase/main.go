// ybase：终端里的最小智能体（M4.8：建在会话宿主 host 之上）。
//
//	YBASE_API_KEY=... ybase -base https://api.deepseek.com/v1 -model deepseek-chat
//	ybase explain     显示插件组合、插件包、插槽来源、点位登记、配置来源
//	ybase plugin list 列出已加载的插件包；ybase plugin new <名字> [-project] 生成插件包骨架
//	ybase export      导出当前会话 JSONL
//
// 交互：回合进行中输入的文字作为插话（steer）；Ctrl+C 取消当前回合；
// /mode [档位] 查看或切换权限档位，/max-steps [n] 查看或设置单回合步数上限（0 = 不限），/plugins 查看插件开关，/rewind [n] 退回 n 个回合，/compact 压缩历史，/fork <名字> 分叉并切换，/exit 退出。
// 会话存在 <workdir>/.ybase/state.db（SQLite），同名再次启动即续聊；上次中断的回合自动续跑。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"ybase/appapi"
	"ybase/apps/internal/boot"
	"ybase/modules/security/permission"
	"ybase/modules/security/sandbox"
	"ybase/runtime/agent"
	"ybase/runtime/assemble"
	"ybase/runtime/bus"
	"ybase/runtime/component"
	"ybase/runtime/host"
	"ybase/runtime/pkgs"
	"ybase/runtime/reqlog"
	"ybase/runtime/session"
	"ybase/runtime/store"
	"ybase/runtime/store/sqlite"

	// 插件登记（init 里 component.Register）；装配哪些由 profile 决定
	_ "ybase/modules/collab/subagent"
	_ "ybase/modules/collab/workgroup"
	_ "ybase/modules/context/instructions"
	_ "ybase/modules/context/prompt"
	_ "ybase/modules/context/redact"
	_ "ybase/modules/context/skills"
	_ "ybase/modules/extend/hooks"
	_ "ybase/modules/tools/ask-user"
	_ "ybase/modules/tools/codemode"
	_ "ybase/modules/tools/fs"
	_ "ybase/modules/tools/mcp"
	_ "ybase/modules/tools/shell"
	_ "ybase/modules/tools/todo"
	_ "ybase/modules/tools/web"
)

// 恢复会话时回显的最近消息条数。
const echoRecent = 6

type opts struct {
	base, model, workdir, sess, profile, sandbox, mcp, mode string
	yes, projectHooks, trustProject                         bool
	maxSteps                                                int
}

func main() {
	var o opts
	flag.StringVar(&o.base, "base", env("YBASE_BASE_URL", ""), "OpenAI 兼容端点（覆盖 profile）")
	flag.StringVar(&o.model, "model", env("YBASE_MODEL", ""), "模型名（覆盖 profile）")
	flag.StringVar(&o.workdir, "dir", ".", "工作目录")
	flag.StringVar(&o.sess, "session", "default", "会话名")
	flag.StringVar(&o.profile, "profile", "", "profile TOML（缺省用内置 base profile）")
	flag.StringVar(&o.mode, "mode", "", "权限档位：read-only / ask / workspace（缺省）/ full 或自定义档位名（覆盖配置与会话记录）")
	flag.StringVar(&o.sandbox, "sandbox", "", "固定 shell 沙箱：read-only / workspace-write / danger-full-access（缺省随档位）")
	flag.BoolVar(&o.projectHooks, "project-hooks", false, "加载 <dir>/.ybase/hooks.toml（会执行仓库里写的命令）")
	flag.StringVar(&o.mcp, "mcp", "", "MCP 配置 TOML（缺省 <dir>/.ybase/mcp.toml）")
	flag.IntVar(&o.maxSteps, "max-steps", -1, "单回合步数上限，0 = 不限（缺省不限；指定后覆盖会话记录）")
	flag.BoolVar(&o.trustProject, "trust-project", false, "本次运行信任项目（读 <dir>/.ybase 下的 profile / 权限 / 钩子 / MCP 配置），不保存")
	flag.BoolVar(&o.yes, "yes", false, "等同 -mode full：写文件 / 执行命令不再确认")
	flag.Parse()

	var err error
	switch flag.Arg(0) {
	case "export":
		err = withRuntime(o, func(a *app) error {
			w := bufio.NewWriter(os.Stdout)
			defer w.Flush()
			return a.rt.Store.DB().Export(context.Background(), o.sess, w)
		})
	case "sandbox-reset": // 撤销沙箱对工作区的全部修改（含旧版 Low 标签）
		root, aerr := filepath.Abs(o.workdir)
		if err = aerr; err == nil {
			if err = sandbox.Reset(root, nil); err == nil {
				fmt.Printf("已恢复 %s 的权限设置\n", root)
			}
		}
	case "trust": // ybase trust [-untrust] [dir]：保存项目信任记录
		err = trustCmd(flag.Args()[1:], o.workdir)
	case "mode": // ybase mode list | copy <源> <新名> [-project]
		err = modeCmd(flag.Args()[1:], o.workdir)
	case "explain":
		if args := flag.Args(); len(args) >= 3 && args[1] == "trace" {
			err = withRuntime(o, func(a *app) error {
				out, err := host.ExplainTrace(context.Background(), a.rt.Store.DB(), args[2])
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			})
			break
		}
		err = withRuntime(o, func(a *app) error {
			fmt.Printf("项目信任：%v（%s）\n", a.trusted, a.trustWhy)
			for _, n := range a.notes {
				fmt.Println("注意：" + n)
			}
			fmt.Println("插件开关（全部已登记插件）：")
			for _, st := range a.profile.States() {
				fmt.Println("  " + stateLine(st))
			}
			fmt.Println("插件包：")
			for _, l := range packageLines(a.packages) {
				fmt.Println("  " + l)
			}
			a.rt.Asm.Explain(os.Stdout, a.profile.Sources)
			return nil
		})
	case "plugin": // ybase plugin list | new <名字> [-project]
		err = pluginCmd(flag.Args()[1:], o)
	default:
		err = withRuntime(o, func(a *app) error { return chat(o, a) })
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

// app 装配好的进程：插件运行时 + 应用预置给插件的服务（宿主、终端界面、工作目录）。
type app struct {
	rt      *assembled
	profile *component.Profile
	root    string
	bus     *bus.Bus
	h       *host.Host
	ui      *termUI
	// 项目信任结果与装配提示（横幅 / explain 显示）。
	trusted  bool
	trustWhy string
	notes    []string
	packages []*pkgs.Package // 已加载的插件包
}

// termUI 终端界面（service:ui）。提问交给主循环（终端只在主 goroutine 读写）。
type termUI struct{ ask chan askReq }

func (*termUI) Print(text string)       { fmt.Print(text) }
func (*termUI) Warn(source, msg string) { fmt.Fprintf(os.Stderr, "[%s] %s\n", source, msg) }
func (u *termUI) Ask(ctx context.Context, q string, options []string) (string, error) {
	r := askReq{q: q, options: options, reply: make(chan string, 1)}
	select {
	case u.ask <- r:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case a := <-r.reply:
		return a, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// withRuntime 读信任与 profile → 装配并启动进程（boot.Start，与桌面端 / api-server 同一条路径：
// [kernel] 的宿主参数、存储清理、启动时派发发件箱都在这里接上）→ 执行 f → 关闭（资源全部回收）。
func withRuntime(o opts, f func(*app) error) error {
	var ask func(string) bool
	if interactive && flag.Arg(0) == "" {
		ask = askTrustStdin
	}
	ui := &termUI{ask: make(chan askReq)}
	r, err := boot.Start(boot.StartOptions{
		Flags: boot.Flags{Base: o.base, Model: o.model, Workdir: o.workdir, Profile: o.profile, Sandbox: o.sandbox,
			MCP: o.mcp, Mode: o.mode, Yes: o.yes, ProjectHooks: o.projectHooks, TrustProject: o.trustProject, MaxSteps: o.maxSteps},
		AskTrust: ask,
		UI:       func(*bus.Bus) assemble.UI { return ui },
	})
	if err != nil {
		return err
	}
	defer r.Close()
	a := &app{rt: r.Rt, profile: r.Profile, root: r.Root, bus: r.Bus, h: r.Host, ui: ui,
		trusted: r.Trusted, trustWhy: r.TrustWhy, notes: r.Notes, packages: r.Packages}
	return f(a)
}

// cli 交互状态（只在主 goroutine 里改）。
type cli struct {
	o        opts
	rt       *assembled
	db       store.Store
	h        *host.Host
	api      *appapi.App
	cur      host.Handle
	running  bool
	confirms []confirmReq // 等待确认的调用（并行调用合并成一个队列，逐个提示）
	asks     []askReq     // ask_user 待回答的问题（确认优先）
	exitWant bool
	perms    *permission.Sessions // 未装 permission 插件时为 nil
	profile  *component.Profile
	packages []*pkgs.Package
}

type confirmReq struct{ sess, callID, tool, name, reason, args string }

type askReq struct {
	q       string
	options []string
	reply   chan string
}

// buildChat 建会话构造器与应用接口层：走 boot.BuildApp，与桌面端 / api-server 同一份装配
// （定义来源、状态提交、守卫规则等不会在 CLI 里漏接）。
func buildChat(o opts, a *app) (*boot.Built, error) {
	br := &boot.Runtime{Rt: a.rt, Profile: a.profile, Root: a.root, Bus: a.bus, Host: a.h,
		Trusted: a.trusted, TrustWhy: a.trustWhy, Notes: a.notes, Packages: a.packages}
	return br.BuildApp(boot.AppOptions{
		MaxSteps:     o.maxSteps,
		ModesWorkdir: o.workdir,
		Hooks: func(*assemble.SessionCx) agent.Hooks {
			return agent.Hooks{
				OnReset: func() { fmt.Print("\n[重试中…]\n") },
				OnText:  func(d string) { fmt.Print(d) },
				OnToolCall: func(c session.ToolCall) {
					fmt.Printf("\n⚙ %s %s\n", c.Name, string(c.Args))
				},
				OnToolResult: func(_ session.ToolCall, out string, isErr bool) {
					tag := "✓"
					if isErr {
						tag = "✗"
					}
					fmt.Printf("%s %s\n", tag, preview(out, 300))
				},
			}
		},
	})
}

func chat(o opts, a *app) error {
	rt, root, h := a.rt, a.root, a.h
	askCh := a.ui.ask
	built, err := buildChat(o, a)
	if err != nil {
		return err
	}
	db, modelName, perms, api := built.DB, built.Model, built.Perms, built.API
	for _, err := range built.Ready(context.Background()) { // 构造器就绪后再跑：如工作组补投会打开领导会话
		fmt.Fprintln(os.Stderr, err)
	}
	c := &cli{o: o, rt: rt, db: db, h: h, api: api, perms: perms, profile: a.profile, packages: a.packages}
	ctx := context.Background()
	// 终端自己打印正文与工具输出（Builder 钩子），这里只订阅回合状态、确认请求与提示。
	sub := api.Events(ctx, bus.Filter{Kinds: []string{appapi.EventTurnStart, appapi.EventTurnEnd,
		appapi.EventTurnDenied, appapi.EventHumanRequested, appapi.EventNotice}})
	defer sub.Close()

	c.echoHistory(ctx, o.sess)
	hd, err := h.Open(ctx, o.sess)
	if err != nil {
		return fmt.Errorf("打开会话 %s: %w", o.sess, err)
	}
	c.cur = hd
	if o.maxSteps >= 0 { // 已有会话：命令行指定的上限覆盖会话记录
		n := uint32(o.maxSteps)
		if err := hd.Do(host.Command{Kind: host.SetConfig, MaxSteps: &n}); err != nil {
			return fmt.Errorf("设置步数上限：%w", err)
		}
	} else if cfg := hd.Agent().Committed().Header.Config; cfg != nil && cfg.Policy.MaxSteps == legacyMaxSteps {
		// 旧版缺省值 24 写进了会话配置：改回现缺省（不限），用户可再用 /max-steps 设置。
		n := uint32(0)
		if err := hd.Do(host.Command{Kind: host.SetConfig, MaxSteps: &n}); err != nil {
			return fmt.Errorf("设置步数上限：%w", err)
		}
		fmt.Println("[本会话沿用的是旧版缺省步数上限 24，已改为不限；需要上限用 /max-steps <n>]")
	}
	// 装配快照：会话头 Ext 记下本次开启的插件；恢复时与上次不同就提示（工具 / 提示词可能变了）。
	now := onNames(a.profile)
	if prev, ok := hd.Agent().Committed().Header.Ext[extAssembly]; ok {
		if d := assemblyDiff(prev, now); d != "" {
			fmt.Println("[注意] 本会话上次的插件装配与这次不同：" + d)
		}
	}
	if err := hd.Do(host.Command{Kind: host.SetExt, Key: extAssembly, Value: now}); err != nil {
		return fmt.Errorf("记录插件装配：%w", err)
	}
	fmt.Printf("ybase · %s · %s · 会话 %s\n回车发送；回合中输入为插话；Ctrl+C 取消回合；/mode [档位] /max-steps [n] /plugins /rewind [n] /compact /fork <名字> /exit\n权限档位：%s\n",
		modelName, root, o.sess, c.modeLine(o.sess))
	fmt.Println("插件：" + enabledLine(a.profile))
	for _, n := range a.notes {
		fmt.Println("[注意] " + n)
	}

	lines := make(chan string)
	go func() {
		in := bufio.NewScanner(os.Stdin)
		in.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for in.Scan() {
			lines <- in.Text()
		}
		close(lines)
	}()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)

	c.prompt()
	for {
		if c.exitWant && !c.running {
			return c.cur.Do(host.Command{Kind: host.Drain})
		}
		select {
		case line, ok := <-lines:
			if !ok {
				lines = nil // EOF：等当前回合结束后退出
				c.exitWant = true
				continue
			}
			if err := c.onLine(ctx, strings.TrimSpace(line)); err != nil {
				fmt.Println("错误:", err)
				c.prompt()
			}
		case ev := <-sub.C():
			c.onEvent(ev)
		case q := <-askCh:
			c.asks = append(c.asks, q)
			if len(c.asks) == 1 && len(c.confirms) == 0 {
				c.prompt()
			}
		case <-c.cur.Done():
			if err := c.cur.Err(); err != nil {
				if errors.Is(err, sqlite.ErrConflict) || errors.Is(err, sqlite.ErrLeaseLost) { // session-host D4：另一进程已推进或接管该会话
					return fmt.Errorf("会话 %s 已被另一个进程写入，本进程停止写入（未提交的修改已丢弃）；请关闭其他进程后重开", c.cur.ID())
				}
				return err
			}
			return nil
		case <-sigs:
			if c.running {
				_ = c.cur.Do(host.Command{Kind: host.Cancel})
				fmt.Println("\n[取消中…]")
			} else {
				fmt.Println("\n（没有进行中的回合；/exit 退出）")
				c.prompt()
			}
		}
	}
}

func (c *cli) prompt() {
	switch {
	case len(c.confirms) > 0:
		q := c.confirms[0]
		more := ""
		if n := len(c.confirms) - 1; n > 0 {
			more = fmt.Sprintf("（另有 %d 个待确认）", n)
		}
		fmt.Printf("\n? %s：%s%s\n", q.tool, q.reason, more)
		if q.args != "" {
			fmt.Printf("  参数：%s\n", preview(q.args, 300))
		}
		opt := "（未装权限插件）"
		if c.perms != nil {
			rs, blocked := c.perms.SessionAllow(q.name, json.RawMessage(q.args))
			opt = "（" + ruleList(rs) + "）"
			if len(blocked) > 0 {
				opt += "；启动器类命令不生成会话规则：" + strings.Join(blocked, "、")
			}
		}
		fmt.Printf("  允许？y 本次 / a 本次会话内都允许%s / N 拒绝：", opt)
	case len(c.asks) > 0:
		q := c.asks[0]
		fmt.Printf("\n? %s\n", q.q)
		for i, o := range q.options {
			fmt.Printf("  %d) %s\n", i+1, o)
		}
		fmt.Print("  回答：")
	case !c.running:
		fmt.Print("\n> ")
	}
}

func (c *cli) onEvent(ev bus.Envelope) {
	m, _ := ev.Data.(map[string]any)
	if ev.Session != c.cur.ID() && ev.Kind != "human_requested" && ev.Kind != appapi.EventNotice { // 子智能体的确认上交当前界面
		return
	}
	switch ev.Kind {
	case appapi.EventNotice: // 档位文件变化等提示
		if t, _ := m["text"].(string); t != "" {
			fmt.Println("\n" + t)
		}
	case "turn_start":
		if !c.running {
			fmt.Println("[继续上次未完成的回合]")
		}
		c.running = true
	case "turn_end":
		c.running = false
		c.confirms = nil
		c.asks = nil
		if r, _ := m["reason"].(string); r != "" && r != session.DoneReason().String() {
			fmt.Printf("\n[回合结束：%s]", r)
			if r == session.MaxStepsReason().String() {
				fmt.Print("\n[已达到步数上限，会话状态已保存；输入“继续”接着做，或用 /max-steps 调整上限]")
			}
		}
		fmt.Println()
		c.prompt()
	case "turn_denied":
		c.running = false
		fmt.Println("[输入被拒绝，未开回合]")
		c.prompt()
	case "human_requested":
		q := confirmReq{sess: ev.Session}
		if ev.Session != c.cur.ID() {
			q.tool = "[" + ev.Session + "] "
		}
		q.callID, _ = m["call_id"].(string)
		t, _ := m["tool"].(string)
		q.tool += t
		q.name = t
		q.reason, _ = m["reason"].(string)
		q.args, _ = m["args"].(string)
		c.confirms = append(c.confirms, q)
		if len(c.confirms) == 1 {
			c.prompt()
		}
	}
}

func (c *cli) onLine(ctx context.Context, line string) error {
	if len(c.confirms) > 0 { // 有待确认：这一行是回答
		q := c.confirms[0]
		c.confirms = c.confirms[1:]
		ok := strings.EqualFold(line, "y") || strings.EqualFold(line, "yes")
		always := strings.EqualFold(line, "a")
		hd := c.cur
		if q.sess != "" && q.sess != c.cur.ID() {
			if g, found := c.h.Get(q.sess); found {
				hd = g
			}
		}
		if always && c.perms != nil {
			ok = true
			sid := q.sess
			if sid == "" {
				sid = c.cur.ID()
			}
			rs, _ := c.perms.SessionAllow(q.name, json.RawMessage(q.args))
			var raw []string
			for _, r := range rs {
				raw = c.perms.AllowForSession(sid, r)
			}
			// 规则跟随顶层会话持久化
			top := c.cur
			if t := permission.Top(sid); t != c.cur.ID() {
				if g, found := c.h.Get(t); found {
					top = g
				}
			}
			fmt.Printf("[本会话内都允许 %s]\n", ruleList(rs))
			// 规则已在内存生效；持久化异步做：回合进行中 SetExt 要等到步边界才受理，
			// 而这一步正等着下面的 Confirm，同步等待会死锁。
			go func() {
				if err := top.Do(host.Command{Kind: host.SetExt, Key: permission.ExtRules, Value: raw}); err != nil {
					fmt.Printf("\n[会话规则未能保存：%v]\n", err)
				}
			}()
		}
		err := hd.Do(host.Command{Kind: host.Confirm, CallID: q.callID, Approve: ok})
		c.prompt()
		return err
	}
	if len(c.asks) > 0 { // 有待回答的提问：这一行是答案（数字选项映射为选项文本）
		q := c.asks[0]
		c.asks = c.asks[1:]
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(q.options) {
			line = q.options[n-1]
		}
		q.reply <- line
		c.prompt()
		return nil
	}
	if line == "" {
		c.prompt()
		return nil
	}
	if strings.HasPrefix(line, "/") {
		return c.slash(ctx, line)
	}
	if c.running {
		fmt.Println("[插话已送达，下一步生效]")
		return c.cur.Do(host.Command{Kind: host.Steer, Text: line})
	}
	c.running = true
	return c.cur.Do(host.Command{Kind: host.Send, Text: line})
}

func (c *cli) slash(ctx context.Context, line string) error {
	f := strings.Fields(line)
	switch f[0] {
	case "/exit", "/quit":
		c.exitWant = true
		if c.running {
			fmt.Println("[当前回合结束后退出]")
		}
		return nil
	case "/stats":
		return c.stats(ctx)
	case "/mode":
		return c.mode(f[1:])
	case "/max-steps":
		return c.maxSteps(f[1:])
	case "/plugins": // 只看不改：插件开关在 profile 里改，重启生效（plugin-switch-plan §3.6）
		for _, st := range c.profile.States() {
			fmt.Println("  " + stateLine(st))
		}
		fmt.Println("插件包：")
		for _, l := range packageLines(c.packages) {
			fmt.Println("  " + l)
		}
		fmt.Println("（改开关：编辑 ~/.ybase/profile.toml 或 <dir>/.ybase/profile.toml，重启生效）")
		return nil
	}
	if c.running {
		return fmt.Errorf("回合进行中，%s 需在空闲时使用（可先 Ctrl+C 取消）", f[0])
	}
	switch f[0] {
	case "/rewind":
		n := 1
		if len(f) > 1 {
			v, err := strconv.Atoi(f[1])
			if err != nil || v < 1 {
				return fmt.Errorf("用法：/rewind [回合数]")
			}
			n = v
		}
		if _, err := c.api.Rewind(ctx, appapi.RewindArgs{Session: c.cur.ID(), Turns: n}); err != nil {
			return err
		}
		fmt.Printf("已退回 %d 个回合\n", n)
	case "/compact":
		if err := c.compact(ctx); err != nil {
			return err
		}
	case "/fork":
		if len(f) < 2 {
			return fmt.Errorf("用法：/fork <新会话名>")
		}
		hd, err := c.h.Fork(ctx, c.cur.ID(), f[1], -1, "fork")
		if err != nil {
			return err
		}
		_ = c.cur.Do(host.Command{Kind: host.Close})
		c.cur = hd
		fmt.Printf("已分叉到会话 %s（下次用 -session %s 打开）\n", f[1], f[1])
	default:
		return fmt.Errorf("未知命令 %s", f[0])
	}
	c.prompt()
	return nil
}

// compact 手动压缩（/compact）：经 appapi 由 compact-basic 生成摘要，再以 Compact 命令写入。
func (c *cli) compact(ctx context.Context) error {
	if !c.api.HasCompactor() {
		return fmt.Errorf("未装配 compact-basic 插件")
	}
	fmt.Println("[压缩中…]")
	r, err := c.api.Compact(ctx, appapi.SessionArgs{Session: c.cur.ID()})
	if err != nil {
		return err
	}
	fmt.Printf("已压缩 %d 条日志为摘要（约 %v → %v token）\n", r.Upto, r.TokensBefore, r.TokensAfter)
	return nil
}

// stats 打印会话用量、缓存命中率与最近一次请求的上下文组成（只读投影，回合中也可用）。
func (c *cli) stats(ctx context.Context) error {
	st, err := c.db.SessionStats(ctx, c.cur.ID())
	if err != nil {
		return err
	}
	fmt.Printf("模型调用 %d 次（失败 %d，无用量 %d）· 工具调用 %d · 压缩 %d\n", st.Attempts, st.Errors, st.Missing, st.ToolCalls, st.Compacts)
	fmt.Printf("输入 %d · 输出 %d · 缓存读 %d · 缓存写 %d\n", st.Input, st.Output, st.CacheRead, st.CacheWrite)
	if r := st.HitRate(); r >= 0 {
		fmt.Printf("缓存命中率 %.1f%%（%d 次有厂商用量的调用）\n", r*100, st.Reported)
	} else {
		fmt.Println("缓存命中率：无数据")
	}
	if len(st.Composition) > 0 {
		var comp reqlog.Composition
		if json.Unmarshal(st.Composition, &comp) == nil && comp.Total > 0 {
			keys := make([]string, 0, len(comp.Parts))
			for k := range comp.Parts {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return comp.Parts[keys[i]] > comp.Parts[keys[j]] })
			fmt.Printf("最近一次请求上下文（估算）约 %d token：\n", comp.Total)
			for _, k := range keys {
				fmt.Printf("  %-24s %7d  %5.1f%%\n", k, comp.Parts[k], float64(comp.Parts[k])*100/float64(comp.Total))
			}
		}
	}
	return nil
}

// echoHistory 恢复会话时只回显最近几条消息。
func (c *cli) echoHistory(ctx context.Context, id string) {
	l, err := c.db.Load(ctx, id, nil)
	if err != nil {
		return
	}
	core, err := session.Restore(l.Snapshot)
	if err != nil {
		return
	}
	msgs := agent.History(core)
	fmt.Printf("已恢复会话 %s（%d 条消息）\n", id, len(msgs))
	if len(msgs) > echoRecent {
		fmt.Printf("  …（省略 %d 条）\n", len(msgs)-echoRecent)
		msgs = msgs[len(msgs)-echoRecent:]
	}
	for _, m := range msgs {
		t := ""
		for _, p := range m.Content {
			t += p.Text
		}
		if m.Role == "tool" || (t == "" && len(m.ToolCalls) > 0) {
			continue
		}
		fmt.Printf("  %s: %s\n", m.Role, preview(t, 120))
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (c *cli) modeText(m permission.Mode) string {
	if c.perms != nil {
		if d, ok := c.perms.Registry().Get(m); ok {
			return permission.Label(d, c.perms.Items()) // 自定义档位带基础档位与放宽清单（§9.6）
		}
	}
	return string(m)
}

// maxSteps /max-steps 查看或设置单回合步数上限（存在会话配置里，重启后仍生效；回合中到下一步边界生效）。
func (c *cli) maxSteps(args []string) error {
	if len(args) == 0 {
		cur := c.cur.Agent().Committed().Header.Config.Policy.MaxSteps
		if cur == 0 {
			fmt.Println("当前步数上限：不限（/max-steps <n> 设置，0 = 不限）")
		} else {
			fmt.Printf("当前步数上限：%d（/max-steps <n> 设置，0 = 不限）\n", cur)
		}
		return nil
	}
	v, err := strconv.Atoi(args[0])
	if err != nil || v < 0 {
		return fmt.Errorf("用法：/max-steps <n>（0 = 不限）")
	}
	n := uint32(v)
	if err := c.cur.Do(host.Command{Kind: host.SetConfig, MaxSteps: &n}); err != nil {
		return err
	}
	if n == 0 {
		fmt.Println("步数上限：不限")
	} else {
		fmt.Printf("步数上限：%d\n", n)
	}
	return nil
}

// modeLine 横幅里的档位说明。
func (c *cli) modeLine(id string) string {
	if c.perms == nil {
		return "未装 permission 插件（不做权限检查）"
	}
	return c.modeText(c.perms.Mode(id))
}

// mode /mode 查看或切换当前会话的权限档位（持久化到会话 Ext，重启后仍生效）。
func (c *cli) mode(args []string) error {
	if c.perms == nil {
		return fmt.Errorf("profile 中没有 permission 插件")
	}
	id := c.cur.ID()
	if len(args) == 0 {
		fmt.Printf("当前档位：%s，沙箱 %s\n", c.modeText(c.perms.Mode(id)), c.perms.SandboxMode(id))
		printModes(c.perms.Registry())
		if rs := c.perms.SessionRules(id); len(rs) > 0 {
			fmt.Printf("本会话允许规则：%s\n", strings.Join(rs, "，"))
		}
		fmt.Println("切换：/mode <名字>；完整取值表：/mode show [名字]；复制成自定义档位：/mode copy <源> <新名> [-project]")
		return nil
	}
	if args[0] == "copy" {
		return modeCopy(c.perms.Registry(), args[1:])
	}
	if args[0] == "show" {
		m := c.perms.Mode(id)
		if len(args) > 1 {
			m = permission.Mode(args[1])
		}
		d, ok := c.perms.Registry().Get(m)
		if !ok {
			return fmt.Errorf("没有档位 %q", m)
		}
		fmt.Print(permission.Show(d, c.perms.Items()))
		return nil
	}
	m := permission.Mode(args[0])
	// §9.6：受信项目提交的档位若放宽了权限，首次选用要用户确认一次（记进 trust.toml，放宽清单变了要重新确认）。
	if d, ok := c.perms.Registry().Get(m); ok && d.Project {
		if w := strings.Join(permission.ProjectWidened(d, c.perms.Items()), "、"); w != "" && !modeApproved(c.o.workdir, string(m), w) {
			if len(args) < 2 || args[1] != "-confirm" {
				fmt.Printf("档位 %s 来自项目 .ybase/modes，放宽了：%s\n确认使用请输入：/mode %s -confirm\n", m, w, m)
				return nil
			}
			if err := approveMode(c.o.workdir, string(m), w); err != nil {
				return err
			}
		}
	}
	if err := c.perms.SetMode(id, m); err != nil {
		return err
	}
	m = c.perms.Mode(id)
	fmt.Printf("已切换到 %s，沙箱 %s\n", c.modeText(m), c.perms.SandboxMode(id))
	// 内存里已生效；持久化异步：回合中要等到步边界才受理，同步等会卡住确认输入
	hd := c.cur
	go func() {
		if err := hd.Do(host.Command{Kind: host.SetExt, Key: permission.ExtMode, Value: string(m)}); err != nil {
			fmt.Printf("\n[档位未能保存：%v]\n", err)
		}
	}()
	return nil
}

func preview(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ruleList 规则列表的展示写法。
func ruleList(rs []permission.Rule) string {
	s := make([]string, len(rs))
	for i, r := range rs {
		s[i] = r.String()
	}
	return strings.Join(s, "、")
}

// legacyMaxSteps 旧版写进会话配置的缺省步数上限（现缺省为不限）。
const legacyMaxSteps = 24
