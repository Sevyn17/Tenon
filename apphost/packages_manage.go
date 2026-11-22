package apphost

// 插件包管理（客户端改进 M11 配置T22，spike-config 第六节）：WorkspaceManager 实现 appapi.PackageManager。
// 磁盘操作在 adapters/plugins（Stage / Install / Remove / SetEnabled，目录规则本来就在那里）；这里补上
// 预览要的展示字段（命令行、钩子、技能、定义、配置项、风险、与已安装版本的比较）和协议错误码。重建由 appapi 调 Reload。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/adapters/plugins"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/pkgs"
	"github.com/Sevyn17/tenon/runtime/store"
)

var _ appapi.PackageManager = (*WorkspaceManager)(nil)

var pkgMu sync.Mutex // 同一进程里改用户级插件包目录串行

// pkgErr adapters/plugins 的错误类别 → 协议错误码。
func pkgErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, plugins.ErrInvalid):
		return appapi.Errorf(wire.CodeInvalid, "%s", strings.TrimPrefix(err.Error(), plugins.ErrInvalid.Error()+"："))
	case errors.Is(err, plugins.ErrNotFound):
		return appapi.Errorf(wire.CodeNotFound, "%s", strings.TrimPrefix(err.Error(), plugins.ErrNotFound.Error()+"："))
	}
	return err
}

func pkgHome() (string, error) {
	h := Home()
	if h == "" {
		return "", errors.New("取不到用户目录，无法管理插件包")
	}
	return h, nil
}

// UserPackages 见 appapi.PackageManager。
func (m *WorkspaceManager) UserPackages(context.Context) ([]wire.PackageInfo, error) {
	home, err := pkgHome()
	if err != nil {
		return nil, err
	}
	us, err := plugins.ScanUser(home)
	if err != nil {
		return nil, err
	}
	out := make([]wire.PackageInfo, 0, len(us))
	for _, u := range us {
		pi := wire.PackageInfo{Name: u.Name, Scope: pkgs.ScopeUser, Enabled: u.Enabled, Dir: u.Dir,
			Source: filepath.Join(u.Dir, pkgs.ManifestName), Risks: []string{}}
		if u.Package != nil {
			pi.Version, pi.Description, pi.Contents, pi.Risks = u.Package.Version, u.Package.Description, u.Package.Contents(), u.Package.Risks()
		} else {
			pi.Contents = "（清单不可用）"
			pi.Error = u.Err.Error()
		}
		out = append(out, pi)
	}
	return out, nil
}

// PreviewPackage 见 appapi.PackageManager。
func (m *WorkspaceManager) PreviewPackage(_ context.Context, source string) (wire.PackagesPreviewResult, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	home, err := pkgHome()
	if err != nil {
		return wire.PackagesPreviewResult{}, err
	}
	var prof *component.Profile
	if cur := m.Current(); cur != nil && cur.Runtime != nil {
		prof = cur.Runtime.Profile
	}
	return previewPackage(home, source, prof)
}

// PreviewPackage 只读解析来源（本地目录或 .zip），返回安装确认要的内容（packages.preview 与 CLI 共用）。
// prof 非空时据它提示「负责的插件没开」。来源或清单不合法时返回 *appapi.Error（invalid）。
func PreviewPackage(source string, prof *component.Profile) (wire.PackagesPreviewResult, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	home, err := pkgHome()
	if err != nil {
		return wire.PackagesPreviewResult{}, err
	}
	return previewPackage(home, source, prof)
}

func previewPackage(home, source string, prof *component.Profile) (wire.PackagesPreviewResult, error) {
	s, err := plugins.Stage(home, source)
	if err != nil {
		return wire.PackagesPreviewResult{}, pkgErr(err)
	}
	defer s.Close()
	return describe(home, s.Package, prof), nil
}

// describe 安装确认框要的内容。p 是 staging 里的副本；命令行里的 ${TENON_PLUGIN_DIR} 按装好后的目录展开。
func describe(home string, p *pkgs.Package, prof *component.Profile) wire.PackagesPreviewResult {
	dir := filepath.Join(plugins.UserDir(home), p.Name)
	out := wire.PackagesPreviewResult{Name: p.Name, Version: p.Version, Description: p.Description,
		Contents: p.Contents(), Dir: dir, Risks: p.Risks(), UICommands: p.UICommands()}
	note := func(s string) { out.Notes = append(out.Notes, s) }
	src := "package:" + p.Name
	var f previewFile
	if _, err := toml.DecodeFile(p.File, &f); err == nil {
		for _, s := range f.Server {
			var args []string
			for _, a := range s.Args {
				args = append(args, pkgs.Expand(a, dir))
			}
			out.MCPServers = append(out.MCPServers, wire.TrustMCPServer{Name: s.Name, Command: pkgs.Expand(s.Command, dir), Args: args, URL: s.URL, Source: src})
		}
		for _, h := range f.Hook {
			out.Hooks = append(out.Hooks, wire.TrustHook{Event: h.Event, Matcher: h.Matcher, Command: pkgs.Expand(h.Command, dir), Source: src})
		}
	}
	if p.SkillsDir != "" {
		out.Skills = skillNames(p.SkillsDir)
	}
	for _, f := range p.Prompts {
		if r, err := filepath.Rel(p.Dir, f); err == nil {
			out.Prompts = append(out.Prompts, filepath.ToSlash(r))
		}
	}
	if p.AgentsDir != "" {
		refs, err := agentdef.FS(os.DirFS(p.AgentsDir), p.AgentsDir).List(context.Background(), agentdef.Scope{Tenant: store.Tenant})
		if err != nil {
			note("读定义目录失败：" + err.Error())
		}
		for _, r := range refs {
			out.Agents = append(out.Agents, r.String())
		}
	}
	names := make([]string, 0, len(p.Config))
	for n := range p.Config {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		denied := map[string]bool{}
		for _, k := range deniedKeys(n) {
			denied[k] = true
		}
		keys := make([]string, 0, len(p.Config[n]))
		for k := range p.Config[n] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out.Config = append(out.Config, wire.PackageConfig{Plugin: n, Key: k, Value: p.Config[n][k], Ignored: denied[k]})
			if denied[k] {
				note("插件包不能设 " + n + "." + k + "（端点、凭据、存储位置），加载时会忽略")
			}
		}
	}
	for _, u := range p.UISlots() {
		ws := wire.UISlot{ID: u.ID, Slot: u.Slot, Title: u.Title, Order: u.Order, Requires: u.Requires, View: u.View}
		if u.Data != nil {
			ws.Data = &wire.UIData{Cmd: u.Data.Cmd, Args: u.Data.Args}
		}
		out.UI = append(out.UI, ws)
	}
	if prof != nil {
		out.Notes = append(out.Notes, PackageNotices(prof, []*pkgs.Package{p})...)
	}
	if us, err := plugins.ScanUser(home); err == nil {
		for _, u := range us {
			if u.Name != p.Name {
				continue
			}
			in := &wire.PackageInstalled{Enabled: u.Enabled, Compare: wire.VersionUnknown}
			if u.Package != nil {
				in.Version = u.Package.Version
				if c, ok := plugins.CompareVersions(p.Version, in.Version); ok {
					in.Compare = map[int]string{1: wire.VersionNewer, 0: wire.VersionSame, -1: wire.VersionOlder}[c]
				}
			} else if u.Err != nil {
				in.Error = u.Err.Error()
			}
			out.Installed = in
			note("已安装同名包：安装后旧包挪进 " + filepath.Join(plugins.UserDir(home), plugins.TrashDir) + "，可手工挪回恢复")
		}
	}
	note("本地插件包没有签名，来源由你自己判断")
	return out
}

// skillNames 技能目录里的技能名：<名字>/SKILL.md 或 <名字>.md（同 ~/.tenon/skills 的格式）。
func skillNames(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		switch {
		case strings.HasPrefix(n, "."):
		case e.IsDir():
			if _, err := os.Stat(filepath.Join(dir, n, "SKILL.md")); err == nil {
				out = append(out, n)
			}
		case strings.HasSuffix(n, ".md"):
			out = append(out, strings.TrimSuffix(n, ".md"))
		}
	}
	sort.Strings(out)
	return out
}

// InstallPackage 见 appapi.PackageManager。
func (m *WorkspaceManager) InstallPackage(_ context.Context, source string) (appapi.PackageWrite, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	home, err := pkgHome()
	if err != nil {
		return appapi.PackageWrite{}, err
	}
	s, err := plugins.Stage(home, source)
	if err != nil {
		return appapi.PackageWrite{}, pkgErr(err)
	}
	defer s.Close()
	ch, err := plugins.Install(home, s)
	if err != nil {
		return appapi.PackageWrite{}, pkgErr(err)
	}
	return appapi.PackageWrite{Name: ch.Name, Trashed: ch.Trashed, Changed: true, Undo: lockedUndo(ch.Undo)}, nil
}

// RemovePackage 见 appapi.PackageManager。只动用户级包：当前加载的同名包来自项目时 invalid。
func (m *WorkspaceManager) RemovePackage(_ context.Context, name string) (appapi.PackageWrite, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	home, err := pkgHome()
	if err != nil {
		return appapi.PackageWrite{}, err
	}
	if err := m.userOnly(home, name); err != nil {
		return appapi.PackageWrite{}, err
	}
	ch, err := plugins.Remove(home, name)
	if err != nil {
		return appapi.PackageWrite{}, pkgErr(err)
	}
	return appapi.PackageWrite{Name: ch.Name, Trashed: ch.Trashed, Changed: true, Undo: lockedUndo(ch.Undo)}, nil
}

// SetPackageEnabled 见 appapi.PackageManager。
func (m *WorkspaceManager) SetPackageEnabled(_ context.Context, name string, enabled bool) (appapi.PackageWrite, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	home, err := pkgHome()
	if err != nil {
		return appapi.PackageWrite{}, err
	}
	if err := m.userOnly(home, name); err != nil {
		return appapi.PackageWrite{}, err
	}
	ch, changed, err := plugins.SetEnabled(home, name, enabled)
	if err != nil {
		return appapi.PackageWrite{}, pkgErr(err)
	}
	return appapi.PackageWrite{Name: ch.Name, Changed: changed, Undo: lockedUndo(ch.Undo)}, nil
}

// userOnly 用户级目录里没有、而当前加载了同名项目包时报 invalid（项目包只读）；其余交给 adapters/plugins 判断。
func (m *WorkspaceManager) userOnly(home, name string) error {
	if _, err := os.Stat(filepath.Join(plugins.UserDir(home), name)); err == nil {
		return nil
	}
	if cur := m.Current(); cur != nil && cur.Runtime != nil {
		for _, p := range cur.Runtime.Packages {
			if p.Name == name && p.Scope == pkgs.ScopeProject {
				return appapi.Errorf(wire.CodeInvalid, "插件包 %s 来自项目（%s），只读：去项目目录里改", name, p.Dir)
			}
		}
	}
	return nil
}

func lockedUndo(f func() error) func() error {
	if f == nil {
		return nil
	}
	return func() error {
		pkgMu.Lock()
		defer pkgMu.Unlock()
		return f()
	}
}
