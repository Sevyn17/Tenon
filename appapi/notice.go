package appapi

// 提示送达（客户端改进 M10 T19）：notice 带级别与来源，进程保留最近 wire.NoticeKeep 条，连接级推送（不依赖会话订阅），
// 连接第一次 hello 之后补发它建立之前的那些。
//
// 记录与发布在同一处（noticeLog.publish）：先编号记下，再发到总线。连接先挂总线订阅、再读当前编号作为分界：
// 编号不超过分界的从记录里补发（总线上收到的副本丢掉），超过的走实时，两边不重不漏。
// 装配期插件经 UI.Warn 发的告警早于 App 构造，所以记录放在 UI 上，App 与它共用一份（没有协议界面时 App 自己建一份）。

import (
	"strings"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/bus"
)

// CapNotices 能力 notices：notice 连接级送达、带 level / source，hello 后补发最近的提示（wire.Notice 的说明）。
const CapNotices = "notices"

// noticeSeqKey 总线 notice 事件 data 里的进程内编号（不上线）。没有编号的（别处直接发到总线的）一律按实时推送。
const noticeSeqKey = "nseq"

type noticeRec struct {
	seq     int64
	session string
	at      int64
	n       wire.Notice
}

// noticeLog 最近的提示。零值可用；方法并发安全。
type noticeLog struct {
	mu   sync.Mutex
	seq  int64
	recs []noticeRec
}

// publish 编号记下一条提示并发到总线。level 空按 info。
func (l *noticeLog) publish(b *bus.Bus, session, level, source, text string) {
	if level == "" {
		level = wire.NoticeInfo
	}
	at := time.Now().UnixMilli()
	l.mu.Lock()
	l.seq++
	seq := l.seq
	l.recs = append(l.recs, noticeRec{seq: seq, session: session, at: at, n: wire.Notice{Level: level, Source: source, Text: text}})
	if n := len(l.recs); n > wire.NoticeKeep {
		l.recs = append([]noticeRec(nil), l.recs[n-wire.NoticeKeep:]...)
	}
	l.mu.Unlock()
	b.Publish(bus.Envelope{Session: session, At: at, Kind: EventNotice,
		Data: map[string]any{"text": text, "level": level, "source": source, noticeSeqKey: seq}})
}

// last 当前最大编号（连接的分界）。
func (l *noticeLog) last() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// upto 编号不超过 floor 的记录（按编号升序，最多 NoticeKeep 条）。
func (l *noticeLog) upto(floor int64) []noticeRec {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []noticeRec
	for _, r := range l.recs {
		if r.seq <= floor {
			out = append(out, r)
		}
	}
	return out
}

// NoticeFrom 发一条带级别与来源的提示（level 取 wire.NoticeInfo / NoticeWarn / NoticeError，空 = info；
// sessionID 空 = 全局提示）。
func (a *App) NoticeFrom(sessionID, level, source, text string) {
	a.notices.publish(a.o.Bus, sessionID, level, source, text)
}

// modeNoticeLevel 档位变化提示的级别：读取失败、档位已不存在是 warn，配置更新是 info。
// Modes.Notice 只给文本（接口不改），按 permission 插件的固定措辞判断。
func modeNoticeLevel(text string) string {
	if strings.Contains(text, "失败") || strings.Contains(text, "不存在") {
		return wire.NoticeWarn
	}
	return wire.NoticeInfo
}
