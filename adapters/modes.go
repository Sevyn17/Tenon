package adapters

// 权限档位：permission 插件 → appapi.Modes。

import (
	"encoding/json"
	"strings"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/modules/security/permission"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// ModeApprovals 项目档位放宽权限的确认记录：记在哪由应用决定（apphost 记在 ~/.tenon/trust.toml，按项目）。
// widened 是放宽清单（顿号连接）；清单变了要重新确认。
type ModeApprovals interface {
	Approved(mode, widened string) bool
	Approve(mode, widened string) error
}

// PermModes 用 permission 插件的会话服务实现 appapi.Modes。Approvals 为 nil 时不记确认：每次选用放宽了的项目档位都要确认。
type PermModes struct {
	P         *permission.Sessions
	Approvals ModeApprovals
}

var _ appapi.Modes = PermModes{}

func (m PermModes) List() []appapi.ModeInfo {
	items := m.P.Items()
	var out []appapi.ModeInfo
	for _, d := range m.P.Registry().List() {
		out = append(out, appapi.ModeInfo{ID: string(d.ID), Label: permission.Label(d, items), Builtin: d.Builtin,
			Project: d.Project, Widened: permission.Widened(d, items)})
	}
	return out
}

func (m PermModes) approved(mode, widened string) bool {
	return m.Approvals != nil && m.Approvals.Approved(mode, widened)
}

func (m PermModes) label(mode permission.Mode) string {
	if d, ok := m.P.Registry().Get(mode); ok {
		return permission.Label(d, m.P.Items())
	}
	return string(mode)
}

func (m PermModes) Get(id string) appapi.ModeState {
	mode := m.P.Mode(id)
	return appapi.ModeState{Mode: string(mode), Label: m.label(mode), Sandbox: string(m.P.SandboxMode(id))}
}

func (m PermModes) Set(id, mode string, confirm bool) (appapi.ModeState, error) {
	d, ok := m.P.Registry().Get(permission.Mode(mode))
	if !ok {
		return appapi.ModeState{}, appapi.Errorf(appapi.CodeNotFound, "没有档位 %q", mode)
	}
	// §9.6：受信项目提交的档位若放宽了权限，首次选用要用户确认一次（记进 trust.toml，放宽清单变了要重新确认）。
	if d.Project {
		if w := strings.Join(permission.ProjectWidened(d, m.P.Items()), "、"); w != "" && !m.approved(mode, w) {
			if !confirm {
				return appapi.ModeState{}, appapi.Errorf(appapi.CodeNeedConfirm, "档位 %s 来自项目 .tenon/modes，放宽了：%s", mode, w)
			}
			if m.Approvals != nil {
				if err := m.Approvals.Approve(mode, w); err != nil {
					return appapi.ModeState{}, err
				}
			}
		}
	}
	if err := m.P.SetMode(id, permission.Mode(mode)); err != nil {
		return appapi.ModeState{}, err
	}
	return m.Get(id), nil
}

func (m PermModes) Show(id, mode string) ([]appapi.ModeItem, error) {
	mm := permission.Mode(mode)
	if mode == "" {
		mm = m.P.Mode(id)
	}
	d, ok := m.P.Registry().Get(mm)
	if !ok {
		return nil, appapi.Errorf(appapi.CodeNotFound, "没有档位 %q", mm)
	}
	var out []appapi.ModeItem
	for _, it := range m.P.Items() {
		s, src := permission.ItemValue(d, it)
		val := string(s.Default)
		if it.Type == toolhub.ItemLevel {
			val = s.Level
		}
		out = append(out, appapi.ModeItem{Item: it.ID, Title: it.Title, Value: val, Source: src,
			Bounded: it.Type != toolhub.ItemMatch || it.Bounded})
	}
	return out, nil
}

func (m PermModes) Notice(id string) string         { return m.P.Notice(id) }
func (m PermModes) SessionRules(id string) []string { return m.P.SessionRules(id) }

func (m PermModes) SessionAllow(tool string, args json.RawMessage) (rules, blocked []string) {
	rs, blocked := m.P.SessionAllow(tool, args)
	for _, r := range rs {
		rules = append(rules, r.String())
	}
	return rules, blocked
}

func (m PermModes) AllowForSession(id, tool string, args json.RawMessage) (added, all []string) {
	rs, _ := m.P.SessionAllow(tool, args)
	for _, r := range rs {
		all = m.P.AllowForSession(id, r)
		added = append(added, r.String())
	}
	if all == nil { // 没生成规则：持久化时保留已有规则
		all = m.P.SessionRules(id)
	}
	return added, all
}

func (PermModes) ExtKeys() (mode, rules string) { return permission.ExtMode, permission.ExtRules }
