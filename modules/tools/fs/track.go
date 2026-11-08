package fs

// 读后写（2026-11-04 审查 P1-43）：覆盖或编辑一个已存在的文件之前，本会话必须读过它，且读过之后
// 文件没被别人改过。否则模型可能覆盖它从没看过的内容，或在用户改过之后用旧内容写回。
// 新建文件不受限制。按会话记录每个文件最后一次读 / 写时的修改时间与大小。

import (
	"fmt"
	"os"
	"sync"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// TrackMaxFiles 每个会话记住的文件数上限；超出时整表清空重来（之后的写需要重新读一次）。
const TrackMaxFiles = 4096

type stamp struct {
	mod  int64
	size int64
}

// Tracker 记录各会话读过 / 写过的文件状态。零值可用；nil 表示不检查。
type Tracker struct {
	mu   sync.Mutex
	seen map[string]map[string]stamp // 会话 → 绝对路径 → 状态
}

func stampOf(p string) (stamp, bool) {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return stamp{}, false
	}
	return stamp{mod: st.ModTime().UnixNano(), size: st.Size()}, true
}

// Note 记下本会话看到的文件当前状态（读完、写完后调用）。
func (t *Tracker) Note(cx *toolhub.Cx, p string) {
	if t == nil {
		return
	}
	s, ok := stampOf(p)
	if !ok {
		return
	}
	sid := sessionOf(cx)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[string]map[string]stamp{}
	}
	m := t.seen[sid]
	if m == nil || len(m) >= TrackMaxFiles {
		m = map[string]stamp{}
		t.seen[sid] = m
	}
	m[p] = s
}

// Check 写一个文件之前校验：文件不存在（新建）通过；存在时必须本会话读过且之后没被改过。
func (t *Tracker) Check(cx *toolhub.Cx, p, display string) error {
	if t == nil {
		return nil
	}
	cur, exists := stampOf(p)
	if !exists {
		return nil
	}
	t.mu.Lock()
	prev, ok := t.seen[sessionOf(cx)][p]
	t.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s 已存在但本会话还没读过：先用 read_file 读一遍再改（避免覆盖没看过的内容）", display)
	}
	if prev != cur {
		return fmt.Errorf("%s 在上次读取之后被改过（可能是用户或别的程序）：先用 read_file 重新读，再在最新内容上修改", display)
	}
	return nil
}

func sessionOf(cx *toolhub.Cx) string {
	if cx == nil {
		return ""
	}
	return cx.SessionID
}
