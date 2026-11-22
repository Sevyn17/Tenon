package std

// 权限档位：permission 插件 → appapi.Modes。

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/Sevyn17/tenon/adapters"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/modules/security/permission"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// PermModes 用 permission 插件的会话服务实现 appapi.Modes。Approvals 为 nil 时不记确认：每次选用放宽了的项目档位都要确认。
type PermModes struct {
	P         *permission.Sessions
	Approvals adapters.ModeApprovals
}

var _ appapi.Modes = PermModes{}

func (m PermModes) List() []appapi.ModeInfo {
	items := m.P.Items()
	var out []appapi.ModeInfo
	for _, d := range m.P.Registry().List() {
		out = append(out, appapi.ModeInfo{ID: string(d.ID), Label: permission.Label(d, items), Short: permission.Short(d), Builtin: d.Builtin,
			Project: d.Project, Widened: permission.Widened(d, items), Description: d.Description})
	}
	return out
}

// DefaultMode 新会话的缺省档位（appapi.ModeDefaulter）：permissions.toml 的 mode 或强制档位，
// 没确认过的放宽项目档位退回内置缺省档位。
func (m PermModes) DefaultMode() string { return string(m.P.DefaultMode()) }

var _ appapi.ModeDefaulter = PermModes{}

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
		_, own := d.File.Items[it.ID]
		row := appapi.ModeItem{Item: it.ID, Title: it.Title, Value: val, Source: src,
			Bounded: it.Type != toolhub.ItemMatch || it.Bounded,
			Class:   it.Class, Type: string(it.Type), Owner: it.Owner, Tools: m.P.ItemTools(it.ID), Overridden: own}
		switch it.Type {
		case toolhub.ItemMatch:
			row.Allow, row.Ask, row.Deny = clone(s.Allow), clone(s.Ask), clone(s.Deny)
		case toolhub.ItemLevel:
			row.Levels = clone(it.Levels)
		}
		out = append(out, row)
	}
	return out, nil
}

func clone(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}
	return append([]string(nil), xs...)
}

// —— 界面里编辑档位（appapi.ModeEditor，docs/51 第 6 节）——

var _ appapi.ModeEditor = PermModes{}

func (m PermModes) Editable() bool { return m.P.Editable() }

func (m PermModes) Detail(mode string) (appapi.ModeDetail, error) {
	d, ok := m.P.Registry().Get(permission.Mode(mode))
	if !ok {
		return appapi.ModeDetail{}, appapi.Errorf(appapi.CodeNotFound, "没有档位 %q", mode)
	}
	return appapi.ModeDetail{Name: d.Title, Base: d.File.Base, Description: d.Description, Builtin: d.Builtin, Project: d.Project,
		Editable: m.P.UserEditable(d.ID), Revision: m.P.ModeRevision(d.ID)}, nil
}

func (m PermModes) info(id permission.Mode) appapi.ModeInfo {
	d, ok := m.P.Registry().Get(id)
	if !ok {
		return appapi.ModeInfo{ID: string(id), Label: string(id)}
	}
	items := m.P.Items()
	return appapi.ModeInfo{ID: string(d.ID), Label: permission.Label(d, items), Short: permission.Short(d), Builtin: d.Builtin,
		Project: d.Project, Widened: permission.Widened(d, items), Description: d.Description}
}

func (m PermModes) SaveMode(in wire.ModeDraft, revision string, create bool) (appapi.ModeInfo, string, error) {
	e := permission.ModeEdit{ID: permission.Mode(in.ID), Name: in.Name, Base: in.Base, Description: in.Description,
		Items: map[string]permission.ItemFile{}}
	for k, v := range in.Items {
		e.Items[k] = permission.ItemFile{Default: v.Default, Level: v.Level, Allow: v.Allow, Ask: v.Ask, Deny: v.Deny}
	}
	rev, err := m.P.SaveMode(e, revision, create)
	if err != nil {
		return appapi.ModeInfo{}, "", editError(err)
	}
	return m.info(e.ID), rev, nil
}

func (m PermModes) RemoveMode(id, revision string) (string, bool, error) {
	fb, err := m.P.RemoveMode(permission.Mode(id), revision)
	if err != nil {
		return "", false, editError(err)
	}
	return m.DefaultMode(), fb, nil
}

func (m PermModes) SetDefaultMode(id string) (string, string, error) {
	pinned, err := m.P.SetDefaultMode(permission.Mode(id))
	if err != nil {
		return "", "", editError(err)
	}
	return m.DefaultMode(), pinned, nil
}

// editError permission.EditError → 协议错误码。
func editError(err error) error {
	var ee *permission.EditError
	if !errors.As(err, &ee) {
		return err
	}
	switch ee.Kind {
	case permission.EditNotFound:
		return appapi.Errorf(appapi.CodeNotFound, "%s", ee.Msg)
	case permission.EditUnsupported:
		return appapi.Errorf(appapi.CodeUnsupported, "%s", ee.Msg)
	case permission.EditStale, permission.EditExists:
		reason := wire.ConflictStale
		if ee.Kind == permission.EditExists {
			reason = wire.ConflictExists
		}
		e := appapi.Errorf(appapi.CodeConflict, "%s", ee.Msg)
		e.Details = map[string]any{"reason": reason, "revision": ee.Revision}
		return e
	}
	return appapi.Errorf(wire.CodeInvalid, "%s", ee.Msg)
}

func (m PermModes) Notice(id string) string { return m.P.Notice(id) }

// NoticeOf 档位变化提示带稳定键（appapi.ModeNoticer）。
func (m PermModes) NoticeOf(id string) (string, map[string]string, string) {
	n := m.P.NoticeOf(id)
	return n.Key, n.Args, n.Text
}

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
