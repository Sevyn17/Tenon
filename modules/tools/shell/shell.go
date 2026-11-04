package shell

// 5.3：shell / job_output / job_kill（docs/90 - 归档/20 - 插件/10 - 命令执行.md）。
// 每次调用新进程；超时或取消时杀整个进程树；后台 job 只存在于本进程内存。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"ybase/modules/internal/proc"
	"ybase/runtime/session"
	"ybase/runtime/toolhub"
)

// shell 相关默认值。
const (
	ShellTimeout    = 120 * time.Second
	ShellMaxTimeout = 600 * time.Second
	ShellMaxOutput  = 4 << 20 // 前台输出内存上限（保留尾部）
	JobBuffer       = 1 << 20 // 每个后台 job 保留的尾部输出
	JobWait         = 30 * time.Second
)

// tailBuf 只保留最近 cap 字节的并发安全缓冲。环形：写满后覆盖最旧的字节，不再重分配
// （2026-11-04 审查 P1-44：原先写满后每次写都把整段 cap 字节复制一遍，大输出命令开销是 O(总量 × cap)）。
type tailBuf struct {
	mu    sync.Mutex
	cap   int
	ring  []byte // 长度增长到 cap 后固定
	total int64  // 累计写入字节；下一个字节写在 ring[total % cap]
}

func (b *tailBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.cap <= 0 {
		b.total += int64(n)
		return n, nil
	}
	if len(p) > b.cap { // 只有最后 cap 字节留得下
		b.total += int64(len(p) - b.cap)
		p = p[len(p)-b.cap:]
		if len(b.ring) < b.cap {
			// 这次写会覆盖全部 cap 字节：直接转成满环，按 total % cap 定位
			// （未满时追加是从 ring[0] 起放，与跳过后的 total 对不上）。
			b.ring = append(b.ring, make([]byte, b.cap-len(b.ring))...)
		}
	}
	if len(b.ring) < b.cap { // 还没写满：追加，满了再转成环
		room := b.cap - len(b.ring)
		k := min(room, len(p))
		b.ring = append(b.ring, p[:k]...)
		b.total += int64(k)
		p = p[k:]
	}
	for len(p) > 0 {
		pos := int(b.total % int64(b.cap))
		k := copy(b.ring[pos:], p)
		b.total += int64(k)
		p = p[k:]
	}
	return n, nil
}

// since 返回自累计偏移 off 起的输出、新偏移、被丢弃的字节数。
func (b *tailBuf) since(off int64) (string, int64, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	first := b.total - int64(len(b.ring))
	var dropped int64
	if off < first {
		dropped, off = first-off, first
	}
	n := int(b.total - off)
	if n <= 0 {
		return "", b.total, dropped
	}
	if len(b.ring) < b.cap { // 未写满：按顺序存放
		return string(b.ring[len(b.ring)-n:]), b.total, dropped
	}
	start := int(off % int64(b.cap))
	out := make([]byte, 0, n)
	if start+n <= b.cap {
		out = append(out, b.ring[start:start+n]...)
	} else {
		out = append(out, b.ring[start:]...)
		out = append(out, b.ring[:n-(b.cap-start)]...)
	}
	return string(out), b.total, dropped
}

// Shell 一组共享后台 job 表的 shell 工具。
type Shell struct {
	Root string
	// argv 构造命令行（测试可替换）；默认按平台选 pwsh/powershell 或 bash/sh。
	argv func(command string) []string
	// Sandbox 可空（= 不受限）。非空时每次执行按会话模式或已批准的申请放进沙箱，无法强制执行则拒绝（不降级）。
	Sandbox toolhub.Confiner
	// Mode 会话沙箱模式（Sandbox 非空时使用）。
	Mode toolhub.SandboxMode
	// ModeFor 可空：按会话取沙箱模式（权限档位切换后即时生效）；空则用 Mode。
	ModeFor func(sessionID string) toolhub.SandboxMode
	// Approver 可空：会话规则批准的更宽沙箱申请按已批准执行。
	Approver toolhub.SandboxApprover
	// 以下开关零值 = 开启（插件配置 strip_ansi / deny_hint / intro，plugin-switch-plan §2.2）。
	KeepANSI   bool // 不去终端控制序列
	NoDenyHint bool // 不追加沙箱拒绝提示
	NoIntro    bool // 工具描述不带平台语法说明

	mu   sync.Mutex
	seq  int
	jobs map[string]*job
}

type job struct {
	id string
	// session 启动它的会话：只有同一会话能读 / 杀（2026-11-04 审查 P1-9：原先键全局，A 会话能操作 B 的 job）。
	session string
	command string
	buf     *tailBuf
	read    int64
	done    chan struct{}
	exit    int
	err     error
	cancel  context.CancelFunc
	started time.Time
}

// NewShell 构造 shell 工具组。
func NewShell(root string) *Shell {
	root, _ = filepath.Abs(root)
	return &Shell{Root: root, argv: defaultArgv(), jobs: map[string]*job{}}
}

func defaultArgv() func(string) []string { return proc.ShellArgv(true) }

// Tools 返回 shell、job_output、job_kill。
func (s *Shell) Tools() []*toolhub.Tool {
	tail := map[string]any{toolhub.AttrPreview: "tail"}
	return []*toolhub.Tool{
		{
			Spec: session.ToolSpec{Name: "shell",
				Description: fmt.Sprintf(s.intro()+"返回合并的 stdout/stderr 与退出码。每次调用是新进程，已在工作目录执行（不必先 cd），cd 与环境变量不保留。timeout 秒（默认 %d，最大 %d），超时杀整个进程树。长时间运行（服务、监听）用 background:true，返回 job id，再用 job_output 读取输出、job_kill 终止。", int(ShellTimeout.Seconds()), int(ShellMaxTimeout.Seconds())),
				Schema:      json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"integer","description":"超时秒数"},"background":{"type":"boolean","description":"后台运行，立即返回 job id"},"sandbox_permissions":{"type":"string","enum":["read-only","workspace-write","danger-full-access"],"description":"仅在命令被沙箱拒绝后使用：为本次调用申请更宽的沙箱，需要用户批准"}},"required":["command"]}`)},
			Risk:    toolhub.Dangerous,
			Timeout: ShellMaxTimeout + 10*time.Second,
			Attrs:   map[string]any{toolhub.AttrPreview: "tail", toolhub.AttrSandboxed: true},
			Items:   []string{ItemExec},
			Call:    s.callShell,
		},
		{
			Spec: session.ToolSpec{Name: "job_output",
				Description: "读取后台 job 自上次读取以来的新输出与状态。wait:true 时等到 job 结束或 timeout 秒（默认 30）。",
				Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"wait":{"type":"boolean"},"timeout":{"type":"integer"}},"required":["id"]}`)},
			Risk:    toolhub.Read,
			Timeout: ShellMaxTimeout + 10*time.Second,
			Attrs:   tail,
			Items:   []string{ItemJob},
			Call:    s.callOutput,
		},
		{
			Spec: session.ToolSpec{Name: "job_kill",
				Description: "终止一个后台 job（连同其子进程）。",
				Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)},
			Risk:  toolhub.Write,
			Items: []string{ItemJob},
			Call:  s.callKill,
		},
	}
}

func (s *Shell) command(ctx context.Context, command string, out *tailBuf) *exec.Cmd {
	a := s.argv(command)
	cmd := exec.CommandContext(ctx, a[0], a[1:]...)
	cmd.Dir = s.Root
	cmd.Stdout, cmd.Stderr = out, out
	proc.SetTree(cmd)
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func (s *Shell) approved(cx *toolhub.Cx, raw json.RawMessage) bool {
	if cx == nil {
		return false
	}
	if cx.Approved {
		return true
	}
	if s.Approver == nil || cx.Tool == nil {
		return false
	}
	req, err := toolhub.RequestedSandbox(raw)
	return err == nil && req != "" && s.Approver.AllowsSandbox(cx.SessionID, cx.Tool.Spec.Name, raw, req)
}

// confine 按沙箱模式包装 cmd；返回的 cleanup 在 Start 之后调用。
func (s *Shell) confine(cmd *exec.Cmd, cx *toolhub.Cx, raw json.RawMessage) (func(), error) {
	if s.Sandbox == nil {
		return func() {}, nil
	}
	approved := s.approved(cx, raw)
	base := s.Mode
	if s.ModeFor != nil && cx != nil {
		base = s.ModeFor(cx.SessionID)
	}
	mode, err := toolhub.EffectiveSandbox(base, raw, approved)
	if err != nil {
		return nil, err
	}
	return s.Sandbox.Apply(cmd, mode)
}

func exitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}

func (s *Shell) callShell(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
	var a struct {
		Command    string `json:"command"`
		Timeout    int    `json:"timeout"`
		Background bool   `json:"background"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.Command == "" {
		return toolhub.Failf("参数不合法：需要 command"), nil
	}
	if a.Background {
		return s.startJob(a.Command, cx, raw)
	}
	to := ShellTimeout
	if a.Timeout > 0 {
		to = min(time.Duration(a.Timeout)*time.Second, ShellMaxTimeout)
	}
	rctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	buf := &tailBuf{cap: ShellMaxOutput}
	cmd := s.command(rctx, a.Command, buf)
	cleanup, err := s.confine(cmd, cx, raw)
	if err != nil {
		return toolhub.Failf("%v（命令未执行）", err), nil
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return toolhub.Failf("无法执行命令：%v", err), nil
	}
	cleanup()
	code, err := exitCode(cmd.Wait())
	text, total, dropped := buf.since(0)
	_ = total
	text = s.clean(text)
	if dropped > 0 {
		text = fmt.Sprintf("…（前 %d 字节已丢弃）\n", dropped) + text
	}
	switch {
	case errors.Is(rctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return failText(text + fmt.Sprintf("\n[超时 %d 秒，已终止进程树]", int(to.Seconds()))), nil
	case ctx.Err() != nil:
		return failText(text + "\n[已取消，进程树已终止]"), nil
	case err != nil:
		return toolhub.Failf("无法执行命令：%v", err), nil
	}
	text += fmt.Sprintf("\n[退出码 %d]", code)
	if code != 0 {
		return failText(text + s.hint(cx, raw, text)), nil
	}
	return toolhub.Text(text), nil
}

// denyHint 沙箱内命令输出像“拒绝访问”时，提示模型可申请一次更宽的沙箱（需用户批准）。
func (s *Shell) denyHint(cx *toolhub.Cx, raw json.RawMessage, out string) string {
	if s.Sandbox == nil {
		return ""
	}
	base := s.Mode
	if s.ModeFor != nil && cx != nil {
		base = s.ModeFor(cx.SessionID)
	}
	mode, err := toolhub.EffectiveSandbox(base, raw, s.approved(cx, raw))
	if err != nil || mode == toolhub.SandboxFull || !looksDenied(out) {
		return ""
	}
	next := toolhub.SandboxWorkspaceWrite
	if mode == toolhub.SandboxWorkspaceWrite {
		next = toolhub.SandboxFull
	}
	hint := ""
	if strings.Contains(strings.ToLower(out), "spawn eperm") {
		// 写受限令牌下 node/libuv 的命名管道 stdio 打不开（已知限制，见 docs/90 - 归档/20 - 插件/06 - 沙箱.md）
		hint = "node 在沙箱内不能用管道启动子进程；"
	}
	return fmt.Sprintf("\n[可能被沙箱 %s 拒绝：%s如确需越出，用同一命令加 sandbox_permissions:%q 重试，需用户批准，仅本次有效]", mode, hint, next)
}

func looksDenied(s string) bool {
	l := strings.ToLower(s)
	for _, k := range []string{"access is denied", "access to the path", "permission denied", "unauthorizedaccess", "拒绝访问", "operation not permitted", "read-only file system", "spawn eperm"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// stripANSI 去掉终端控制序列（颜色、光标移动、OSC 标题等），只影响返回给模型的文本
// （docs/03 - 约束清单.md“shell / job_output 输出清洗”）。
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) && !strings.ContainsRune(s, 0x9b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			// 8 位 CSI（U+009B，UTF-8 为 C2 9B）
			if c == 0xc2 && i+1 < len(s) && s[i+1] == 0x9b {
				i = skipCSI(s, i+2)
				continue
			}
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			break // 末尾残缺的 ESC
		}
		switch s[i+1] {
		case '[':
			i = skipCSI(s, i+2)
		case ']', 'P', '_', '^', 'X': // OSC / DCS / APC / PM / SOS：到 BEL 或 ST 为止
			j := i + 2
			for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
				j++
			}
			switch {
			case j >= len(s):
				i = j
			case s[j] == 0x07:
				i = j + 1
			default:
				i = j + 2
			}
		default:
			i += 2 // 两字节序列，如 ESC = / ESC >
		}
	}
	return b.String()
}

// skipCSI 跳过参数 / 中间字节，返回终止字节（0x40–0x7E）之后的位置。
func skipCSI(s string, j int) int {
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
		j++
	}
	if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
		j++
	}
	return j
}

func failText(s string) toolhub.Output {
	o := toolhub.Text(s)
	o.Status = session.ToolError
	return o
}

func (s *Shell) startJob(command string, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{session: sessionOf(cx), command: command, buf: &tailBuf{cap: JobBuffer}, done: make(chan struct{}), cancel: cancel, started: time.Now()}
	cmd := s.command(ctx, command, j.buf)
	cleanup, err := s.confine(cmd, cx, raw)
	if err != nil {
		cancel()
		return toolhub.Failf("%v（命令未执行）", err), nil
	}
	err = cmd.Start()
	cleanup()
	if err != nil {
		cancel()
		return toolhub.Failf("无法启动命令：%v", err), nil
	}
	s.mu.Lock()
	s.seq++
	j.id = "job-" + strconv.Itoa(s.seq)
	s.jobs[j.id] = j
	s.mu.Unlock()
	go func() {
		j.exit, j.err = exitCode(cmd.Wait())
		cancel()
		close(j.done)
	}()
	return toolhub.Text(fmt.Sprintf("已在后台启动 %s（pid %d）。用 job_output 读取输出，job_kill 终止。", j.id, cmd.Process.Pid)), nil
}

func sessionOf(cx *toolhub.Cx) string {
	if cx == nil {
		return ""
	}
	return cx.SessionID
}

// lookup 取本会话的 job；别的会话的 job 按不存在处理。
func (s *Shell) lookup(id, session string) (*job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok || j.session != session {
		return nil, false
	}
	return j, true
}

// forget 已结束、输出已读完的 job 从表里删掉（每个占最多 JobBuffer 字节，原先从不回收）。
func (s *Shell) forget(j *job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs[j.id] == j {
		delete(s.jobs, j.id)
	}
}

func (s *Shell) callOutput(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
	var a struct {
		ID      string `json:"id"`
		Wait    bool   `json:"wait"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return toolhub.Failf("参数不合法：%v", err), nil
	}
	j, ok := s.lookup(a.ID, sessionOf(cx))
	if !ok {
		return toolhub.Failf("job %s 不存在（进程重启后后台任务已丢失；已结束的 job 读完输出后即清理）", a.ID), nil
	}
	if a.Wait {
		to := JobWait
		if a.Timeout > 0 {
			to = min(time.Duration(a.Timeout)*time.Second, ShellMaxTimeout)
		}
		t := time.NewTimer(to)
		select {
		case <-j.done:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	s.mu.Lock() // 读偏移只由本函数推进
	text, next, dropped := j.buf.since(j.read)
	j.read = next
	s.mu.Unlock()
	text = s.clean(text)
	if dropped > 0 {
		text = fmt.Sprintf("…（%d 字节输出已超出缓冲被丢弃）\n", dropped) + text
	}
	select {
	case <-j.done:
		// 已结束：上面读到的就是剩余的全部输出，交出后清理。
		s.mu.Lock()
		rest, _, _ := j.buf.since(j.read)
		s.mu.Unlock()
		if rest == "" {
			s.forget(j)
		}
		status := fmt.Sprintf("[%s 已结束，退出码 %d]", j.id, j.exit)
		if j.err != nil {
			status = fmt.Sprintf("[%s 异常结束：%v]", j.id, j.err)
		}
		return toolhub.Text(text + "\n" + status), nil
	default:
		return toolhub.Text(text + fmt.Sprintf("\n[%s 运行中，已运行 %s]", j.id, time.Since(j.started).Round(time.Second))), nil
	}
}

func (s *Shell) callKill(_ context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return toolhub.Failf("参数不合法：%v", err), nil
	}
	j, ok := s.lookup(a.ID, sessionOf(cx))
	if !ok {
		return toolhub.Failf("job %s 不存在", a.ID), nil
	}
	select {
	case <-j.done:
		s.forget(j)
		return toolhub.Text(fmt.Sprintf("%s 早已结束（退出码 %d）", j.id, j.exit)), nil
	default:
	}
	j.cancel()
	select {
	case <-j.done:
	case <-time.After(5 * time.Second):
		return toolhub.Failf("已发送终止信号，%s 5 秒内未退出", j.id), nil
	}
	s.forget(j)
	return toolhub.Text(fmt.Sprintf("已终止 %s", j.id)), nil
}

// Close 终止全部后台 job（进程退出时调用）。
func (s *Shell) Close() {
	s.mu.Lock()
	js := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		js = append(js, j)
	}
	s.mu.Unlock()
	for _, j := range js {
		j.cancel()
	}
	for _, j := range js {
		select {
		case <-j.done:
		case <-time.After(5 * time.Second):
		}
	}
}

// shellIntro 按平台说明命令语法，避免模型在 PowerShell 里写 bash 语法。
func shellIntro() string {
	if runtime.GOOS == "windows" {
		return "在工作目录执行一条 PowerShell 命令（不是 bash：不支持 <<<、heredoc、export、反引号命令替换；用 ; 分隔语句，用 $env:NAME 读写环境变量，用 2>&1 合并输出）。"
	}
	return "在工作目录执行一条 bash 命令。"
}

func (s *Shell) intro() string {
	if s.NoIntro {
		return ""
	}
	return shellIntro()
}

func (s *Shell) clean(t string) string {
	if s.KeepANSI {
		return t
	}
	return stripANSI(t)
}

func (s *Shell) hint(cx *toolhub.Cx, raw json.RawMessage, out string) string {
	if s.NoDenyHint {
		return ""
	}
	return s.denyHint(cx, raw, out)
}
