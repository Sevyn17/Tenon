package apphost

// 界面里信任项目（客户端改进 M11 配置T13，spike-config 第四节）：WorkspaceManager 实现 appapi.Truster。
// TrustPreview 按「受信」口径只读地读一遍项目配置（mcp.toml、hooks.toml、permissions.toml、modes、插件包、
// profile.toml），不启动任何东西；SetTrust 写 trust.toml（SaveTrust）。重建由 appapi 调 Reload。
// 受信后项目 mcp.toml 直接生效（docs/50 §5.1）；项目钩子在受信时由 hooks 插件加载（hooks 插件开着时）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Sevyn17/tenon/adapters/profile"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/modules/security/permission"
	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/pkgs"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

var _ appapi.Truster = (*WorkspaceManager)(nil)

// TrustPreview 见 appapi.Truster。
func (m *WorkspaceManager) TrustPreview(context.Context) (wire.WorkspaceTrustPreviewResult, error) {
	cur := m.Current()
	if cur == nil {
		return wire.WorkspaceTrustPreviewResult{}, ErrNoWorkspace
	}
	var items []toolhub.Item
	if cur.Built != nil && cur.Built.Perms != nil {
		items = cur.Built.Perms.Items()
	}
	f := m.o.Flags
	out := previewTrust(cur.Runtime.Root, f.BaseProfile, f.Profile, items)
	out.Trusted, out.Why = cur.Runtime.Trusted, cur.Runtime.TrustWhy
	out.Forced = f.TrustProject || f.ProjectHooks
	return out, nil
}

// SetTrust 见 appapi.Truster：在当前工作区的信任键上写记录（level 为空 = 删除），按新记录算重建后是否受信。
func (m *WorkspaceManager) SetTrust(_ context.Context, level string) (appapi.TrustChange, error) {
	cur := m.Current()
	if cur == nil {
		return appapi.TrustChange{}, ErrNoWorkspace
	}
	root := cur.Runtime.Root
	key := ProjectKey(root)
	recs, err := LoadTrust()
	if err != nil {
		return appapi.TrustChange{}, err
	}
	ch := appapi.TrustChange{Key: key, Prev: recs[key], Before: cur.Runtime.Trusted}
	if err := SaveTrust(key, level); err != nil {
		return ch, err
	}
	// 和 startWith 同一口径：网页端、桌面端不问（ask 为 nil），无记录按不受信。
	after, _, err := ResolveTrust(m.o.Flags.TrustProject || m.o.Flags.ProjectHooks, root, nil)
	if err != nil {
		return ch, errors.Join(err, SaveTrust(key, ch.Prev))
	}
	ch.After = after
	return ch, nil
}

// previewServer / previewHook mcp.toml 的 [[server]]、hooks.toml 的 [[hook]] 里预览要的字段（插件包清单同形）。
// 只读解析，不 import mcp / hooks 插件（它们的校验在装配时做；这里解析失败记成提示）。
type previewServer struct {
	Name    string   `toml:"name"`
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	URL     string   `toml:"url"`
}

type previewHook struct {
	Event   string `toml:"event"`
	Matcher string `toml:"matcher"`
	Command string `toml:"command"`
}

type previewFile struct {
	Server []previewServer `toml:"server"`
	Hook   []previewHook   `toml:"hook"`
	Mode   string          `toml:"mode"`
}

// decodeIfExists 文件存在就解码；不存在返回 false、nil。
func decodeIfExists(path string, v any) (bool, error) {
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return false, nil
	}
	_, err := toml.DecodeFile(path, v)
	return true, err
}

// previewTrust 按受信口径读 root 的项目配置。错误都记进 Notes，不让预览失败。
func previewTrust(root, base, extra string, items []toolhub.Item) wire.WorkspaceTrustPreviewResult {
	key := ProjectKey(root)
	out := wire.WorkspaceTrustPreviewResult{Root: root, Key: key}
	note := func(s string) { out.Notes = append(out.Notes, s) }
	if recs, err := LoadTrust(); err != nil {
		note(err.Error())
	} else {
		out.Level = LookupTrust(recs, key)
	}
	dir := appid.Project(root)
	rel := func(p string) string {
		if r, err := filepath.Rel(root, p); err == nil {
			return filepath.ToSlash(r)
		}
		return p
	}

	// 配置文件
	for _, n := range []string{"profile.toml", "permissions.toml", "hooks.toml", "mcp.toml"} {
		path := filepath.Join(dir, n)
		var f previewFile
		ok, err := decodeIfExists(path, &f)
		if !ok {
			continue
		}
		out.Files = append(out.Files, rel(path))
		if err != nil {
			note("读 " + rel(path) + "：" + err.Error())
			continue
		}
		switch n {
		case "mcp.toml":
			for _, s := range f.Server {
				out.MCPServers = append(out.MCPServers, wire.TrustMCPServer{Name: s.Name, Command: s.Command, Args: s.Args, URL: s.URL, Source: "project"})
			}
		case "hooks.toml":
			for _, h := range f.Hook {
				out.Hooks = append(out.Hooks, wire.TrustHook{Event: h.Event, Matcher: h.Matcher, Command: h.Command, Source: "project"})
			}
		case "permissions.toml":
			out.DefaultMode = f.Mode
		case "profile.toml":
			out.Plugins = previewProfile(path, note)
		}
	}

	// 项目档位（modes/*.toml）：只列放宽了权限的
	modesDir := filepath.Join(dir, "modes")
	if ents, err := os.ReadDir(modesDir); err == nil {
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
				out.Files = append(out.Files, rel(filepath.Join(modesDir, e.Name())))
			}
		}
		reg, err := permission.NewRegistry(permission.ModeDirs(root)...)
		if err != nil {
			note("读项目档位：" + err.Error())
		} else {
			for _, d := range reg.List() {
				if !d.Project {
					continue
				}
				w, exact := previewWidened(d, items)
				if len(w) == 0 {
					continue
				}
				// 确认记录按 ProjectWidened 的清单比对；权限项注册表还没有时清单不全，不报「已确认」
				ok := exact && ModeApproved(root, string(d.ID), strings.Join(w, "、"))
				out.WidenedModes = append(out.WidenedModes, wire.TrustMode{ID: string(d.ID), Title: d.Title, Widened: w, Approved: ok})
			}
		}
	}

	// 按受信口径空跑一遍 profile 与插件包：读不出来的话信任后重建会失败（会回到原来的配置）
	p, ps, notes, err := LoadAll(base, extra, root, true)
	if err != nil {
		note("按受信读配置会失败，信任后重建不会生效：" + err.Error())
	} else {
		for _, n := range notes {
			if strings.Contains(n, "项目") || strings.Contains(n, dir) {
				note(n)
			}
		}
		for _, k := range ps {
			if k.Scope != pkgs.ScopeProject {
				continue
			}
			out.Files = append(out.Files, rel(k.Dir)+"/")
			out.Packages = append(out.Packages, wire.TrustPackage{Name: k.Name, Version: k.Version, Description: k.Description, Dir: k.Dir})
			var f previewFile
			if _, err := toml.DecodeFile(k.File, &f); err != nil {
				note("读 " + rel(k.File) + "：" + err.Error())
				continue
			}
			src := "package:" + k.Name
			for _, s := range f.Server {
				args := make([]string, len(s.Args))
				for i, a := range s.Args {
					args[i] = pkgs.Expand(a, k.Dir)
				}
				if len(args) == 0 {
					args = nil
				}
				out.MCPServers = append(out.MCPServers, wire.TrustMCPServer{Name: s.Name, Command: pkgs.Expand(s.Command, k.Dir), Args: args, URL: s.URL, Source: src})
			}
			for _, h := range f.Hook {
				out.Hooks = append(out.Hooks, wire.TrustHook{Event: h.Event, Matcher: h.Matcher, Command: pkgs.Expand(h.Command, k.Dir), Source: src})
			}
		}
		on := map[string]bool{}
		for _, st := range p.States() {
			on[st.Name] = st.On
		}
		if len(out.MCPServers) > 0 && !on["mcp"] {
			note("mcp 插件没开：信任后项目 MCP 服务也不会启动")
		}
		if len(out.Hooks) > 0 && !on["hooks"] {
			note("hooks 插件没开：信任后项目钩子也不会执行")
		}
	}
	return out
}

// previewWidened 项目档位放宽了什么。权限项注册表（第一个会话装配后才有）在时用 ProjectWidened，和 mode.set
// 确认时的清单一致（exact）；不在时只能看档位文件本身：继承根比缺省宽、或某项写了 allow。
func previewWidened(d *permission.ModeDef, items []toolhub.Item) ([]string, bool) {
	if len(items) > 0 {
		return permission.ProjectWidened(d, items), true
	}
	w := permission.ProjectWidened(d, nil)
	var ids []string
	for id, s := range d.Items {
		if s.Default == toolhub.ActionAllow || len(s.Allow) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return append(ids, w...), false
}

// previewProfile 项目 profile.toml 的插件行。
func previewProfile(path string, note func(string)) []wire.TrustPluginSetting {
	q, err := profile.Load(path)
	if err != nil {
		note("读项目 profile：" + err.Error())
		return nil
	}
	var out []wire.TrustPluginSetting
	for _, e := range q.Plugins {
		s := wire.TrustPluginSetting{Name: e.Name, Enabled: e.Enabled}
		denied := map[string]bool{}
		for _, k := range projectDenied[e.Name] {
			denied[k] = true
		}
		for k := range e.Config {
			if denied[k] {
				s.Ignored = append(s.Ignored, k)
			} else {
				s.Keys = append(s.Keys, k)
			}
		}
		sort.Strings(s.Keys)
		sort.Strings(s.Ignored)
		out = append(out, s)
	}
	return out
}
