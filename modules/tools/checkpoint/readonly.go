package checkpoint

// 只读 shell 命令判定（2026-11-21 检查点扫描提速）：能保守地判定为只读的 shell 调用前后都不扫工作区。
// 回合末补扫照旧（作为兜底）。拿不准一律当作可能写文件。
//
// 规则：
//   - 只认声明了 toolhub.AttrCommandArg 的工具（shell 等；MCP、扩展进程不声明，不判定），且只认一条简单命令：出现 ; & | < > $ ` ( ) { } @ 或换行一律不算
//     （覆盖 bash 的管道、重定向、命令替换、后台、进程替换、花括号展开，以及 PowerShell 的子表达式、脚本块、
//     splat；Windows 上 shell 工具用 PowerShell 执行）。引号必须配对。
//   - 第一个词是白名单里的命令名（不带路径；Windows 上不分大小写、可带 .exe），并排除会写文件或执行别的程序的参数。

import (
	"encoding/json"
	"runtime"
	"strings"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// readOnlyCmds 白名单：命令名 → 参数检查（nil = 任何参数都只读）。
var readOnlyCmds = map[string]func(args []string) bool{
	"ls": nil, "dir": nil, "cat": nil, "head": nil, "tail": nil, "wc": nil, "grep": nil, "egrep": nil, "fgrep": nil,
	"pwd": nil, "echo": nil, "stat": nil, "du": nil, "df": nil, "which": nil, "basename": nil, "dirname": nil,
	"realpath": nil, "readlink": nil, "whoami": nil, "uname": nil, "cmp": nil, "diff": nil,
	"md5sum": nil, "sha1sum": nil, "sha256sum": nil, "sha512sum": nil,
	"rg":   rgReadOnly,
	"find": findReadOnly,
	"sed":  sedReadOnly,
	"sort": sortReadOnly,
	"tree": treeReadOnly,
	"git":  gitReadOnly,
	// PowerShell（Windows 上 shell 工具用它执行）：只读的 cmdlet 与别名。
	"get-content": nil, "gc": nil, "type": nil, "get-childitem": nil, "gci": nil, "get-item": nil, "gi": nil,
	"get-location": nil, "gl": nil, "select-string": nil, "sls": nil, "test-path": nil, "resolve-path": nil,
	"rvpa": nil, "get-filehash": nil, "write-output": nil, "write-host": nil,
}

// shellMeta 出现即不算简单命令的字符。
const shellMeta = ";&|<>$`(){}@\n\r"

// readOnlyCall 这次调用是否是能保守判定为只读的命令：只认声明了 toolhub.AttrCommandArg 的工具
// （shell 及产品层同类工具），按该属性指定的参数字段取命令文本。不认工具名。
func readOnlyCall(attrs map[string]any, args json.RawMessage) bool {
	field, _ := attrs[toolhub.AttrCommandArg].(string)
	if field == "" {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return false
	}
	var cmd string
	if json.Unmarshal(m[field], &cmd) != nil {
		return false
	}
	return readOnlyCommand(cmd, runtime.GOOS == "windows")
}

// readOnlyCommand 判定一条命令文本；windows = 命令名不分大小写、可带 .exe。
func readOnlyCommand(cmd string, windows bool) bool {
	if strings.ContainsAny(cmd, shellMeta) {
		return false
	}
	words, ok := splitWords(cmd)
	if !ok || len(words) == 0 {
		return false
	}
	name := words[0]
	if strings.ContainsAny(name, `/\=`) { // 带路径、环境变量赋值前缀
		return false
	}
	if windows {
		name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	}
	check, ok := readOnlyCmds[name]
	if !ok {
		return false
	}
	return check == nil || check(words[1:])
}

// splitWords 按空白切词，单 / 双引号内的空白不切（不做转义处理）；引号不配对返回 false。
func splitWords(s string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	in, has := rune(0), false
	for _, r := range s {
		switch {
		case in != 0:
			if r == in {
				in = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			in, has = r, true
		case r == ' ' || r == '\t':
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if in != 0 {
		return nil, false
	}
	if has {
		out = append(out, cur.String())
	}
	return out, true
}

// shortHas 短选项（-abc，不是 --long）里是否含字母 c。
func shortHas(a string, c byte) bool {
	return len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a[1:], c) >= 0
}

func findReadOnly(args []string) bool {
	for _, a := range args {
		switch a {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
			return false
		}
	}
	return true
}

// rgReadOnly --pre / --hostname-bin 会执行别的程序。
func rgReadOnly(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--pre") || strings.HasPrefix(a, "--hostname-bin") {
			return false
		}
	}
	return true
}

// sedReadOnly 排除就地改（-i、--in-place）、脚本文件（-f），以及脚本里可能写文件 / 执行命令的 w W e
// （分不清是命令还是普通字母，含这些字母的脚本一律不算只读）。
func sedReadOnly(args []string) bool {
	var scripts []string
	explicit := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if !explicit && i+1 < len(args) {
				scripts = append(scripts, args[i+1])
			}
			i = len(args)
		case strings.HasPrefix(a, "--in-place"), strings.HasPrefix(a, "--file"), shortHas(a, 'i'), shortHas(a, 'f'):
			return false
		case a == "-e" || a == "--expression":
			if i+1 >= len(args) {
				return false
			}
			explicit = true
			scripts = append(scripts, args[i+1])
			i++
		case strings.HasPrefix(a, "--expression="):
			explicit = true
			scripts = append(scripts, strings.TrimPrefix(a, "--expression="))
		case shortHas(a, 'e'): // -ne 'script' 之类
			if i+1 >= len(args) {
				return false
			}
			explicit = true
			scripts = append(scripts, args[i+1])
			i++
		case strings.HasPrefix(a, "-") && a != "-":
		default:
			if !explicit {
				scripts = append(scripts, a)
				explicit = true // 第一个非选项参数是脚本，之后是文件
			}
		}
	}
	if len(scripts) == 0 {
		return false
	}
	for _, s := range scripts {
		if strings.ContainsAny(s, "wWe") {
			return false
		}
	}
	return true
}

// sortReadOnly -o / --output 写文件；--compress-program 执行别的程序。
func sortReadOnly(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "--compress") || shortHas(a, 'o') {
			return false
		}
	}
	return true
}

// treeReadOnly -o 写文件。
func treeReadOnly(args []string) bool {
	for _, a := range args {
		if shortHas(a, 'o') {
			return false
		}
	}
	return true
}

var gitReadOnlySubs = map[string]bool{"status": true, "log": true, "diff": true, "show": true, "rev-parse": true, "ls-files": true}

// gitReadOnly 子命令必须紧跟 git（-C、-c 等全局选项可以改配置、执行命令，不认），且不带 --output（写文件）、
// --ext-diff（执行外部 diff 程序）。
func gitReadOnly(args []string) bool {
	if len(args) == 0 || !gitReadOnlySubs[args[0]] {
		return false
	}
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "--ext-diff") {
			return false
		}
	}
	return true
}
