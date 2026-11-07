//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

// group 新进程组（taskkill /T 按进程树杀，不依赖进程组，但新组让 Ctrl-C 不波及宿主）。
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree 杀整个进程树：先按系统快照记下所有后代，再杀主进程，最后逐个杀掉记下的后代（孙进程）。
// 不用 taskkill /T：它是另一个进程，负载高时要几百毫秒到几秒，偶尔还会失败（退出码非零），
// 失败时退回只杀主进程就把孙进程留下、占着管道（2026-11-07 CI 全量并行下 proc.TestKillTree 偶发超时）；
// 快照 + TerminateProcess 是纯系统调用，快且不依赖外部程序。快照失败时才退回 taskkill。
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := uint32(cmd.Process.Pid)
	kids, snapOK := descendants(pid)
	err := cmd.Process.Kill() // 先杀主进程：它不再派生子进程
	if snapOK {
		for i := len(kids) - 1; i >= 0; i-- { // 深的先杀（叶子在前）
			if kids[i] != pid {
				terminate(kids[i])
			}
		}
		return nil // 主进程 Kill 报错多半是它已自行退出，后代已按快照清掉，不算失败
	}
	// 快照失败：退回 taskkill /T，再不行只杀主进程。
	k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(int(pid)))
	if kerr := k.Run(); kerr != nil && err != nil {
		return err
	}
	return nil
}

// descendants 主进程及其全部后代（按层序：深的在后面）。快照失败返回 false。
func descendants(root uint32) ([]uint32, bool) {
	snap, e := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if e != nil {
		return nil, false
	}
	defer syscall.CloseHandle(snap)
	children := map[uint32][]uint32{}
	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for e := syscall.Process32First(snap, &pe); e == nil; e = syscall.Process32Next(snap, &pe) {
		children[pe.ParentProcessID] = append(children[pe.ParentProcessID], pe.ProcessID)
	}
	out := []uint32{root}
	for i := 0; i < len(out); i++ {
		out = append(out, children[out[i]]...)
	}
	return out, true
}

// terminate 结束一个进程；已经退出返回 false。
func terminate(pid uint32) bool {
	h, e := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, pid)
	if e != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	return syscall.TerminateProcess(h, 1) == nil
}
