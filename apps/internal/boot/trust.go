package boot

// 项目信任（plugin-switch-plan §3.3，参照 Codex projects.trust_level）。
// ~/.ybase/trust.toml：[projects."<路径>"] trust = "trusted" | "untrusted"；键 = git 根目录，找不到用工作目录；
// 子目录继承最近的上级记录。不受信：不读项目 .ybase 下的 profile / permissions / hooks / mcp（AGENTS.md 照读）。
// 原在 apps/ybase/trust.go，桌面端与 CLI 共用后移到这里；CLI 只保留终端交互部分。

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"ybase/runtime/component"
	"ybase/runtime/pkgs"
)

// 信任级别。
const (
	Trusted   = "trusted"
	Untrusted = "untrusted"
)

//go:embed profile.toml
var defaultProfileTOML string

// DefaultProfileTOML 内置 base profile 原文。
func DefaultProfileTOML() string { return defaultProfileTOML }

type trustProject struct {
	Trust string `toml:"trust"`
}

type trustFile struct {
	Projects map[string]trustProject `toml:"projects"`
	// ModeApprovals 项目级档位放宽的首次确认（06 - 工具权限设计方案 §9.6）：
	// 键 "<项目键>#<档位名>"，值是当时确认的放宽清单；清单变了要重新确认。
	ModeApprovals map[string]string `toml:"mode_approvals"`
}

func readTrustFile() (trustFile, error) {
	var f trustFile
	path := TrustPath()
	if path == "" {
		return f, nil
	}
	if _, err := toml.DecodeFile(path, &f); err != nil && !os.IsNotExist(err) {
		return f, fmt.Errorf("读 %s: %w", path, err)
	}
	return f, nil
}

func writeTrustFile(f trustFile) error {
	path := TrustPath()
	if path == "" {
		return fmt.Errorf("取不到用户目录，无法保存信任记录")
	}
	// 信任记录决定哪些项目的配置会被加载：只本人可读写（0700 / 0600）。
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	keys := make([]string, 0, len(f.Projects))
	for k := range f.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# 项目信任记录（ybase trust 维护）\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "\n[projects.%q]\ntrust = %q\n", k, f.Projects[k].Trust)
	}
	if len(f.ModeApprovals) > 0 {
		ks := make([]string, 0, len(f.ModeApprovals))
		for k := range f.ModeApprovals {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		b.WriteString("\n# 项目级档位放宽的确认记录（放宽清单变化后需重新确认）\n[mode_approvals]\n")
		for _, k := range ks {
			fmt.Fprintf(&b, "%q = %q\n", k, f.ModeApprovals[k])
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600) // 已存在的旧文件 WriteFile 不改权限
}

// ModeApproved 项目档位 mode 的放宽清单 widened 是否已被确认过。
func ModeApproved(root, mode, widened string) bool {
	f, err := readTrustFile()
	if err != nil {
		return false
	}
	return f.ModeApprovals[ProjectKey(root)+"#"+mode] == widened
}

// ApproveMode 记下项目档位放宽的确认。
func ApproveMode(root, mode, widened string) error {
	f, err := readTrustFile()
	if err != nil {
		return err
	}
	if f.ModeApprovals == nil {
		f.ModeApprovals = map[string]string{}
	}
	f.ModeApprovals[ProjectKey(root)+"#"+mode] = widened
	return writeTrustFile(f)
}

// Home 用户级目录：$YBASE_HOME，缺省 ~/.ybase；取不到返回空。
func Home() string {
	if tp := TrustPath(); tp != "" {
		return filepath.Dir(tp)
	}
	return ""
}

// PackageNotices 插件包带了内容、但负责的内置插件没开时的提示（插件包不替用户开插件）。
func PackageNotices(p *component.Profile, ps []*pkgs.Package) []string {
	on := map[string]bool{}
	for _, st := range p.States() {
		on[st.Name] = st.On
	}
	var out []string
	for _, k := range ps {
		var miss []string
		if len(k.Server) > 0 && !on["mcp"] {
			miss = append(miss, "MCP 服务需开 mcp")
		}
		if len(k.Hook) > 0 && !on["hooks"] {
			miss = append(miss, "钩子需开 hooks")
		}
		if k.SkillsDir != "" && !on["skills"] {
			miss = append(miss, "技能需开 skills")
		}
		if len(k.Prompts) > 0 && !on["prompt"] {
			miss = append(miss, "提示词需开 prompt")
		}
		if len(miss) > 0 {
			out = append(out, fmt.Sprintf("插件包 %s 的部分内容未生效：%s（在 profile.toml 里 [plugin.<名>] enabled = true）", k.Name, strings.Join(miss, "、")))
		}
	}
	return out
}

// TrustPath trust.toml 位置：$YBASE_HOME/trust.toml，缺省 ~/.ybase/trust.toml。
func TrustPath() string {
	if h := os.Getenv("YBASE_HOME"); h != "" {
		return filepath.Join(h, "trust.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ybase", "trust.toml")
}

// ProjectKey 信任记录的键：向上找 .git，找不到用工作目录本身。
func ProjectKey(root string) string {
	for d := root; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return NormPath(d)
		}
		p := filepath.Dir(d)
		if p == d {
			return NormPath(root)
		}
		d = p
	}
}

// NormPath 规范化路径（Windows 下转小写）。
func NormPath(p string) string {
	p = filepath.Clean(p)
	if os.PathSeparator == '\\' {
		p = strings.ToLower(p)
	}
	return p
}

// LoadTrust 读全部项目信任记录（键已规范化）。
func LoadTrust() (map[string]string, error) {
	out := map[string]string{}
	f, err := readTrustFile()
	if err != nil {
		return nil, err
	}
	for k, v := range f.Projects {
		switch v.Trust {
		case Trusted, Untrusted:
		default:
			return nil, fmt.Errorf("%s: 项目 %s 的 trust 应为 trusted / untrusted，得到 %q", TrustPath(), k, v.Trust)
		}
		out[NormPath(k)] = v.Trust
	}
	return out, nil
}

// LookupTrust 最近的上级记录；没有返回 ""。
func LookupTrust(m map[string]string, key string) string {
	for d := key; ; {
		if v, ok := m[d]; ok {
			return v
		}
		p := filepath.Dir(d)
		if p == d {
			return ""
		}
		d = p
	}
}

// SaveTrust 保存（level 为空 = 删除）一条项目信任记录。
func SaveTrust(key, level string) error {
	if _, err := LoadTrust(); err != nil {
		return err
	}
	f, err := readTrustFile()
	if err != nil {
		return err
	}
	if f.Projects == nil {
		f.Projects = map[string]trustProject{}
	}
	if level == "" {
		delete(f.Projects, key)
	} else {
		f.Projects[key] = trustProject{Trust: level}
	}
	return writeTrustFile(f)
}

// HasProjectConfig 工作目录下是否有会被信任规则影响的项目配置。
func HasProjectConfig(root string) bool {
	for _, n := range []string{"profile.toml", "permissions.toml", "hooks.toml", "mcp.toml"} {
		if _, err := os.Stat(filepath.Join(root, ".ybase", n)); err == nil {
			return true
		}
	}
	return pkgs.HasAny(filepath.Join(root, ".ybase", "plugins"))
}

// ResolveTrust 决定本次运行是否信任项目。force（-trust-project / -project-hooks）只对本次生效。
// 无记录且有项目配置时：ask 非空就问一次并保存（ask 的保存失败只告警），ask 为空按不受信。
func ResolveTrust(force bool, root string, ask func(key string) bool) (bool, string, error) {
	if force {
		return true, "命令行 -trust-project", nil
	}
	m, err := LoadTrust()
	if err != nil {
		return false, "", err
	}
	key := ProjectKey(root)
	switch LookupTrust(m, key) {
	case Trusted:
		return true, "trust.toml", nil
	case Untrusted:
		return false, "trust.toml", nil
	}
	if !HasProjectConfig(root) {
		return false, "无记录（项目无 .ybase 配置）", nil
	}
	if ask == nil {
		return false, "无记录（非交互，按不受信）", nil
	}
	ok := ask(key)
	level := Untrusted
	if ok {
		level = Trusted
	}
	if err := SaveTrust(key, level); err != nil {
		fmt.Fprintf(os.Stderr, "[信任] 未能保存：%v\n", err)
	}
	return ok, "首次询问", nil
}

// projectDenied 受信项目也不能设的键（端点 / 凭据 / 存储位置）。
var projectDenied = map[string][]string{
	"models":       {"base_url", "catalog"},
	"store-sqlite": {"dir"},
}

// LoadProfiles 同 LoadAll，不要插件包列表。
func LoadProfiles(extra, root string, trusted bool) (*component.Profile, []string, error) {
	p, _, notes, err := LoadAll(extra, root, trusted)
	return p, notes, err
}

// LoadAll 读插件包与各层 profile：
// 内置 < 插件包 [config.*] < ~/.ybase/profile.toml < <dir>/.ybase/profile.toml（受信时）< extra（-profile）。
// 插件包：用户级 <home>/plugins/*，项目级 <dir>/.ybase/plugins/*（只在受信时）。
func LoadAll(extra, root string, trusted bool) (*component.Profile, []*pkgs.Package, []string, error) {
	p, err := component.ParseProfile(defaultProfileTOML, ".")
	if err != nil {
		return nil, nil, nil, err
	}
	p.Sources = map[string]string{}
	var notes []string
	ps, pn, err := pkgs.Load(Home(), root, trusted)
	if err != nil {
		return nil, nil, nil, err
	}
	notes = append(notes, pn...)
	for _, k := range ps {
		q := k.Profile()
		if k.Scope == pkgs.ScopeProject {
			for name, keys := range projectDenied {
				for _, key := range q.Strip(name, keys...) {
					notes = append(notes, fmt.Sprintf("项目插件包 %s 不能设 %s，已忽略", k.Name, key))
				}
			}
		}
		if err := p.Overlay(q); err != nil {
			return nil, nil, nil, fmt.Errorf("插件包 %s：%w", k.File, err)
		}
	}
	layer := func(path string, project bool) error {
		if _, err := os.Stat(path); err != nil {
			return nil
		}
		q, err := component.LoadProfile(path)
		if err != nil {
			return err
		}
		if project {
			for name, keys := range projectDenied {
				for _, k := range q.Strip(name, keys...) {
					notes = append(notes, fmt.Sprintf("项目 profile 不能设 %s，已忽略", k))
				}
			}
		}
		return p.Overlay(q)
	}
	if tp := TrustPath(); tp != "" {
		if err := layer(filepath.Join(filepath.Dir(tp), "profile.toml"), false); err != nil {
			return nil, nil, nil, err
		}
	}
	proj := filepath.Join(root, ".ybase", "profile.toml")
	if trusted {
		if err := layer(proj, true); err != nil {
			return nil, nil, nil, err
		}
	} else if _, err := os.Stat(proj); err == nil {
		notes = append(notes, "项目未受信，未读 "+proj+"（ybase trust 可信任）")
	}
	if extra != "" {
		q, err := component.LoadProfile(extra)
		if err != nil {
			return nil, nil, nil, err
		}
		if err := p.Overlay(q); err != nil {
			return nil, nil, nil, err
		}
	}
	notes = append(notes, PackageNotices(p, ps)...)
	return p, ps, notes, nil
}
