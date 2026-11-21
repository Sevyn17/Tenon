//go:build windows

package wsfs

// Windows 监视器：ReadDirectoryChangesW 递归监视整棵树，完成端口取结果（短超时等待，不起 goroutine，见 drainFirst）。

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 64 KiB：网络卷上 ReadDirectoryChangesW 的缓冲区上限（虽然只监视本地盘，留同样的尺寸）。
const rdcwBuf = 64 << 10

// ReadDirectoryChangesW 的完成包不是在写文件的系统调用里同步入队的：内核要先在发起请求的线程上把结果
// 拷进用户缓冲区（APC），完成包随后才进端口。实测（2026-11-21，NTFS，随机增删改 / 目录改名）
// 超时 0 的排空 15 轮里有 5 轮漏掉刚发生的改动；先等 drainFirst 毫秒、取到后每批再等 drainSettle 毫秒，
// 15 轮全部一致。所以每次排空至少等 drainFirst（没有通知时就是这么久）。
const (
	drainFirst  uint32 = 10
	drainSettle uint32 = 10
)

const rdcwMask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
	windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE | windows.FILE_NOTIFY_CHANGE_CREATION

type winWatcher struct {
	dir     windows.Handle
	port    windows.Handle
	ov      *windows.Overlapped
	buf     []byte
	pending bool // 有一次 ReadDirectoryChanges 在途（buf 归内核所有）
}

func openWatcher(root string) (watcher, error) {
	vol := filepath.VolumeName(root)
	if vol == "" || strings.HasPrefix(vol, `\\`) {
		return nil, errNoWatch // UNC（网络共享、\\wsl$）：别的机器上的改动没有通知
	}
	vp, err := windows.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return nil, err
	}
	if t := windows.GetDriveType(vp); t != windows.DRIVE_FIXED && t != windows.DRIVE_RAMDISK {
		return nil, errNoWatch
	}
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	port, err := windows.CreateIoCompletionPort(h, 0, 0, 1)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	w := &winWatcher{dir: h, port: port, ov: &windows.Overlapped{}, buf: make([]byte, rdcwBuf)}
	if err := w.issue(); err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

func (w *winWatcher) recursive() bool          { return true }
func (w *winWatcher) add(string, string) error { return nil }

func (w *winWatcher) issue() error {
	*w.ov = windows.Overlapped{}
	if err := windows.ReadDirectoryChanges(w.dir, &w.buf[0], uint32(len(w.buf)), true, rdcwMask, nil, w.ov, 0); err != nil {
		return err
	}
	w.pending = true
	return nil
}

func (w *winWatcher) drain() ([]change, bool, error) {
	var out []change
	over := false
	wait := drainFirst
	for {
		var n uint32
		var key uintptr
		var ov *windows.Overlapped
		err := windows.GetQueuedCompletionStatus(w.port, &n, &key, &ov, wait)
		// 取到一批后重新发起的请求：两次请求之间内核缓冲的改动由新请求带回，它的完成包可能稍晚才进端口。
		// 之后每次等 drainSettle，直到一次等待超时。
		wait = drainSettle
		if ov == nil { // 没有已完成的：队列空了（WAIT_TIMEOUT）或端口本身出错
			if err != nil && !errors.Is(err, windows.WAIT_TIMEOUT) {
				return nil, false, err
			}
			return out, over, nil
		}
		w.pending = false
		switch {
		case errors.Is(err, windows.ERROR_NOTIFY_ENUM_DIR):
			over = true // 内部缓冲区溢出
		case err != nil:
			return nil, false, err // 根目录被删等：监视失效
		case n == 0:
			over = true
		default:
			out = append(out, parseNotify(w.buf[:n])...)
		}
		if err := w.issue(); err != nil {
			return nil, false, err
		}
	}
}

func parseNotify(b []byte) []change {
	var out []change
	for off := 0; off+12 <= len(b); {
		next := *(*uint32)(unsafe.Pointer(&b[off]))
		action := *(*uint32)(unsafe.Pointer(&b[off+4]))
		nlen := int(*(*uint32)(unsafe.Pointer(&b[off+8])))
		if off+12+nlen > len(b) {
			break
		}
		u := unsafe.Slice((*uint16)(unsafe.Pointer(&b[off+12])), nlen/2)
		name := strings.ReplaceAll(windows.UTF16ToString(u), `\`, "/")
		out = append(out, change{path: name, replaced: action != windows.FILE_ACTION_MODIFIED})
		if next == 0 {
			break
		}
		off += int(next)
	}
	return out
}

func (w *winWatcher) close() error {
	if w.pending {
		_ = windows.CancelIoEx(w.dir, w.ov)
		// 等取消完成再释放缓冲区（内核可能还在写）；最多等 2 秒。
		deadline := time.Now().Add(2 * time.Second)
		for w.pending && time.Now().Before(deadline) {
			var n uint32
			var key uintptr
			var ov *windows.Overlapped
			_ = windows.GetQueuedCompletionStatus(w.port, &n, &key, &ov, 100)
			if ov != nil {
				w.pending = false
			}
		}
	}
	err := windows.CloseHandle(w.dir)
	if e := windows.CloseHandle(w.port); err == nil {
		err = e
	}
	if w.pending {
		// 取消一直没完成：故意不释放 buf / ov 的引用（泄漏 64 KiB 胜过让内核写已回收的内存）。
		keepAlive = append(keepAlive, w.buf)
	}
	return err
}

var keepAlive [][]byte
