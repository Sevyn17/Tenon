// Package mail：信封（框架设计 11 信封表 / 13 会话与消息机制）。
//
// 信封是会话之间、应用与组件之间投递消息的统一格式。内核字段（from、origin、
// caps、corr、trace、hop、budget_ref 等）由内核在投递时写入，见 11 的表格；
// 调用方只填得到 to / kind / mode / key / body 这类字段。
//
// 本包只做数据与渲染，不做投递与守卫（那在 core/host 与守卫层）。
package mail

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 地址前缀（11 11.3）。
const (
	AddrSession   = "session"
	AddrAgent     = "agent"
	AddrHuman     = "human"
	AddrComponent = "component"
)

// Address 是一个已解析的地址：Kind + ID。
type Address struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ParseAddress 解析 "session:<id>" / "agent:<名字>" / "human:<用户>" / "component:<组件>"。
func ParseAddress(raw string) (Address, error) {
	kind, id, ok := strings.Cut(raw, ":")
	if !ok || kind == "" || id == "" {
		return Address{}, fmt.Errorf("mail: 地址 %q 不合法（应为 <类型>:<标识>）", raw)
	}
	switch kind {
	case AddrSession, AddrAgent, AddrHuman, AddrComponent:
		return Address{Kind: kind, ID: id}, nil
	default:
		return Address{}, fmt.Errorf("mail: 未知地址类型 %q", kind)
	}
}

// String 还原成 "kind:id"。
func (a Address) String() string {
	if a.Kind == "" {
		return a.ID
	}
	return a.Kind + ":" + a.ID
}

// Kind 信封类型（11 信封表）。
type Kind string

const (
	KindRequest Kind = "request" // 请求：通常期待一封 reply
	KindReply   Kind = "reply"   // 回信：带 corr 回到请求会话
	KindNotify  Kind = "notify"  // 通知：不期待回复
	KindControl Kind = "control" // 控制：中断、关闭
)

// Mode 处理方式（11 / 13 13.3）。
type Mode string

const (
	ModeWake    Mode = "wake"    // 唤醒：空闲开回合，运行中下次调模型前插入
	ModeContext Mode = "context" // 背景：不开回合，等下一次 wake 一起进模型
	ModeNotice  Mode = "notice"  // 通知：进下一次状态更新
	ModeControl Mode = "control" // 控制：内核处理，不进模型
)

// DefaultMode 缺省处理方式：按 kind 推出（11 信封表 mode 行）。
func DefaultMode(k Kind) Mode {
	switch k {
	case KindReply, KindRequest:
		return ModeWake
	case KindNotify:
		return ModeNotice
	case KindControl:
		return ModeControl
	default:
		return ModeWake
	}
}

// Origin 来源类型（11 信封表 origin 行）：由入口决定，沿链只降不升。
type Origin string

const (
	OriginHuman    Origin = "human"    // 应用鉴权后的用户
	OriginAgent    Origin = "agent"    // 智能体
	OriginExternal Origin = "external" // 定时、webhook、事件
)

// Trusted 报告该来源是否可信（external 内容按不可信处理）。
func (o Origin) Trusted() bool { return o != OriginExternal }

// Envelope 一封信封。内核字段由投递方写入，见 11 的信封表。
type Envelope struct {
	ID        string    `json:"id"`
	From      Address   `json:"from"`
	To        Address   `json:"to"`
	Kind      Kind      `json:"kind"`
	Mode      Mode      `json:"mode"`
	Key       string    `json:"key,omitempty"` // 会话键，只对 agent: 有效
	Corr      string    `json:"corr,omitempty"`
	Trace     string    `json:"trace,omitempty"`
	Hop       int       `json:"hop,omitempty"`
	Origin    Origin    `json:"origin"`
	Caps      []string  `json:"caps,omitempty"`
	BudgetRef string    `json:"budget_ref,omitempty"`
	Body      string    `json:"body"`
	Deadline  int64     `json:"deadline,omitempty"` // Unix 毫秒，0 = 无期限
	At        time.Time `json:"at,omitempty"`
}

// ErrInvalid 信封不合法。
var ErrInvalid = errors.New("mail: 信封不合法")

// Normalize 补齐内核可缺省字段（id、mode、at、origin），并校验必填项。
// 已在信封里显式给出的 mode 保留（调用方或守卫可以指定、降级）。
func (e *Envelope) Normalize() error {
	if e.To.Kind == "" || e.To.ID == "" {
		return fmt.Errorf("%w: 缺 to", ErrInvalid)
	}
	if e.From.Kind == "" || e.From.ID == "" {
		return fmt.Errorf("%w: 缺 from", ErrInvalid)
	}
	if e.Kind == "" {
		e.Kind = KindRequest
	}
	switch e.Kind {
	case KindRequest, KindReply, KindNotify, KindControl:
	default:
		return fmt.Errorf("%w: 未知 kind %q", ErrInvalid, e.Kind)
	}
	if e.Mode == "" {
		e.Mode = DefaultMode(e.Kind)
	}
	switch e.Mode {
	case ModeWake, ModeContext, ModeNotice, ModeControl:
	default:
		return fmt.Errorf("%w: 未知 mode %q", ErrInvalid, e.Mode)
	}
	if e.Kind == KindControl && e.Mode != ModeControl {
		return fmt.Errorf("%w: control 信封的 mode 只能是 control", ErrInvalid)
	}
	if e.Mode == ModeControl && e.Kind != KindControl {
		return fmt.Errorf("%w: mode=control 的信封 kind 只能是 control", ErrInvalid)
	}
	if e.Kind == KindReply && e.Corr == "" {
		return fmt.Errorf("%w: 回信缺 corr", ErrInvalid)
	}
	if e.Origin == "" {
		e.Origin = OriginHuman
	}
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	return nil
}

// NewID 生成不可猜的 128 位随机 id（hex）。
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Subject 权限主体（13 13.3 合并规则：同一触发用户 + 同一 budget_ref）。
func (e Envelope) Subject() string {
	return e.From.ID + "\x00" + e.BudgetRef
}

// Render 把一批信封渲染成一条用户消息（13 13.3 合并格式）：
//
//	<msg from="李四" at="18:20">这周进度怎么样</msg>
//
// body 里的尖括号与 & 会被转义，避免伪造合并标签。
func Render(envs []Envelope) string {
	var b strings.Builder
	for i, e := range envs {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, `<msg from=%q at=%q`, EscapeAttr(e.From.ID), e.At.Format("15:04"))
		if e.To.Kind == AddrSession && e.To.ID != "" {
			// 群聊/多人场景里标明"对我说"，单聊省略。
		}
		b.WriteString(">")
		b.WriteString(EscapeText(e.Body))
		b.WriteString("</msg>")
	}
	return b.String()
}

// EscapeAttr 转义 XML 属性值（" & < >）。
var attrEsc = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")

// EscapeText 转义正文里的标签起始符，防止伪造 <msg> 等合并标签。
var textEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// EscapeAttr 转义属性值。
func EscapeAttr(s string) string { return attrEsc.Replace(s) }

// EscapeText 转义正文。
func EscapeText(s string) string { return textEsc.Replace(s) }
