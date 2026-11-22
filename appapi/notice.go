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
	l.publishKey(b, session, level, source, "", nil, text)
}

// publishKey 同 publish，另带稳定提示键与参数（wire.Notice.Key / Args，供产品翻译）。
func (l *noticeLog) publishKey(b *bus.Bus, session, level, source, key string, args map[string]string, text string) {
	if level == "" {
		level = wire.NoticeInfo
	}
	at := time.Now().UnixMilli()
	l.mu.Lock()
	l.seq++
	seq := l.seq
	l.recs = append(l.recs, noticeRec{seq: seq, session: session, at: at, n: wire.Notice{Level: level, Source: source, Text: text, Key: key, Args: args}})
	if n := len(l.recs); n > wire.NoticeKeep {
		l.recs = append([]noticeRec(nil), l.recs[n-wire.NoticeKeep:]...)
	}
	l.mu.Unlock()
	b.Publish(bus.Envelope{Session: session, At: at, Kind: EventNotice,
		Data: map[string]any{"text": text, "level": level, "source": source, "key": key, "args": args, noticeSeqKey: seq}})
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

// NoticeKey 同 NoticeFrom，另带稳定提示键 key 与参数 args（wire.Notice.Key / Args）：产品按键翻译，
// text 是英文兜底。
func (a *App) NoticeKey(sessionID, level, source, key string, args map[string]string, text string) {
	a.notices.publishKey(a.o.Bus, sessionID, level, source, key, args, text)
}

// ModeNoticer Modes 的可选扩展：档位变化提示带稳定键与参数（wire.Notice.Key / Args）。没实现时只用 Modes.Notice 的文本。
type ModeNoticer interface {
	NoticeOf(session string) (key string, args map[string]string, text string)
}

// 档位变化提示的稳定键（与 permission 插件一致）。
const (
	NoticeKeyModeReadFailed = "permission.mode_read_failed"
	NoticeKeyModeGone       = "permission.mode_gone"
	NoticeKeyModeUpdated    = "permission.mode_updated"
)

// 其他 appapi 自己发的提示的稳定键。参数 error 为原始错误文本。
const (
	NoticeKeyRulesSaveFailed    = "permission.rules_save_failed"
	NoticeKeySettingsSaveFailed = "settings.save_failed"
	NoticeKeyReloadReverted     = "workspace.reload_reverted"
	NoticeKeyReadyFailed        = "ready.failed"
)

// modeNoticeLevel 档位变化提示的级别：读取失败、档位已不存在是 warn，配置更新是 info。
// 有键按键判断；没有键（旧实现只给文本）时按英文措辞判断。
func modeNoticeLevel(key, text string) string {
	switch key {
	case NoticeKeyModeReadFailed, NoticeKeyModeGone:
		return wire.NoticeWarn
	case NoticeKeyModeUpdated:
		return wire.NoticeInfo
	}
	if strings.Contains(text, "failed") || strings.Contains(text, "no longer exists") {
		return wire.NoticeWarn
	}
	return wire.NoticeInfo
}
