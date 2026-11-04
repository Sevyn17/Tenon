package permission

// 插件登记（thin-kernel-plan §2.2）：配置文件（用户级 + 项目级）→ 插件配置覆盖（命令行）→ 会话 Ext 持久状态。
// 提供 service:permission（*Sessions，同时满足 toolhub.SandboxPolicy），登记 Guard 层。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"ybase/runtime/assemble"
	"ybase/runtime/component"
	"ybase/runtime/store"
	"ybase/runtime/toolhub"
)

// Service 本插件提供的服务键。
const Service = "service:permission"

// UseConfig 定义里的 [use.permission]（03 3.2、08 8.3）：这个智能体的权限上限。
// 只收窄、不放宽——每次调用按会话当前档位与这里的档位各判一次，取更严的结论。
//
//	[use.permission]
//	mode = "read-only"
type UseConfig struct {
	Mode string `toml:"mode" description:"本智能体的权限上限档位（read-only / ask / workspace / full 或自定义档位名）；与会话档位取更严"`
}

// PluginConfig permission 插件配置（命令行 -mode / -yes / -sandbox 经插件配置覆盖传入）。
type PluginConfig struct {
	ForceMode string `cfg:"force_mode" title:"强制档位" description:"覆盖配置文件与会话记录：read-only / ask / workspace / full 或自定义档位名"`
	Sandbox   string `cfg:"sandbox" title:"固定沙箱" description:"固定 shell 沙箱：read-only / workspace-write / danger-full-access（缺省随档位）"`
}

type plug struct{}

func init() { component.Register("permission", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "permission", Version: "0.1.0", Default: true, Config: &PluginConfig{},
		Requires: []string{assemble.ServiceDB, assemble.ServiceWorkdir, "?" + assemble.ServiceTrust}, Provides: []string{Service}}
}

func (*plug) Setup(cx *component.Registrar) error {
	cfg, _ := cx.Config().(*PluginConfig)
	if cfg == nil {
		cfg = &PluginConfig{}
	}
	root, err := component.Use[string](cx, assemble.ServiceWorkdir)
	if err != nil {
		return err
	}
	store, err := component.Use[assemble.DBHandle](cx, assemble.ServiceDB)
	if err != nil {
		return err
	}
	proj := root
	if !assemble.ProjectTrusted(cx) {
		proj = "" // 不受信项目：不读 <dir>/.ybase/permissions.toml
	}
	s, err := Open(root, proj, cfg.ForceMode, cfg.Sandbox, store.DB)
	if err != nil {
		return err
	}
	if err := cx.AgentConfig(&UseConfig{}); err != nil {
		return err
	}
	cx.Register(assemble.PointPermItems, ItemSandbox, SandboxItem())
	cx.Register(assemble.PointSessionLayer, "permission", assemble.LayerEntry{Guard: true, Order: 10,
		Func: func(sc *assemble.SessionCx) ([]toolhub.Layer, error) {
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
			// [use.permission]（08 8.3）：定义给的权限上限档位，会话打开后由框架算好放进 Ctx。
			// 没写时不加限制；写了但档位不存在时按 read-only 处理（不因笔误放开）。
			// 会话打开后读算好的 use_config（含租户 / 用户层）；构造工具列表时会话还没打开，
			// 先按定义里写的值（同一来源的第一层）。
			capDef := func() *ModeDef {
				var u UseConfig
				if sc.Ctx != nil {
					_ = sc.Ctx.Config("permission", &u)
				}
				if u.Mode == "" && sc.Def != nil {
					u.Mode, _ = sc.Def.Use["permission"]["mode"].(string)
				}
				if u.Mode == "" {
					return nil
				}
				d, ok := s.reg().Get(Mode(u.Mode))
				if !ok {
					return builtinDef(ReadOnly) // 配置了却找不到：按最严处理，不放开
				}
				return d
			}
			if err := hub.SetVisible(func(n string) bool {
				t, ok := hub.Get(n)
				if !ok {
					return true
				}
				items := hub.ItemsOf(t)
				if d := capDef(); d != nil && !Visible(d, t, items) {
					return false
				}
				return Visible(s.Def(id), t, items)
			}); err != nil {
				return nil, err
			}
			policy := func(sid string) Policy {
				p := s.Policy(sid)
				// 不可信输入回合（外部入口、不可信来源的信封）：allow 降为询问（由内核按回合标记）。
				p.Untrusted = func(cx *toolhub.Cx) bool { return cx.Ctx != nil && cx.Ctx.Untrusted() }
				if d := capDef(); d != nil {
					p.Cap = &Policy{Mode: d.ID, Def: d, Rules: p.Rules, Root: p.Root, Sandbox: p.Sandbox}
				}
				return p
			}
			return []toolhub.Layer{Layer(policy, nil)}, nil
		}})
	return cx.Provide("permission", s)
}

func (*plug) Start(*component.Scope) error { return nil }

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
	} // 平台有没有沙箱由沙箱插件判断（PlatformSupported / Degrade），这里不写死（审查 P1-14）
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
