package component

// 资源类型与订阅匹配器（框架设计 04 4.7，T4c）。
//
// 资源记录存在组件自己的 Collection 里，内核不存、也不负责发现"有新记录"。
// 组件写完一条记录后调用 Mailer.Fanout，框架按订阅跑匹配器，命中的走现有投递。
// 匹配器是纯函数：只看这一条记录和本订阅的参数，不读存储、不调模型。

import (
	"fmt"
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
	for _, item := range strings.Split(value, ",") {
		if rec.Fields[field] == item {
			return Match{Matched: true}, nil
		}
	}
	return Match{}, nil
}
