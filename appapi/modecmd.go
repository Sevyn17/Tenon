package appapi

// 权限档位命令（§二 mode.*）。需 Options.Modes；未装权限插件返回 unsupported。

import (
	"context"

	"ybase/runtime/host"
)

// ModeArgs mode.get / mode.set 参数。Mode 为空 = 只查询；Confirm 用于确认放宽权限的项目档位。
type ModeArgs struct {
	Session string `json:"session"`
	Mode    string `json:"mode,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

// ShowArgs mode.show 参数：Mode 为空取 Session 的当前档位。
type ShowArgs struct {
	Session string `json:"session,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

func (a *App) modes() (Modes, error) {
	if a.o.Modes == nil {
		return nil, Errorf(CodeUnsupported, "未装权限插件")
	}
	return a.o.Modes, nil
}

// ModeList 全部档位。
func (a *App) ModeList(_ context.Context, _ NoArgs) ([]ModeInfo, error) {
	m, err := a.modes()
	if err != nil {
		return nil, err
	}
	out := m.List()
	if out == nil {
		out = []ModeInfo{}
	}
	return out, nil
}

// ModeGet 会话当前档位。
func (a *App) ModeGet(_ context.Context, in ModeArgs) (ModeState, error) {
	m, err := a.modes()
	if err != nil {
		return ModeState{}, err
	}
	if err := need(in.Session); err != nil {
		return ModeState{}, err
	}
	return m.Get(in.Session), nil
}

// ModeSet 切换会话档位：内存立即生效，持久化到顶层会话头异步进行（回合中 SetExt 排到步边界），
// 失败发 notice。Mode 为空等同 mode.get。
func (a *App) ModeSet(ctx context.Context, in ModeArgs) (ModeState, error) {
	m, err := a.modes()
	if err != nil {
		return ModeState{}, err
	}
	if in.Mode == "" {
		return a.ModeGet(ctx, in)
	}
	hd, err := a.handle(ctx, in.Session)
	if err != nil {
		return ModeState{}, err
	}
	st, err := m.Set(in.Session, in.Mode, in.Confirm)
	if err != nil {
		return ModeState{}, wrap(err)
	}
	top := hd
	if t := topOf(in.Session); t != in.Session {
		if g, ok := a.o.Host.Get(t); ok {
			top = g
		}
	}
	key, _ := m.ExtKeys()
	go func() {
		if err := top.Do(host.Command{Kind: host.SetExt, Key: key, Value: st.Mode}); err != nil {
			a.Notice(in.Session, "[档位未能保存："+err.Error()+"]")
		}
	}()
	return st, nil
}

// ModeShow 档位的完整取值表。
func (a *App) ModeShow(_ context.Context, in ShowArgs) ([]ModeItem, error) {
	m, err := a.modes()
	if err != nil {
		return nil, err
	}
	if in.Session == "" && in.Mode == "" {
		return nil, Errorf(CodeBadRequest, "缺少 session 或 mode")
	}
	out, err := m.Show(in.Session, in.Mode)
	if err != nil {
		return nil, wrap(err)
	}
	if out == nil {
		out = []ModeItem{}
	}
	return out, nil
}
