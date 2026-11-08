package fs

// edit：精确编辑（M5.1，docs/90 - 归档/20 - 插件/08 - 精确编辑.md）。
//
// 形状 {path, edits:[{old_text,new_text}]}：每处在原文件上匹配、互不重叠，全部成功才原子写入。
// 匹配两级：精确（唯一命中）→ 逐行规范化（去行尾空白、Unicode 标点/空格归一，唯一命中），
// 规范化命中只替换命中的行，其余字节原样保留。保留 BOM 与原换行风格。old_text 为空 = 新建文件。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// EditSchema edit 工具参数。
const editSchema = `{"type":"object","properties":{
"path":{"type":"string","description":"文件路径（相对工作目录）"},
"edits":{"type":"array","description":"一处或多处替换。每处 old_text 在原文件中必须唯一、互不重叠；相邻改动合并成一处。","items":{"type":"object","properties":{
"old_text":{"type":"string","description":"要替换的原文（精确匹配；尽量短但要唯一）。为空表示新建文件。不要带 read_file 输出里的行号前缀"},
"new_text":{"type":"string","description":"替换后的文本"},
"replace_all":{"type":"boolean","description":"替换 old_text 的全部出现（不要求唯一，只做精确匹配）；缺省 false"}},"required":["old_text","new_text"]}}},
"required":["path","edits"]}`

type editArgs struct {
	Path  string     `json:"path"`
	Edits []editItem `json:"edits"`
}

type editItem struct {
	Old string `json:"old_text"`
	New string `json:"new_text"`
	All bool   `json:"replace_all"`
}

// EditSnippetLines 结果里每处改动回显的最多行数（改动后的文本，带行号）。
const EditSnippetLines = 12

// Edit 构造 edit 工具。
// tr 可选：编辑已存在的文件前做读后写检查（审查 P1-43）。
func Edit(root string, tr ...*Tracker) *toolhub.Tool {
	root, _ = filepath.Abs(root)
	track := firstTracker(tr)
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: "edit",
			Description: "精确编辑文件：按 old_text → new_text 替换，可一次多处（在原文件上匹配、全部成功才写入）。old_text 为空表示新建文件。修改已有文件优先用它，不要整文件重写。",
			Schema:      json.RawMessage(editSchema)},
		Risk:  toolhub.Write,
		Attrs: map[string]any{toolhub.AttrWorkspace: true},
		Call: func(_ context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a editArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return toolhub.Failf("参数不合法：%v", err), nil
			}
			p, err := resolve(root, a.Path)
			if err != nil {
				return toolhub.Failf("%v", err), nil
			}
			if err := track.Check(cx, p, a.Path); err != nil {
				return toolhub.Failf("%v", err), nil
			}
			msg, err := applyEdits(p, a.Path, a.Edits)
			if err != nil {
				return toolhub.Failf("%v", err), nil
			}
			track.Note(cx, p) // 自己写的结果算读过：连续 edit 不必每次重读
			return toolhub.Text(msg), nil
		},
	}
}

func applyEdits(p, display string, edits []editItem) (string, error) {
	if len(edits) == 0 {
		return "", errors.New("edits 不能为空")
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		if len(edits) != 1 || edits[0].Old != "" {
			return "", fmt.Errorf("文件 %s 不存在（新建文件请给一处 old_text 为空的编辑）", display)
		}
		if err := writeAtomic(p, []byte(edits[0].New)); err != nil {
			return "", err
		}
		return fmt.Sprintf("已新建 %s（%d 行）", display, countLines(edits[0].New)), nil
	}
	if err != nil {
		return "", err
	}
	for _, e := range edits {
		if e.Old == "" {
			return "", fmt.Errorf("文件 %s 已存在，old_text 不能为空（整文件覆盖用 write_file）", display)
		}
	}
	bom := bytes.HasPrefix(raw, []byte("\xEF\xBB\xBF"))
	if bom {
		raw = raw[3:]
	}
	crlf := bytes.Contains(raw, []byte("\r\n"))
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")

	type rep struct {
		start, end int
		text       string
		idx        int
		fuzzy      bool
	}
	reps := make([]rep, 0, len(edits))
	for i, e := range edits {
		old := strings.ReplaceAll(e.Old, "\r\n", "\n")
		nw := strings.ReplaceAll(e.New, "\r\n", "\n")
		if e.All {
			n := 0
			for off := 0; ; {
				j := strings.Index(content[off:], old)
				if j < 0 {
					break
				}
				reps = append(reps, rep{off + j, off + j + len(old), nw, i, false})
				off += j + len(old)
				n++
			}
			if n == 0 {
				return "", fmt.Errorf("第 %d 处编辑：找不到 old_text（replace_all 只做精确匹配）", i+1)
			}
			continue
		}
		s, en, fuzzy, err := locate(content, old)
		if err != nil {
			return "", fmt.Errorf("第 %d 处编辑：%v", i+1, err)
		}
		reps = append(reps, rep{s, en, nw, i, fuzzy})
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].start < reps[j].start })
	for i := 1; i < len(reps); i++ {
		if reps[i].start < reps[i-1].end {
			return "", fmt.Errorf("第 %d 处与第 %d 处编辑重叠，请合并成一处", reps[i-1].idx+1, reps[i].idx+1)
		}
	}
	var out strings.Builder
	var notes []string
	type span struct{ from, n int } // 改动后文件里的起始行（1 起）与行数，回显片段用
	var spans []span
	prev, newLine := 0, 1 // newLine：改动后文件里当前写到的行号，逐段累加（replace_all 时不必每处从头数）
	for _, r := range reps {
		out.WriteString(content[prev:r.start])
		newLine += strings.Count(content[prev:r.start], "\n")
		out.WriteString(r.text)
		prev = r.end
		line := strings.Count(content[:r.start], "\n") + 1
		n := fmt.Sprintf("第 %d 处：第 %d–%d 行 → %d 行", r.idx+1, line, line+strings.Count(content[r.start:r.end], "\n"), countLines(r.text))
		if r.fuzzy {
			n += "（规范化匹配：忽略了行尾空白 / Unicode 标点差异）"
		}
		notes = append(notes, n)
		spans = append(spans, span{newLine, max(countLines(r.text), 1)})
	}
	out.WriteString(content[prev:])
	res := out.String()
	if res == content {
		return "", errors.New("替换后内容没有变化")
	}
	if crlf {
		res = strings.ReplaceAll(res, "\n", "\r\n")
	}
	data := []byte(res)
	if bom {
		data = append([]byte("\xEF\xBB\xBF"), data...)
	}
	if err := writeAtomic(p, data); err != nil {
		return "", err
	}
	// notes 已按位置排好（reps 按 start 排序）；原先 sort.Strings 按字典序排，"第 10 处"排在"第 2 处"前面。
	var b strings.Builder
	fmt.Fprintf(&b, "已编辑 %s（%d 处）\n%s", display, len(reps), strings.Join(notes, "\n"))
	// 回显改动后的片段（带行号），模型不必再 read_file 一次确认（2026-11-04 审查 P2 工具体验）。
	lines := strings.Split(strings.TrimSuffix(res, "\n"), "\n")
	if crlf {
		lines = strings.Split(strings.TrimSuffix(strings.ReplaceAll(res, "\r\n", "\n"), "\n"), "\n")
	}
	shown := 0
	for _, s := range spans {
		if shown >= 3*EditSnippetLines {
			b.WriteString("\n…（其余改动未显示）")
			break
		}
		end := min(s.from-1+s.n, len(lines), s.from-1+EditSnippetLines)
		b.WriteString("\n---\n")
		for i := s.from - 1; i < end; i++ {
			fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i]) // 与 read_file 同格式
			shown++
		}
		if s.n > EditSnippetLines {
			fmt.Fprintf(&b, "…（本处共 %d 行，只显示前 %d 行）\n", s.n, EditSnippetLines)
		}
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// locate 返回 old 在 content 中的唯一位置 [start,end)。
func locate(content, old string) (int, int, bool, error) {
	if n := strings.Count(content, old); n == 1 {
		i := strings.Index(content, old)
		return i, i + len(old), false, nil
	} else if n > 1 {
		return 0, 0, false, fmt.Errorf("old_text 命中 %d 处（第 %s 行），请加上下文使其唯一", n, hitLines(content, old))
	}
	// 规范化逐行匹配。
	lines := strings.SplitAfter(content, "\n")
	want := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	var hits []int
	for i := 0; i+len(want) <= len(lines); i++ {
		ok := true
		for j, w := range want {
			if normLine(lines[i+j]) != normLine(w) {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 0:
		return 0, 0, false, errors.New("找不到 old_text（请先用 read_file 读取当前内容，按原文逐字复制）")
	case 1:
	default:
		var ls []string
		for _, h := range hits {
			ls = append(ls, fmt.Sprint(h+1))
		}
		return 0, 0, false, fmt.Errorf("old_text 规范化后命中 %d 处（第 %s 行），请加上下文使其唯一", len(hits), strings.Join(ls, "、"))
	}
	start := 0
	for _, l := range lines[:hits[0]] {
		start += len(l)
	}
	end := start
	for _, l := range lines[hits[0] : hits[0]+len(want)] {
		end += len(l)
	}
	// old 不以换行结尾时保留命中末行的换行。
	if !strings.HasSuffix(old, "\n") && end > start && content[end-1] == '\n' {
		end--
	}
	return start, end, true, nil
}

var punct = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", "\u201A", "'", "\u201B", "'",
	"\u201C", `"`, "\u201D", `"`, "\u201E", `"`, "\u201F", `"`,
	"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-", "\u2212", "-",
	"\u00A0", " ", "\u202F", " ", "\u205F", " ", "\u3000", " ",
	"\u2002", " ", "\u2003", " ", "\u2004", " ", "\u2005", " ", "\u2006", " ", "\u2007", " ", "\u2008", " ", "\u2009", " ", "\u200A", " ",
)

func normLine(s string) string {
	s = strings.TrimRight(s, " \t\r\n")
	return punct.Replace(norm.NFKC.String(s))
}

func hitLines(content, old string) string {
	var ls []string
	off := 0
	for {
		i := strings.Index(content[off:], old)
		if i < 0 || len(ls) >= 10 {
			break
		}
		ls = append(ls, fmt.Sprint(strings.Count(content[:off+i], "\n")+1))
		off += i + 1
	}
	return strings.Join(ls, "、")
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// writeAtomic 同目录临时文件 + rename，保留原文件权限。
func writeAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tenon-edit-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, mode)
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
