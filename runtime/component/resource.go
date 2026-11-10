package component

// 资源类型与订阅匹配器（框架设计 04 4.7，T4c）。
//
// 资源记录存在组件自己的 Collection 里，内核不存、也不负责发现"有新记录"。
// 组件写完一条记录后调用 Mailer.Fanout，框架按订阅跑匹配器，命中的走现有投递。
// 匹配器是纯函数：只看这一条记录和本订阅的参数，不读存储、不调模型。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/Sevyn17/tenon/runtime/mail"
)

// ResourceNameMax 资源类型名与匹配器名的最大长度（约束清单「资源与订阅」）。
// 字符集是小写字母、数字、下划线，与集合名同一规则；地址类型不能带冒号。
const ResourceNameMax = 32

// ResourceType 一种资源（04 4.7）。记录存在登记它的组件自己的集合里。
type ResourceType struct {
	Name string // 地址类型，如 "group"；资源地址是 "<Name>:<id>"
	// Render 把一条记录渲染成信封正文。nil 时用记录的 Body。
	Render func(ResourceRecord) string
}

// ResourceRecord 一条资源记录。序号由组件排，内核不分配。
type ResourceRecord struct {
	Resource string // 资源地址，如 "group:g1"
	Seq      int64  // 该资源内的序号，从 1 起
	From     mail.Address
	Body     string
	// Fields 匹配器能看到的字段。缺省含 "from"（地址字符串）与 "body"。
	Fields map[string]string
}

// Match 一次匹配的结论。Matched 为 false 时不投。
type Match struct {
	Matched bool
	Mode    mail.Mode // 空 = notice
}

// Matcher 订阅匹配器。只看这一条记录和本订阅的参数，不读存储、不调模型。
type Matcher func(rec ResourceRecord, params map[string]string) (Match, error)

// matcherReg 一个已登记的匹配器，带上登记它的组件。内置的 owner 为空。
type matcherReg struct {
	owner string
	fn    Matcher
}

// ResourceType 登记一种资源类型（04 4.7）。同名（跨组件）重复登记拒绝。
func (cx *Registrar) ResourceType(t ResourceType) error {
	if err := checkResourceName("资源类型", t.Name); err != nil {
		return fmt.Errorf("插件 %s：%w", cx.plugin, err)
	}
	if _, dup := cx.asm.resources[t.Name]; dup {
		return fmt.Errorf("插件 %s：资源类型 %s 重复登记", cx.plugin, t.Name)
	}
	cx.asm.resources[t.Name] = t
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "resource_type", Name: t.Name})
	return nil
}

// Matcher 登记一个匹配器（04 4.7）。同名重复登记拒绝，包括与内置的 exact、prefix、list 重名。
func (cx *Registrar) Matcher(name string, m Matcher) error {
	if err := checkResourceName("匹配器", name); err != nil {
		return fmt.Errorf("插件 %s：%w", cx.plugin, err)
	}
	if m == nil {
		return fmt.Errorf("插件 %s：匹配器 %s 缺函数", cx.plugin, name)
	}
	if _, dup := cx.asm.matchers[name]; dup {
		return fmt.Errorf("插件 %s：匹配器 %s 重复登记", cx.plugin, name)
	}
	cx.asm.matchers[name] = matcherReg{owner: cx.plugin, fn: m}
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "matcher", Name: name})
	return nil
}

// ResourceTypes 已登记的资源类型，按名字排序。
func (a *Assembly) ResourceTypes() []ResourceType {
	out := make([]ResourceType, 0, len(a.resources))
	for _, n := range sortedKeys(a.resources) {
		out = append(out, a.resources[n])
	}
	return out
}

// Matcher 按名字取匹配器。没有返回 false。
func (a *Assembly) Matcher(name string) (Matcher, bool) {
	m, ok := a.matchers[name]
	return m.fn, ok
}

// checkResourceName 校验资源类型名与匹配器名。
func checkResourceName(what, name string) error {
	if !nameOK(name) || len(name) > ResourceNameMax {
		return fmt.Errorf("%s %q 不合法（小写字母、数字、下划线，最长 %d）", what, name, ResourceNameMax)
	}
	return nil
}

// nameOK 小写字母、数字、下划线，且非空。
func nameOK(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// sortedKeys 按名字排序，结果确定。
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 内置匹配器（04 4.7）：只做程序规则。模型判断类归 T5c，不进这个签名。

const (
	MatcherExact  = "exact"  // Fields[field] == value
	MatcherPrefix = "prefix" // Fields[field] 以 value 开头
	MatcherList   = "list"   // Fields[field] 等于 value 里逗号分隔的某一项
)

// installBuiltinMatchers 在组件 Setup 之前占住三个名字，组件同名登记会被拒。
func (a *Assembly) installBuiltinMatchers() {
	a.matchers[MatcherExact] = matcherReg{fn: matchExact}
	a.matchers[MatcherPrefix] = matcherReg{fn: matchPrefix}
	a.matchers[MatcherList] = matcherReg{fn: matchList}
}

// matchParams 取出 field 与 value。缺任一返回错误，该订阅本条不投。
func matchParams(name string, params map[string]string) (field, value string, err error) {
	field, value = params["field"], params["value"]
	if field == "" || value == "" {
		return "", "", fmt.Errorf("匹配器 %s 缺 field 或 value", name)
	}
	return field, value, nil
}

func matchExact(rec ResourceRecord, params map[string]string) (Match, error) {
	field, value, err := matchParams(MatcherExact, params)
	if err != nil {
		return Match{}, err
	}
	return Match{Matched: rec.Fields[field] == value}, nil
}

func matchPrefix(rec ResourceRecord, params map[string]string) (Match, error) {
	field, value, err := matchParams(MatcherPrefix, params)
	if err != nil {
		return Match{}, err
	}
	return Match{Matched: strings.HasPrefix(rec.Fields[field], value)}, nil
}

func matchList(rec ResourceRecord, params map[string]string) (Match, error) {
	field, value, err := matchParams(MatcherList, params)
	if err != nil {
		return Match{}, err
	}
	for _, item := range splitList(value) {
		if rec.Fields[field] == item {
			return Match{Matched: true}, nil
		}
	}
	return Match{}, nil
}

// Subscription 一条资源订阅。由持有资源的组件从自己的集合查出来传给 Fanout，
// 内核不存、也不查组件的集合（07 执行方案 3.2）。
type Subscription struct {
	ID         string            // 订阅 id，组件自己分配
	Resource   string            // 资源地址，如 "group:g1"
	Subscriber string            // 收信地址，本步只接受 "session:<id>"
	Matcher    string            // 已登记的匹配器名
	Params     map[string]string // 匹配器参数
	Exclude    string            // 可选，命中则跳过的来源地址，逗号分隔
}

// FanoutResult 一次扇出的结果。错误的订阅记在这里，不作为 Fanout 的返回错误。
type FanoutResult struct {
	Delivered int      // 投出的信封数
	Skipped   int      // 未命中或来源被排除的订阅数
	Denied    []string // 投递服务返回错误的订阅 id（联系名单、守卫、跳数，也含会话不存在）
	Errors    []string // 订阅本身不合法（地址不是 session:、匹配器未登记）或匹配器报错、返回非法 mode，格式 "<订阅 id>: <原因>"
}

// Fanout 把一条资源记录按订阅投给会话（04 4.7）。
//
// subs 是该资源的订阅，由调用方从自己的集合查好传进来：内核不查组件的集合，
// 也不负责发现"有新记录"。写记录和调用本方法之间崩溃，由组件启动时按自己的序号补投。
//
// 返回错误只表示根本没投成，例如没有投递服务。单条订阅的问题记日志、跳过，
// 收进 FanoutResult，不挡住其余订阅。
func (m *Mailer) Fanout(ctx context.Context, rec ResourceRecord, subs []Subscription) (FanoutResult, error) {
	if m.host == nil {
		return FanoutResult{}, fmt.Errorf("plugin: 没有内核投递服务，无法扇出 %s", rec.Resource)
	}
	var out FanoutResult
	for _, sub := range subs {
		switch err := m.fanoutOne(ctx, rec, sub); {
		case err == nil:
			out.Delivered++
		case err == errFanoutSkip:
			out.Skipped++
		default:
			var denied deniedError
			if errors.As(err, &denied) {
				out.Denied = append(out.Denied, denied.id)
				log.Printf("component: 组件 %s 扇出 %s#%d 给订阅 %s 被拒绝：%v", m.from.ID, rec.Resource, rec.Seq, sub.ID, err)
				continue
			}
			out.Errors = append(out.Errors, sub.ID+": "+err.Error())
			log.Printf("component: 组件 %s 扇出 %s#%d 给订阅 %s 失败：%v", m.from.ID, rec.Resource, rec.Seq, sub.ID, err)
		}
	}
	return out, nil
}

// errFanoutSkip 表示这条订阅本条不投，且不是错误：未命中或来源被排除。
// 地址不是 session: 是订阅本身的错误，不走这里。
var errFanoutSkip = fmt.Errorf("跳过")

// fanoutOne 处理一条订阅。返回 nil 表示已投出。
func (m *Mailer) fanoutOne(ctx context.Context, rec ResourceRecord, sub Subscription) error {
	if excluded(rec.From, sub.Exclude) {
		return errFanoutSkip
	}
	to, err := mail.ParseAddress(sub.Subscriber)
	if err != nil || to.Kind != mail.AddrSession {
		return fmt.Errorf("收信地址 %q 不是 session:", sub.Subscriber)
	}
	match, err := m.runMatcher(rec, sub)
	if err != nil || !match.Matched {
		if err != nil {
			return err
		}
		return errFanoutSkip
	}
	if !validMatchMode(match.Mode) {
		return fmt.Errorf("匹配器 %s 返回的 mode %q 不合法（只能是 wake、context、notice 或空）", sub.Matcher, match.Mode)
	}
	env := mail.Envelope{
		From:   rec.From,
		Via:    m.from.ID,
		To:     to,
		Kind:   mail.KindNotify,
		Mode:   match.Mode,
		Origin: mail.OriginExternal,
		Body:   renderRecord(rec, m.resourceType(rec.Resource)),
	}
	if err := env.Normalize(); err != nil {
		return err
	}
	if err := m.host.Deliver(ctx, env); err != nil {
		return deniedError{id: sub.ID, err: err}
	}
	return nil
}

// validMatchMode 匹配器能给的 mode：空（= notice）、wake、context、notice。
// control 不行：扇出信封是 notify，不能控制会话。
func validMatchMode(m mail.Mode) bool {
	switch m {
	case "", mail.ModeWake, mail.ModeContext, mail.ModeNotice:
		return true
	}
	return false
}

// deniedError 投递服务返回了错误（联系名单、守卫、跳数，也含会话不存在）。
// Fanout 把它收进 FanoutResult.Denied，不与订阅本身的错误混在一起。
type deniedError struct {
	id  string
	err error
}

func (e deniedError) Error() string { return e.err.Error() }

func (e deniedError) Unwrap() error { return e.err }

// runMatcher 跑订阅指定的匹配器。没有登记的匹配器是订阅本身的错误。
func (m *Mailer) runMatcher(rec ResourceRecord, sub Subscription) (Match, error) {
	reg, ok := m.matchers[sub.Matcher]
	if !ok {
		return Match{}, fmt.Errorf("匹配器 %q 未登记", sub.Matcher)
	}
	return reg.fn(withDefaultFields(rec), sub.Params)
}

// withDefaultFields 补上缺省字段：from 是来源地址字符串，body 是记录正文。
// 组件显式给的同名字段优先。不改调用方的 map。
func withDefaultFields(rec ResourceRecord) ResourceRecord {
	fields := map[string]string{"from": rec.From.String(), "body": rec.Body}
	for k, v := range rec.Fields {
		fields[k] = v
	}
	rec.Fields = fields
	return rec
}

// resourceType 按资源地址的类型名取登记。没登记返回零值，渲染时退回记录正文。
func (m *Mailer) resourceType(resource string) ResourceType {
	name, _, _ := strings.Cut(resource, ":")
	if m.asm == nil {
		return ResourceType{}
	}
	return m.asm.resources[name]
}

// renderRecord 用资源类型的 Render 出正文；没有登记或没给 Render 时用记录自己的正文。
func renderRecord(rec ResourceRecord, t ResourceType) string {
	if t.Render == nil {
		return rec.Body
	}
	return t.Render(rec)
}

// excluded 看来源地址是否在订阅的排除名单里。名单是逗号分隔的地址字符串，空名单不排除。
func excluded(from mail.Address, list string) bool {
	want := from.String()
	for _, item := range splitList(list) {
		if item == want {
			return true
		}
	}
	return false
}

// splitList 拆逗号分隔的名单：每项去掉首尾空白，丢掉空项。
// "human:u1, human:u2" 与 "human:u1,human:u2" 等价。
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
