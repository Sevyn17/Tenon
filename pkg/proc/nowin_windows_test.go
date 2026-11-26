//go:build windows

package proc

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
)

// NoWindow 合并已有字段：不覆盖 Token / CmdLine / 其他 CreationFlags。
func TestNoWindowMerges(t *testing.T) {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "x", Token: 7, CreationFlags: 0x10}
	NoWindow(cmd)
	a := cmd.SysProcAttr
	if a.CmdLine != "x" || a.Token != 7 || a.CreationFlags&0x10 == 0 {
		t.Fatalf("已有字段被覆盖：%+v", a)
	}
	if !a.HideWindow || a.CreationFlags&createNoWindow == 0 {
		t.Fatalf("没设 HideWindow / CREATE_NO_WINDOW：%+v", a)
	}
	var nilCmd exec.Cmd
	NoWindow(&nilCmd) // SysProcAttr 为 nil 时新建
	if nilCmd.SysProcAttr == nil || nilCmd.SysProcAttr.CreationFlags != createNoWindow {
		t.Fatalf("nil SysProcAttr：%+v", nilCmd.SysProcAttr)
	}
}

// NoWindow 与 Group / SetTree 调用顺序无关：最终 flags 一致。
func TestNoWindowOrderIndependent(t *testing.T) {
	want := uint32(createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP)
	steps := map[string][]func(*exec.Cmd){
		"Group,NoWindow":   {Group, NoWindow},
		"NoWindow,Group":   {NoWindow, Group},
		"SetTree,NoWindow": {SetTree, NoWindow},
		"NoWindow,SetTree": {NoWindow, SetTree},
	}
	for name, fs := range steps {
		cmd := exec.CommandContext(context.Background(), "cmd.exe")
		for _, f := range fs {
			f(cmd)
		}
		if got := cmd.SysProcAttr.CreationFlags; got != want || !cmd.SysProcAttr.HideWindow {
			t.Errorf("%s：flags=%#x hide=%v，期望 %#x", name, got, cmd.SysProcAttr.HideWindow, want)
		}
	}
}

// 设了 NoWindow 的进程照常启动、输出走管道。
func TestNoWindowRuns(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "echo ok")
	NoWindow(cmd)
	Group(cmd)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
