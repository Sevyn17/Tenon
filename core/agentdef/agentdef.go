// Package agentdef 智能体定义与加载（docs/框架设计/03 - 智能体定义.md，T1a）。
//
// 定义是一个目录（定义包）：agent.toml 写 6 个内核字段 + [use.*] + [x.*]，其余文件原样随包。
// 本包只做：解析 agent.toml、校验内核字段、计算整包内容哈希、按三个命名空间（平台 / 租户 / 用户）查找定义。
//
// 不在本包（T1b 及以后）：use.<组件> 按组件结构校验、使用配置链、用户层越权拒绝、工具名与模型档位解析。
package agentdef

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// ManifestName 定义包清单文件名。
const ManifestName = "agent.toml"

// FormatV1 当前唯一支持的定义包格式；agent.toml 不写 format 时按它处理。
const FormatV1 = "ybase/v1"

// MaxPackageSize 单个定义包所有文件的总字节上限，防止误把大目录当定义包。
const MaxPackageSize = 32 << 20

// ErrNotFound 定义不存在（或对当前范围不可见）。
var ErrNotFound = errors.New("agentdef: 定义不存在")

// ErrUnsupported 定义用了当前阶段未支持的能力（如 resident = true，T5 之前）。
var ErrUnsupported = errors.New("agentdef: 未支持")

// Files 定义包里的文件：包内相对路径（/ 分隔）→ 内容。
type Files map[string][]byte

// Names 按字典序返回所有文件路径。
func (f Files) Names() []string {
	ns := make([]string, 0, len(f))
	for n := range f {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return ns
}

// Agent 智能体定义。只读、带版本；同一份定义包解析结果恒相同。
type Agent struct {
	// ① 内核字段：固定 6 个
	ID          string   // 定义名，须与所在命名空间里的名字一致
	Description string   // 别的智能体把它当工具用时的工具描述
	Model       string   // 模型或档位（fast / strong），T1b 再校验档位存在
	Prompt      string   // 内联文本，或 "@AGENT.md" 指向包内文件
	Tools       []string // 工具白名单，支持通配；T1b 再解析成具体工具集合
	Resident    bool     // 常驻；T5 之前为 true 时加载报"未支持"

	// ② 组件字段：use.<组件> 原样保存，T1b 由对应组件按其结构校验
	Use map[string]map[string]any
	// ③ 应用字段：x.<命名空间> 原样保存，内核不解释
	X map[string]map[string]any

	// 由框架填写
	Format  string // 定义包格式版本，如 "ybase/v1"
	Version string // 整包内容哈希（sha256 十六进制），见 Hash
	Files   Files  // 定义包里除 agent.toml 外的其他文件
}

// PromptText 返回提示词正文："@文件" 形式取包内文件内容，否则就是内联文本。
func (a *Agent) PromptText() string {
	if f, ok := promptFile(a.Prompt); ok {
		return string(a.Files[f])
	}
	return a.Prompt
}

func promptFile(p string) (string, bool) {
	if strings.HasPrefix(p, "@") {
		return strings.TrimPrefix(p, "@"), true
	}
	return "", false
}

// manifest agent.toml 的形状；字段集合封闭，未知顶层字段报错。
type manifest struct {
	Format      string                    `toml:"format"`
	ID          string                    `toml:"id"`
	Description string                    `toml:"description"`
	Model       string                    `toml:"model"`
	Prompt      string                    `toml:"prompt"`
	Tools       []string                  `toml:"tools"`
	Resident    bool                      `toml:"resident"`
	Use         map[string]map[string]any `toml:"use"`
	X           map[string]map[string]any `toml:"x"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ValidName 定义名 / 用户名是否合法：字母数字开头，字母数字与 _ . -，最长 64，不含 /。
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Parse 把一份定义包（含 agent.toml）解析成 Agent：校验内核字段、填 Format / Version / Files。
// where 用于错误信息（目录或引用），可为空。
func Parse(where string, files Files) (Agent, error) {
	file := ManifestName
	if where != "" {
		file = path.Join(where, ManifestName)
	}
	fail := func(format string, a ...any) error {
		return fmt.Errorf("定义 %s：%s", file, fmt.Sprintf(format, a...))
	}
	raw, ok := files[ManifestName]
	if !ok {
		return Agent{}, fail("缺少 %s", ManifestName)
	}
	total := 0
	for n, b := range files {
		if err := checkPath(n); err != nil {
			return Agent{}, fail("文件 %q：%v", n, err)
		}
		total += len(b)
	}
	if total > MaxPackageSize {
		return Agent{}, fail("定义包共 %d 字节，超过上限 %d", total, MaxPackageSize)
	}

	var m manifest
	md, err := toml.Decode(string(raw), &m)
	if err != nil {
		return Agent{}, fail("%v", err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		ks := make([]string, 0, len(und))
		for _, k := range und {
			ks = append(ks, k.String())
		}
		return Agent{}, fail("未知字段 %s", strings.Join(ks, "、"))
	}

	// use / x 及其下一层都须是表；类型不符时 toml 库可能静默忽略，这里显式检查。
	for _, top := range []string{"use", "x"} {
		if t := md.Type(top); t != "" && t != "Hash" {
			return Agent{}, fail("%s 须是表", top)
		}
	}
	for _, k := range md.Keys() {
		if len(k) == 2 && (k[0] == "use" || k[0] == "x") && md.Type(k...) != "" && md.Type(k...) != "Hash" {
			return Agent{}, fail("%s 须是表", k.String())
		}
	}

	switch m.Format {
	case "":
		m.Format = FormatV1
	case FormatV1:
	default:
		return Agent{}, fail("format %q 不支持（当前只支持 %q）", m.Format, FormatV1)
	}
	if m.ID == "" {
		return Agent{}, fail("缺少 id")
	}
	if !ValidName(m.ID) {
		return Agent{}, fail("id %q 不合法（字母数字开头，只含字母、数字、_ . -，最长 64）", m.ID)
	}
	for i, t := range m.Tools {
		if strings.TrimSpace(t) == "" {
			return Agent{}, fail("tools[%d] 为空", i)
		}
	}
	if f, ok := promptFile(m.Prompt); ok {
		if f == "" || f == ManifestName {
			return Agent{}, fail("prompt %q 须指向包内其他文件", m.Prompt)
		}
		if _, ok := files[f]; !ok {
			return Agent{}, fail("prompt 指向的文件 %s 不在定义包里", f)
		}
	}
	for k := range m.Use {
		if !ValidName(k) {
			return Agent{}, fail("use.%s：组件名不合法", k)
		}
	}
	for k := range m.X {
		if !ValidName(k) {
			return Agent{}, fail("x.%s：命名空间不合法", k)
		}
	}
	if m.Resident {
		return Agent{}, fmt.Errorf("定义 %s：resident = true %w（常驻定义在 T5 支持）", file, ErrUnsupported)
	}

	others := make(Files, len(files)-1)
	for n, b := range files {
		if n != ManifestName {
			others[n] = bytes.Clone(b)
		}
	}
	return Agent{
		ID:          m.ID,
		Description: m.Description,
		Model:       m.Model,
		Prompt:      m.Prompt,
		Tools:       m.Tools,
		Resident:    m.Resident,
		Use:         m.Use,
		X:           m.X,
		Format:      m.Format,
		Version:     Hash(files),
		Files:       others,
	}, nil
}

// checkPath 包内路径须是 / 分隔、已规范化的相对路径，不能逃出包目录。
func checkPath(n string) error {
	if n == "" || strings.Contains(n, `\`) || path.IsAbs(n) || path.Clean(n) != n || n == ".." || strings.HasPrefix(n, "../") {
		return errors.New("路径须为包内规范化的相对路径")
	}
	return nil
}

// Encode 把定义包编成规范字节串，用于入库与哈希：所有文件（含 agent.toml）按路径字典序，
// 依次写入 路径长度（8 字节大端）、路径、内容长度（8 字节大端）、内容。
//
// 规范化：不含 NUL 字节的文件（按文本处理）把 CRLF 换成 LF，这样同一份定义在 Windows
// （git autocrlf）和 Linux 上得到同一个版本；含 NUL 的文件按原始字节。字节串不含时间戳等
// 易变信息，同一份定义永远编出同一串字节，所以可以直接当入库内容的规范形式（Decode 可逆）。
func Encode(files Files) []byte {
	var buf bytes.Buffer
	var n [8]byte
	for _, name := range files.Names() {
		b := files[name]
		if bytes.IndexByte(b, 0) < 0 {
			b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
		}
		binary.BigEndian.PutUint64(n[:], uint64(len(name)))
		buf.Write(n[:])
		buf.WriteString(name)
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		buf.Write(n[:])
		buf.Write(b)
	}
	return buf.Bytes()
}

// Decode 解析 Encode 的字节串。字节串被截断、路径非法或重名时返回错误。
func Decode(b []byte) (Files, error) {
	files := Files{}
	for len(b) > 0 {
		if len(b) < 8 {
			return nil, errors.New("agentdef: 定义包字节串被截断（路径长度）")
		}
		n := binary.BigEndian.Uint64(b[:8])
		b = b[8:]
		if n == 0 || n > uint64(len(b)) {
			return nil, errors.New("agentdef: 定义包字节串被截断（路径）")
		}
		name := string(b[:n])
		b = b[n:]
		if len(b) < 8 {
			return nil, errors.New("agentdef: 定义包字节串被截断（内容长度）")
		}
		m := binary.BigEndian.Uint64(b[:8])
		b = b[8:]
		if m > uint64(len(b)) {
			return nil, errors.New("agentdef: 定义包字节串被截断（内容）")
		}
		if err := checkPath(name); err != nil {
			return nil, fmt.Errorf("agentdef: %s：%w", name, err)
		}
		if _, dup := files[name]; dup {
			return nil, fmt.Errorf("agentdef: 定义包内路径重复 %s", name)
		}
		files[name] = append([]byte(nil), b[:m]...)
		b = b[m:]
	}
	if len(files) == 0 {
		return nil, errors.New("agentdef: 定义包为空")
	}
	if _, ok := files[ManifestName]; !ok {
		return nil, errors.New("agentdef: 定义包缺 " + ManifestName)
	}
	return files, nil
}

// Hash 整包内容哈希 = sha256(Encode(files)) 的十六进制。
//
// 与 Encode 同源：入库内容就是 Encode 的字节串，所以存储算出的 sha256(body)
// 与这里的结果恒等，会话行记的定义哈希、definitions.hash、定义包内容三者一致。
func Hash(files Files) string {
	sum := sha256.Sum256(Encode(files))
	return hex.EncodeToString(sum[:])
}
