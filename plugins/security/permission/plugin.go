package permission

// 插件登记（thin-kernel-plan §2.2）：配置文件（用户级 + 项目级）→ 插件配置覆盖（命令行）→ 会话 Ext 持久状态。
// 提供 service:permission（*Sessions，同时满足 toolhub.SandboxPolicy），登记 Guard 层。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"

	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/store"
	"ybase-kernel/core/toolhub"
)

// Service 本插件提供的服务键。
const Service = "service:permission"

// PluginConfig permission 插件配置（命令行 -mode / -yes / -sandbox 经插件配置覆盖传入）。
type PluginConfig struct {
	ForceMode string `cfg:"force_mode" title:"强制档位" description:"覆盖配置文件与会话记录：read-only / ask / workspace / full 或自定义档位名"`
	Sandbox   string `cfg:"sandbox" title:"固定沙箱" description:"固定 shell 沙箱：read-only / workspace-write / danger-full-access（缺省随档位）"`
}

type plug struct{}

func init() { plugin.Register("permission", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "permission", Version: "0.1.0", Default: true, Config: &PluginConfig{},
		Requires: []string{kr.ServiceDB, kr.ServiceWorkdir, "?" + kr.ServiceTrust}, Provides: []string{Service}}
}

func (*plug) Setup(cx *plugin.Registrar) error {
	cfg, _ := cx.Config().(*PluginConfig)
	if cfg == nil {
		cfg = &PluginConfig{}
	}
	root, err := plugin.Use[string](cx, kr.ServiceWorkdir)
	if err != nil {
		return err
	}
	store, err := plugin.Use[kr.DBHandle](cx, kr.ServiceDB)
	if err != nil {
		return err
	}
	proj := root
	if !kr.ProjectTrusted(cx) {
		proj = "" // 不受信项目：不读 <dir>/.ybase/permissions.toml
	}
	s, err := Open(root, proj, cfg.ForceMode, cfg.Sandbox, store.DB)
	if err != nil {
		return err
	}
	cx.Register(kr.PointPermItems, ItemSandbox, SandboxItem())
	cx.Register(kr.PointSessionLayer, "permission", kr.LayerEntry{Guard: true, Order: 10,
		Func: func(sc *kr.SessionCx) ([]toolhub.Layer, error) {
			// §9.5：与参数无关地必然 deny 的工具不进模型工具列表（每次构造工具列表时按当前档位求值）。
			hub, id := sc.Hub, sc.ID
			s.SetItems(hub.Items())
			s.SetLookup(func(n string) []toolhub.Item {
				t, ok := hub.Get(n)
				if !ok {
					return nil
				}
				return hub.ItemsOf(t)
			})
			// §9.2：插件建议值越过封顶表 → 拒绝启动。
			if err := CheckItems(hub.Items()); err != nil {
				return nil, err
			}
			if err := hub.SetVisible(func(n string) bool {
				t, ok := hub.Get(n)
				if !ok {
					return true
				}
				return Visible(s.Def(id), t, hub.ItemsOf(t))
			}); err != nil {
				return nil, err
			}
			return []toolhub.Layer{Layer(s.Policy, nil)}, nil
		}})
	return cx.Provide("permission", s)
}

func (*plug) Start(*plugin.Scope) error { return nil }

// userHome 用户全局目录：$YBASE_HOME 或 ~/.ybase；取不到返回空。
func userHome() string {
	if h := os.Getenv("YBASE_HOME"); h != "" {
		return h
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ybase")
	}
	return ""
}

// ModeDirs 自定义档位目录：用户级 <home>/modes，项目受信时再加 <project>/.ybase/modes（同名覆盖用户级）。
func ModeDirs(project string) []string {
	var dirs []string
	if h := userHome(); h != "" {
		dirs = append(dirs, filepath.Join(h, "modes"))
	}
	if project != "" {
		dirs = append(dirs, filepath.Join(project, ".ybase", "modes"))
	}
	return dirs
}

// Open 合并用户级与项目级 permissions.toml，读档位注册表，套用强制档位 / 固定沙箱，
// 会话首次用到时经 db 读取其 Ext 里的档位与会话规则（db 返回 nil 时不读）。
// project 为空时不读项目级配置与项目档位（项目不受信）。
func Open(root, project, force, fixedSandbox string, db func() store.Store) (*Sessions, error) {
	var cfgs []Config
	if h := userHome(); h != "" {
		c, err := LoadConfig(filepath.Join(h, "permissions.toml"))
		if err != nil {
			return nil, err
		}
		cfgs = append(cfgs, c)
	}
	if project != "" {
		p := filepath.Join(project, ".ybase", "permissions.toml")
		c, err := LoadConfig(p)
		if err != nil {
			return nil, err
		}
		cfgs = append(cfgs, c)
	}
	cfg := Merge(cfgs...)
	rules, err := cfg.Parsed()
	if err != nil {
		return nil, err
	}
	reg, err := NewRegistry(ModeDirs(project)...)
	if err != nil {
		return nil, err
	}
	def := Default
	if cfg.Mode != "" {
		def = Mode(cfg.Mode)
		if _, ok := reg.Get(def); !ok {
			return nil, fmt.Errorf("permission: permissions.toml 的 mode %q 不存在", cfg.Mode)
		}
	}
	forced := false
	if force != "" {
		def = Mode(force)
		if _, ok := reg.Get(def); !ok {
			return nil, fmt.Errorf("permission: 没有档位 %q", force)
		}
		forced = true
	}
	s := &Sessions{Root: root, Rules: rules, Default: def, Reg: reg}
	if fixedSandbox != "" {
		if s.Fixed, err = toolhub.ParseSandboxMode(fixedSandbox); err != nil {
			return nil, err
		}
	} else if goruntime.GOOS != "windows" { // 其他平台 P0 尚无沙箱实现
		s.Fixed = toolhub.SandboxFull
	}
	s.Load = func(id string) (Mode, []string) {
		d := db()
		if d == nil {
			return "", nil
		}
		l, err := d.Load(context.Background(), id, nil)
		if err != nil {
			return "", nil
		}
		ext := l.Snapshot.Header.Ext
		var m Mode
		if v, _ := ext[ExtMode].(string); v != "" && !forced {
			if _, ok := reg.Get(Mode(v)); ok {
				m = Mode(v)
			}
		}
		var raw []string
		switch v := ext[ExtRules].(type) {
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					raw = append(raw, s)
				}
			}
		case []string:
			raw = v
		}
		return m, raw
	}
	return s, nil
}
