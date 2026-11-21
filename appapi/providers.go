package appapi

// 自定义模型「厂商 → 密钥 → 模型」的协议命令（Mortise docs/51 第 4 节）：models.providers.list / save / remove /
// setModelEnabled / test / fetchModels。由 Platform 的可选扩展 ModelProviders 实现（与 BYOKTester 同一模式）；
// 没实现时全部 unsupported，hello 不报能力 providers。
//
// 写命令的规则参照 mcp.save：revision 并发校验（实现方比对，不一致返回 StaleRevision 的 conflict）；密钥值不回显，
// 保存时 value 为 null = 保留原值。改动影响模型目录（实现方在 ProvidersWrite.Reload 里说明）时重建运行时：
// 先写、再判断有没有回合在跑——有且没带 force 就调 Undo 改回文件，返回 not_idle；重建失败也调 Undo。
// 全部命令只给 operator（列表里有配置文件路径与密钥掩码，和 platform.byok.list 一样）。

import (
	"context"
	"errors"
	"strings"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// CapProviders hello 能力：models.providers.* 可用。
const CapProviders = "providers"

// ModelProviders Platform 的可选扩展：按厂商分组管理自定义模型。
// 实现方保证结果、错误、日志里没有明文密钥。错误是 *Error 时原样交给客户端（invalid / not_found / conflict）。
type ModelProviders interface {
	ProvidersList(ctx context.Context) (wire.ModelProvidersListResult, error)
	// ProvidersSave 按草稿新增或修改一个厂商；revision 非空时先比对。
	ProvidersSave(ctx context.Context, d wire.ModelProviderDraft, revision string) (ProvidersWrite, error)
	// ProvidersRemove 删掉一个厂商的全部配置。
	ProvidersRemove(ctx context.Context, provider, revision string) (ProvidersWrite, error)
	// ProvidersSetModelEnabled 启停一个模型。
	ProvidersSetModelEnabled(ctx context.Context, in wire.ModelProvidersSetModelEnabledArgs) (ProvidersWrite, error)
	// ProvidersTest 测连接。连不通不是错误（OK = false）。
	ProvidersTest(ctx context.Context, in wire.ModelProvidersTestArgs) (wire.BYOKTestResult, error)
	// ProvidersFetch 拉厂商的模型列表。拉不到不是错误（OK = false，带原因）。
	ProvidersFetch(ctx context.Context, in wire.ModelProvidersFetchArgs) (wire.ModelProvidersFetchResult, error)
}

// ProvidersWrite 一次写入的结果。Reload = 改动影响模型目录，要重建运行时；Undo 把文件改回写入前（nil = 没写）。
type ProvidersWrite struct {
	Revision string
	Provider *wire.ModelProviderInfo
	Reload   bool
	Undo     func() error
}

// StaleRevision 配置文件已被改过（revision 对不上）：conflict，details.reason = stale，details.revision = 当前版本。
func StaleRevision(current string) *Error {
	e := Errorf(wire.CodeConflict, "模型配置已被别处改过，请刷新后重试")
	e.Details = map[string]any{"reason": wire.ConflictStale, "revision": current}
	return e
}

func (v *App) providers() (ModelProviders, error) {
	if v.o.Platform != nil {
		if m, ok := v.o.Platform.(ModelProviders); ok {
			return m, nil
		}
	}
	return nil, Errorf(wire.CodeUnsupported, "这个应用不支持按厂商管理自定义模型")
}

// ModelProvidersList 见 wire.ModelProvidersListResult。
func (v *App) ModelProvidersList(ctx context.Context, _ wire.Empty) (wire.ModelProvidersListResult, error) {
	m, err := v.providers()
	if err != nil {
		return wire.ModelProvidersListResult{}, err
	}
	r, err := m.ProvidersList(ctx)
	if err != nil {
		return wire.ModelProvidersListResult{}, AsError(err)
	}
	if r.Kinds == nil {
		r.Kinds = []wire.ProviderKind{}
	}
	if r.Providers == nil {
		r.Providers = []wire.ModelProviderInfo{}
	}
	for i := range r.Providers {
		normProvider(&r.Providers[i])
	}
	return r, nil
}

func normProvider(p *wire.ModelProviderInfo) {
	if p == nil {
		return
	}
	if p.Keys == nil {
		p.Keys = []wire.ProviderKey{}
	}
	if p.Models == nil {
		p.Models = []wire.ProviderModel{}
	}
}

// ModelProvidersSave 见 wire.ModelProvidersSaveArgs。
func (v *App) ModelProvidersSave(ctx context.Context, in wire.ModelProvidersSaveArgs) (wire.ModelProvidersEditResult, error) {
	m, err := v.providers()
	if err != nil {
		return wire.ModelProvidersEditResult{}, err
	}
	in.Provider.ID = strings.TrimSpace(in.Provider.ID)
	in.Provider.Kind = strings.TrimSpace(in.Provider.Kind)
	if in.Provider.ID == "" && in.Provider.Kind == "" {
		return wire.ModelProvidersEditResult{}, invalid("要给 provider.id 或 provider.kind")
	}
	return v.providersApply(ctx, in.Force, func() (ProvidersWrite, error) { return m.ProvidersSave(ctx, in.Provider, in.Revision) })
}

// ModelProvidersRemove 见 wire.ModelProvidersRemoveArgs。
func (v *App) ModelProvidersRemove(ctx context.Context, in wire.ModelProvidersRemoveArgs) (wire.ModelProvidersEditResult, error) {
	m, err := v.providers()
	if err != nil {
		return wire.ModelProvidersEditResult{}, err
	}
	id := strings.TrimSpace(in.Provider)
	if id == "" {
		return wire.ModelProvidersEditResult{}, invalid("缺少 provider")
	}
	return v.providersApply(ctx, in.Force, func() (ProvidersWrite, error) { return m.ProvidersRemove(ctx, id, in.Revision) })
}

// ModelProvidersSetModelEnabled 见 wire.ModelProvidersSetModelEnabledArgs。
func (v *App) ModelProvidersSetModelEnabled(ctx context.Context, in wire.ModelProvidersSetModelEnabledArgs) (wire.ModelProvidersEditResult, error) {
	m, err := v.providers()
	if err != nil {
		return wire.ModelProvidersEditResult{}, err
	}
	in.Provider, in.ID, in.Name = strings.TrimSpace(in.Provider), strings.TrimSpace(in.ID), strings.TrimSpace(in.Name)
	if in.Provider == "" || (in.ID == "" && in.Name == "") {
		return wire.ModelProvidersEditResult{}, invalid("要给 provider 与模型的 id 或 name")
	}
	return v.providersApply(ctx, in.Force, func() (ProvidersWrite, error) { return m.ProvidersSetModelEnabled(ctx, in) })
}

// ModelProvidersTest 见 wire.ModelProvidersTestArgs。
func (v *App) ModelProvidersTest(ctx context.Context, in wire.ModelProvidersTestArgs) (wire.BYOKTestResult, error) {
	m, err := v.providers()
	if err != nil {
		return wire.BYOKTestResult{}, err
	}
	if strings.TrimSpace(in.Provider) == "" && strings.TrimSpace(in.Kind) == "" {
		return wire.BYOKTestResult{}, invalid("要给 provider 或 kind")
	}
	r, err := m.ProvidersTest(ctx, in)
	if err != nil {
		return wire.BYOKTestResult{}, AsError(err)
	}
	return r, nil
}

// ModelProvidersFetchModels 见 wire.ModelProvidersFetchArgs。
func (v *App) ModelProvidersFetchModels(ctx context.Context, in wire.ModelProvidersFetchArgs) (wire.ModelProvidersFetchResult, error) {
	m, err := v.providers()
	if err != nil {
		return wire.ModelProvidersFetchResult{}, err
	}
	if strings.TrimSpace(in.Provider) == "" && strings.TrimSpace(in.Kind) == "" {
		return wire.ModelProvidersFetchResult{}, invalid("要给 provider 或 kind")
	}
	r, err := m.ProvidersFetch(ctx, in)
	if err != nil {
		return wire.ModelProvidersFetchResult{}, AsError(err)
	}
	if r.Models == nil {
		r.Models = []string{}
	}
	normProvider(r.Provider)
	return r, nil
}

// providersApply 写入 → 需要时重建。有回合在跑且没带 force：改回文件，返回 not_idle；带 force 先取消再重建。
func (v *App) providersApply(ctx context.Context, force bool, write func() (ProvidersWrite, error)) (wire.ModelProvidersEditResult, error) {
	w, err := write()
	if err != nil {
		return wire.ModelProvidersEditResult{}, AsError(err)
	}
	normProvider(w.Provider)
	out := wire.ModelProvidersEditResult{Revision: w.Revision, Provider: w.Provider, Workspace: v.currentWorkspace()}
	rl, ok := v.o.Workspaces.(Reloader)
	if !w.Reload || v.o.Workspaces == nil || !ok {
		return out, nil // CLI 没有工作区管理器：下次运行生效
	}
	undo := func(e *Error) *Error {
		if w.Undo != nil {
			if err := w.Undo(); err != nil {
				e.Message += "；模型配置也没能改回：" + err.Error()
			}
		}
		return e
	}
	ids, err := v.runningIDs(ctx)
	if err != nil {
		return wire.ModelProvidersEditResult{}, undo(AsError(err))
	}
	if len(ids) > 0 {
		if !force {
			e := Errorf(wire.CodeNotIdle, "还有 %d 个会话正在运行：改模型配置要重建运行时，会中断它们（带 force 先取消再重建）", len(ids))
			e.Details = map[string]any{"running": len(ids), "sessions": ids}
			return wire.ModelProvidersEditResult{}, undo(e)
		}
		v.cancelAll(ctx, ids)
	}
	ws, err := rl.Reload(ctx)
	if err != nil {
		var rv *ReloadReverted
		if errors.As(err, &rv) {
			e := Errorf(wire.CodeInvalid, "%s", rv.Error())
			e.Details = map[string]any{"reverted": true}
			return wire.ModelProvidersEditResult{}, undo(e)
		}
		return wire.ModelProvidersEditResult{}, undo(Errorf(wire.CodeInternal, "重建运行时失败：%v", err))
	}
	out.Reloaded, out.Workspace = true, ws
	return out, nil
}
