package appapi

// 在界面里编辑档位（Mortise docs/51 第 6 节）：mode.save / mode.remove / mode.setDefault。
// 实现在 Modes 的可选扩展 ModeEditor（adapters.PermModes）；没实现或不可编辑时返回 unsupported，hello 不报 mode.edit。
// 三条命令都写用户级文件，只给 operator（不在 viewerCommands 里）。

import (
	"context"
	"strings"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// 本文件的命令（按文件登记，v2.go 的 registerCommands）。
func init() {
	registerCommands(map[string]handler{
		"mode.save":       bind((*App).ModeSave),
		"mode.remove":     bind((*App).ModeRemove),
		"mode.setDefault": bind((*App).ModeSetDefault),
	})
}

func (v *App) modeEditor() (ModeEditor, error) {
	m, err := v.modes()
	if err != nil {
		return nil, err
	}
	ed, ok := m.(ModeEditor)
	if !ok || !ed.Editable() {
		return nil, Errorf(wire.CodeUnsupported, "这个应用不支持在界面里编辑档位（直接改 modes/ 与 permissions.toml）")
	}
	return ed, nil
}

// modeDetail 给 mode.show 的结果补上档位本身的信息（Modes 没实现 ModeEditor 时不补）。
func (v *App) modeDetail(m Modes, out *wire.ModeShowResult) {
	ed, ok := m.(ModeEditor)
	if !ok {
		return
	}
	d, err := ed.Detail(out.Mode)
	if err != nil {
		return
	}
	out.Name, out.Base, out.Description = d.Name, d.Base, d.Description
	out.Builtin, out.Project, out.Revision = d.Builtin, d.Project, d.Revision
	out.Editable = d.Editable && ed.Editable()
}

func wireModeInfo(d ModeInfo) wire.ModeInfo {
	return wire.ModeInfo{ID: d.ID, Label: d.Label, Short: d.Short, Builtin: d.Builtin, Project: d.Project, Widened: d.Widened,
		Description: d.Description}
}

// ModeSave 见 wire.ModeSaveArgs。
func (v *App) ModeSave(_ context.Context, in wire.ModeSaveArgs) (wire.ModeSaveResult, error) {
	ed, err := v.modeEditor()
	if err != nil {
		return wire.ModeSaveResult{}, err
	}
	in.Mode.ID = strings.TrimSpace(in.Mode.ID)
	if in.Mode.ID == "" {
		return wire.ModeSaveResult{}, Errorf(wire.CodeBadRequest, "缺少 mode.id")
	}
	info, rev, err := ed.SaveMode(in.Mode, in.Revision, in.Create)
	if err != nil {
		return wire.ModeSaveResult{}, AsError(err)
	}
	return wire.ModeSaveResult{Mode: wireModeInfo(info), Revision: rev}, nil
}

// ModeRemove 见 wire.ModeRemoveArgs。
func (v *App) ModeRemove(_ context.Context, in wire.ModeRemoveArgs) (wire.ModeRemoveResult, error) {
	ed, err := v.modeEditor()
	if err != nil {
		return wire.ModeRemoveResult{}, err
	}
	if in.ID == "" {
		return wire.ModeRemoveResult{}, Errorf(wire.CodeBadRequest, "缺少 id")
	}
	def, fb, err := ed.RemoveMode(in.ID, in.Revision)
	if err != nil {
		return wire.ModeRemoveResult{}, AsError(err)
	}
	out := wire.ModeRemoveResult{ID: in.ID, Default: def, FellBack: fb}
	if fb {
		out.Note = "删掉的是新会话默认档位，默认已改回 " + def
	}
	return out, nil
}

// ModeSetDefault 见 wire.ModeSetDefaultArgs。
func (v *App) ModeSetDefault(_ context.Context, in wire.ModeSetDefaultArgs) (wire.ModeSetDefaultResult, error) {
	ed, err := v.modeEditor()
	if err != nil {
		return wire.ModeSetDefaultResult{}, err
	}
	if in.ID == "" {
		return wire.ModeSetDefaultResult{}, Errorf(wire.CodeBadRequest, "缺少 id")
	}
	def, pinned, err := ed.SetDefaultMode(in.ID)
	if err != nil {
		return wire.ModeSetDefaultResult{}, AsError(err)
	}
	return wire.ModeSetDefaultResult{Default: def, Pinned: pinned}, nil
}
