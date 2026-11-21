//go:build linux

package wsfs

// Linux 监视器：inotify 逐目录监视，fd 非阻塞，Snapshot 里读到 EAGAIN 为止（不起 goroutine）。
// inotify 事件在写文件的系统调用里同步入队。只在本地文件系统上用：9p / drvfs（WSL 的 /mnt/c）、
// NFS、overlay 等收不到另一侧的改动，退回全量。

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const inMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MODIFY | unix.IN_ATTRIB | unix.IN_CLOSE_WRITE |
	unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_ONLYDIR | unix.IN_DONT_FOLLOW

// 这些事件说明这个名字可能被整个换掉了（新建、删除、移入移出）。
const inReplaced = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO

// localFS inotify 可靠的本地文件系统（statfs f_type）。
var localFS = map[uint32]bool{
	0xef53:                 true, // ext2 / ext3 / ext4
	unix.XFS_SUPER_MAGIC:   true,
	unix.BTRFS_SUPER_MAGIC: true,
	unix.TMPFS_MAGIC:       true,
	unix.F2FS_SUPER_MAGIC:  true,
	0x2fc12fc1:             true, // zfs
}

type inoWatcher struct {
	fd  int
	wds map[int32]string // 监视号 → 目录（相对 root）
	buf []byte
}

func localDir(p string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		return err
	}
	if !localFS[uint32(st.Type)] {
		return errNoWatch
	}
	return nil
}

func openWatcher(root string) (watcher, error) {
	if err := localDir(root); err != nil {
		return nil, err
	}
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &inoWatcher{fd: fd, wds: map[int32]string{}, buf: make([]byte, 64<<10)}, nil
}

func (w *inoWatcher) recursive() bool { return false }

// add 监视一个目录。目录已经没了（读目录前被删）不算错：上一级的监视会报告它。
// 子目录挂的是别的文件系统（如 ext4 里绑定挂载了 9p）、监视数用完：报错，调用方之后一直全量。
func (w *inoWatcher) add(abs, rel string) error {
	if err := localDir(abs); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return nil
		}
		return err
	}
	wd, err := unix.InotifyAddWatch(w.fd, abs, inMask)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EACCES) {
			return nil
		}
		return err
	}
	w.wds[int32(wd)] = rel // 同一个目录（改名后）再加返回同一个监视号：更新路径
	return nil
}

func (w *inoWatcher) drain() ([]change, bool, error) {
	var out []change
	over := false
	for {
		n, err := unix.Read(w.fd, w.buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || err == nil && n <= 0 {
			return out, over, nil
		}
		if err != nil {
			return nil, false, err
		}
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&w.buf[off]))
			end := off + unix.SizeofInotifyEvent + int(ev.Len)
			if end > n {
				return nil, false, errors.New("inotify 事件不完整")
			}
			name := strings.TrimRight(string(w.buf[off+unix.SizeofInotifyEvent:end]), "\x00")
			off = end
			if ev.Mask&unix.IN_Q_OVERFLOW != 0 {
				over = true
				continue
			}
			rel, ok := w.wds[ev.Wd]
			if !ok {
				continue
			}
			if ev.Mask&unix.IN_IGNORED != 0 { // 监视被内核撤掉（目录删了 / 卸载）
				delete(w.wds, ev.Wd)
				out = append(out, change{path: rel, replaced: true})
				continue
			}
			if name == "" { // 目录自身的事件
				out = append(out, change{path: rel, replaced: ev.Mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0})
				continue
			}
			out = append(out, change{path: join(rel, name), replaced: ev.Mask&inReplaced != 0})
		}
	}
}

func (w *inoWatcher) close() error { return unix.Close(w.fd) }
