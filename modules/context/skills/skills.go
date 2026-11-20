// Package skills 发现并注入技能（可复用的操作说明），按需加载进系统提示词。
package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Sevyn17/tenon/modules/internal/workspace"
	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 技能默认预算（docs/90 - 归档/20 - 插件/03 - 技能.md）。
const (
	SkillNameMax       = 64
	SkillDescMax       = 1024 // 规范上限，超出视为无效
	SkillCatalogDesc   = 500  // 目录里每条描述的截断长度
	SkillCatalogBudget = 8 << 10
	SkillBodyMax       = 64 << 10
)

var skillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// 技能来源（Skill.Source、skills.list 的 source）：项目、用户级，插件包为 "package:<包名>"。
const (
	SourceProject = "project"
	SourceUser    = "user"
	SourcePackage = "package" // 后接 ":<包名>"；不知道包名时就是 "package"
)

// Skill 一条技能摘要（正文按需读取）。
type Skill struct {
	Name, Description string
	Path              string // SKILL.md 或 <name>.md 的绝对路径
	Dir               string // 资源目录（相对路径据此解析）
	ModelInvocable    bool
	Root              string
	Source            string // SourceProject / SourceUser / "package:<名>"；直接给 Roots 时为空
}

// Skills 本地技能目录：发现、目录渲染、按需装载。
type Skills struct {
	Roots    []string // 先列者优先（同名先到先得）
	Budget   int      // 目录字节预算
	Warnings []string
	// RootSource 根目录 → 来源（Skill.Source）；没登记的根来源为空。
	RootSource map[string]string
	// State 启停记录（state.go）；nil = 没有记录，全部启用。
	State *State
	list  []Skill
}

// PackageDir 插件包带的技能目录。
type PackageDir struct{ Name, Dir string }

// NewSkills 默认根：项目 .tenon/skills、.agents/skills，再用户 ~/.tenon/skills，最后 extra（插件包的技能目录）。
func NewSkills(workdir string, extra ...string) *Skills {
	return newSkills(workdir, true, pkgDirs(extra))
}

// NewSkillsTrust 同 NewSkills；trusted=false（项目不受信）时不读项目里的 .tenon/skills、.agents/skills，
// 只读用户级与插件包的技能（2026-11-04 审查 P1-4：与 permission / mcp 的项目信任语义一致）。
func NewSkillsTrust(workdir string, trusted bool, extra ...string) *Skills {
	return newSkills(workdir, trusted, pkgDirs(extra))
}

// NewSkillsPackages 同 NewSkillsTrust，插件包目录带包名（来源记成 "package:<包名>"）。
func NewSkillsPackages(workdir string, trusted bool, ps []PackageDir) *Skills {
	return newSkills(workdir, trusted, ps)
}

func pkgDirs(extra []string) []PackageDir {
	out := make([]PackageDir, len(extra))
	for i, d := range extra {
		out[i] = PackageDir{Dir: d}
	}
	return out
}

func newSkills(workdir string, trusted bool, ps []PackageDir) *Skills {
	workdir, _ = filepath.Abs(workdir)
	root, err := workspace.ProjectRoot(workdir)
	if err != nil {
		root = workdir
	}
	src := map[string]string{}
	roots := []string{}
	add := func(dir, source string) {
		if _, ok := src[dir]; !ok {
			src[dir] = source
		}
		roots = append(roots, dir)
	}
	if trusted {
		for _, d := range []string{workdir, root} {
			add(filepath.Join(appid.Project(d), "skills"), SourceProject)
			add(filepath.Join(d, ".agents", "skills"), SourceProject)
		}
	}
	if h := userHome(); h != "" {
		add(filepath.Join(h, "skills"), SourceUser)
	}
	for _, p := range ps {
		source := SourcePackage
		if p.Name != "" {
			source += ":" + p.Name
		}
		add(p.Dir, source)
	}
	s := &Skills{Roots: dedupe(roots), RootSource: src, State: &State{Workspace: WorkspaceKey(workdir)}}
	s.Scan()
	return s
}

// userHome 用户全局目录：应用的用户级目录（appid.Home，缺省 $TENON_HOME 或 ~/.tenon）；取不到返回空。
func userHome() string { return appid.Home() }

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func dedupe(xs []string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// Scan 重新扫描所有根。只认顶层 <name>/SKILL.md 与 <name>.md，不递归。
func (s *Skills) Scan() {
	s.list, s.Warnings = nil, nil
	seen := map[string]bool{}
	for _, root := range s.Roots {
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			n := e.Name()
			if strings.HasPrefix(n, ".") || n == "node_modules" {
				continue
			}
			var p string
			switch {
			case e.IsDir():
				p = filepath.Join(root, n, "SKILL.md")
				if !fileExists(p) {
					continue
				}
			case strings.HasSuffix(n, ".md"):
				p = filepath.Join(root, n)
			default:
				continue
			}
			sk, warn := parseSkill(p, e.IsDir())
			if warn != "" {
				s.Warnings = append(s.Warnings, p+": "+warn)
			}
			if sk == nil {
				continue
			}
			if seen[sk.Name] {
				s.Warnings = append(s.Warnings, p+": 同名技能 "+sk.Name+" 已存在，忽略")
				continue
			}
			seen[sk.Name] = true
			sk.Root, sk.Source = root, s.RootSource[root]
			s.list = append(s.list, *sk)
		}
	}
	sort.Slice(s.list, func(i, j int) bool { return s.list[i].Name < s.list[j].Name })
}

// List 当前技能（按名排序），含已停用的。
func (s *Skills) List() []Skill { return append([]Skill(nil), s.list...) }

// Info 一条技能与它的有效启停状态（skills.list）。
type Info struct {
	Skill
	Status
}

// Infos 全部技能与有效启停状态（按名排序）。启停记录读不出来时按全部启用，并返回错误。
func (s *Skills) Infos() ([]Info, error) {
	names := make([]string, len(s.list))
	for i, sk := range s.list {
		names[i] = sk.Name
	}
	st, err := s.State.Statuses(names)
	out := make([]Info, len(s.list))
	for i, sk := range s.list {
		out[i] = Info{Skill: sk, Status: st[sk.Name]}
	}
	return out, err
}

// Enabled 当前启用的技能（按名排序）。
func (s *Skills) Enabled() []Skill {
	is, _ := s.Infos()
	var out []Skill
	for _, i := range is {
		if i.Enabled {
			out = append(out, i.Skill)
		}
	}
	return out
}

// Find 按名字找技能（含已停用的）。
func (s *Skills) Find(name string) (Skill, bool) {
	for _, sk := range s.list {
		if sk.Name == name {
			return sk, true
		}
	}
	return Skill{}, false
}

// parseSkill 读 frontmatter。无效返回 nil 和原因。
func parseSkill(p string, bundle bool) (*Skill, string) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err.Error()
	}
	fm, _, ok := splitFrontmatter(string(b))
	if !ok {
		return nil, "缺少 frontmatter"
	}
	name := fm["name"]
	if name == "" {
		if bundle {
			name = filepath.Base(filepath.Dir(p))
		} else {
			name = strings.TrimSuffix(filepath.Base(p), ".md")
		}
	}
	if len(name) > SkillNameMax || !skillNameRe.MatchString(name) {
		return nil, fmt.Sprintf("名称 %q 无效（小写字母、数字、单连字符，≤%d）", name, SkillNameMax)
	}
	desc := strings.TrimSpace(fm["description"])
	if desc == "" {
		return nil, "缺少 description"
	}
	if len([]rune(desc)) > SkillDescMax {
		return nil, fmt.Sprintf("description 超过 %d 字符", SkillDescMax)
	}
	return &Skill{Name: name, Description: desc, Path: p, Dir: filepath.Dir(p),
		ModelInvocable: fm["disable-model-invocation"] != "true"}, ""
}

// splitFrontmatter 解析 --- 包围的简单 YAML（key: value，单行或带引号；`>`/`|` 续行折叠）。
func splitFrontmatter(s string) (map[string]string, string, bool) {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, s, false
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return nil, s, false
	}
	head, body := s[4:4+end], s[4+end+4:]
	body = strings.TrimPrefix(body, "\n")
	fm := map[string]string{}
	var key string
	for _, ln := range strings.Split(head, "\n") {
		if key != "" && (strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t")) {
			fm[key] = strings.TrimSpace(fm[key] + " " + strings.TrimSpace(ln))
			continue
		}
		key = ""
		k, v, ok := strings.Cut(ln, ":")
		if !ok || strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "#") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case v == ">" || v == "|" || v == ">-" || v == "|-" || v == "":
			key = k
			fm[k] = ""
		case len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"'):
			if u, err := unquoteJSON(v); err == nil {
				v = u
			} else {
				v = v[1 : len(v)-1]
			}
			fm[k] = v
		case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
			fm[k] = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		default:
			fm[k] = v
		}
	}
	return fm, body, true
}

func unquoteJSON(v string) (string, error) {
	var s string
	err := json.Unmarshal([]byte(v), &s)
	return s, err
}

// Catalog 渲染模型可见的技能目录（不含已停用的）；无技能返回空串。超出预算的条目只列名字。
func (s *Skills) Catalog() string {
	var vis []Skill
	for _, sk := range s.Enabled() {
		if sk.ModelInvocable {
			vis = append(vis, sk)
		}
	}
	if len(vis) == 0 {
		return ""
	}
	budget := s.Budget
	if budget <= 0 {
		budget = SkillCatalogBudget
	}
	var b strings.Builder
	b.WriteString("以下技能是可复用的任务说明。任务与某条描述相符时，先用 skill 工具按名字装载完整说明再行动；不要只凭描述推断说明内容。\n<available_skills>\n")
	var rest []string
	for _, sk := range vis {
		d := []rune(strings.Join(strings.Fields(sk.Description), " "))
		if len(d) > SkillCatalogDesc {
			d = append(d[:SkillCatalogDesc-1], '…')
		}
		line := fmt.Sprintf("- %s: %s\n", sk.Name, string(d))
		if b.Len()+len(line) > budget {
			rest = append(rest, sk.Name)
			continue
		}
		b.WriteString(line)
	}
	if len(rest) > 0 {
		fmt.Fprintf(&b, "- （预算不足，以下只列名字）%s\n", strings.Join(rest, ", "))
	}
	b.WriteString("</available_skills>")
	return b.String()
}

// Block 目录作为系统块（会话创建时装载，之后不变，前缀缓存稳定）。
func (s *Skills) Block() *session.SystemBlock {
	t := s.Catalog()
	if t == "" {
		return nil
	}
	return &session.SystemBlock{Text: t, Source: "skills"}
}

// Load 按名字装载正文；重新读盘并校验名字未变。
func (s *Skills) Load(name string) (string, error) {
	if !skillNameRe.MatchString(name) {
		return "", fmt.Errorf("技能名 %q 无效", name)
	}
	var hit *Skill
	for i := range s.list {
		if s.list[i].Name == name {
			hit = &s.list[i]
		}
	}
	if hit == nil {
		return "", fmt.Errorf("未知技能 %q（或已不可用）", name)
	}
	if !hit.ModelInvocable {
		return "", fmt.Errorf("技能 %q 不允许模型调用", name)
	}
	// 会话建好后才停用的技能：目录里还在（系统块建会话时定下），装载时按最新记录拒绝。
	if st, _ := s.State.Statuses([]string{name}); !st[name].Enabled {
		return "", fmt.Errorf("技能 %q %s", name, st[name].Reason())
	}
	cur, warn := parseSkill(hit.Path, filepath.Base(hit.Path) == "SKILL.md")
	if cur == nil || cur.Name != name {
		return "", fmt.Errorf("技能 %q 已变更，不再可用：%s", name, warn)
	}
	b, err := os.ReadFile(hit.Path)
	if err != nil {
		return "", err
	}
	_, body, _ := splitFrontmatter(string(b))
	body = strings.TrimSpace(body)
	if len(body) > SkillBodyMax {
		body = workspace.ClipUTF8(body, SkillBodyMax) + "\n…（技能正文超出 64KB，已截断）"
	}
	return fmt.Sprintf("<skill_content name=%q>\n%s\n\n技能目录：%s\n文中的相对路径以该目录为基准解析，用绝对路径调用工具。\n</skill_content>", name, body, hit.Dir), nil
}

// Tool 模型侧装载工具 skill {name}。
func (s *Skills) Tool() *toolhub.Tool {
	return toolhub.Simple("skill",
		"按名字装载技能的完整说明（名字见系统提示中的 available_skills）。",
		`{"type":"object","properties":{"name":{"type":"string","description":"技能名"}},"required":["name"]}`,
		func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Name string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			return s.Load(strings.TrimSpace(a.Name))
		})
}
