package shell

// 命令拆分与「启动器停止线」（06 - 工具权限设计方案 §十）。
//
// 终端是开集：参数本身是另一门语言的程序，文本匹配只是减少询问次数的便利，不构成边界。
// 拆段逻辑属于 shell 插件（它知道本平台的语法），权限插件只拿 Subject 的结果去比对规则。

import (
	"strings"
)

// safeHeads 无副作用、不需要规则即可出现在复合命令里的命令（不区分大小写）。
var safeHeads = map[string]bool{
	"cd": true, "set-location": true, "pwd": true, "get-location": true,
	"echo": true, "write-output": true, "write-host": true, "true": true,
	"select-object": true, "out-string": true, "out-null": true,
	"format-table": true, "format-list": true, "measure-object": true,
	"head": true, "tail": true, "wc": true,
}

// launchers 启动器类命令：它们执行的是别处的程序或脚本，命令文本看不出实际行为。
// 这类命令永不由「本次会话内都允许」生成的前缀规则放行——否则 `npm run test:*` 实际上
// 授权了 package.json 作者能写的任何东西。档位文件里用户显式写的 allow 仍然有效（用户是权威）。
var launchers = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true,
	"cmd": true, "cmd.exe": true, "powershell": true, "powershell.exe": true, "pwsh": true, "pwsh.exe": true,
	"node": true, "deno": true, "bun": true, "python": true, "python3": true, "py": true,
	"ruby": true, "perl": true, "php": true, "lua": true, "osascript": true,
	"npm": true, "npx": true, "pnpm": true, "yarn": true, "pip": true, "pipx": true, "uv": true, "uvx": true,
	"make": true, "cmake": true, "just": true, "task": true, "mvn": true, "gradle": true, "cargo": true,
	"xargs": true, "env": true, "eval": true, "exec": true, "sudo": true, "doas": true, "runas": true,
	"start": true, "start-process": true, "invoke-expression": true, "iex": true, "source": true, ".": true,
	"docker": true, "podman": true, "kubectl": true, "ssh": true,
	// 执行任意代码的其他入口：子系统 / 运行时 / Windows 脚本宿主。
	"wsl": true, "java": true, "node-gyp": true, "tsx": true, "ts-node": true,
	"cscript": true, "wscript": true, "mshta": true, "rundll32": true, "regsvr32": true,
	"msiexec": true, "conhost": true, "busybox": true,
	// 包一层再跑别的命令的：前缀规则会放行被包的任意命令。
	"nohup": true, "setsid": true, "time": true, "watch": true, "nice": true, "chroot": true,
	"su": true, "gsudo": true,
}

// harmless 可直接去掉的重定向（只合并 / 丢弃输出，不写文件）。
var harmless = []string{"2>&1", "2>$null", ">$null", "2>/dev/null", ">/dev/null", "*>$null"}

// SplitCommand 把命令按 ; && || | 换行拆成简单命令。
// 含命令替换、脚本块、后台 &、写文件重定向时 ok=false（无法可靠拆分，调用方按整条命令处理）。
func SplitCommand(c string) (segs []string, ok bool) {
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	var quote byte
	for i := 0; i < len(c); i++ {
		ch := c[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else if quote == '"' && (ch == '`' || (ch == '$' && i+1 < len(c) && c[i+1] == '(')) {
				return nil, false // 双引号内的命令替换 / 转义
			}
			cur.WriteByte(ch)
			continue
		}
		rest := c[i:]
		if h := harmlessAt(rest); h != "" {
			i += len(h) - 1
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			cur.WriteByte(ch)
		case ';', '\n', '\r':
			flush()
		case '|', '&':
			if i+1 < len(c) && c[i+1] == ch { // && ||
				i++
				flush()
			} else if ch == '|' {
				flush()
			} else {
				return nil, false // 单个 &：后台（bash）/ 调用运算符（PowerShell）
			}
		case '`', '{', '}', '<', '>':
			return nil, false
		case '$':
			if i+1 < len(c) && c[i+1] == '(' {
				return nil, false
			}
			cur.WriteByte(ch)
		default:
			cur.WriteByte(ch)
		}
	}
	if quote != 0 {
		return nil, false
	}
	flush()
	return segs, len(segs) > 0
}

func harmlessAt(s string) string {
	for _, h := range harmless {
		if strings.HasPrefix(s, h) {
			return h
		}
	}
	return ""
}

// head 简单命令的首词（小写）。
func head(seg string) string {
	f := strings.Fields(seg)
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(strings.Trim(f[0], `"'`))
}

// Safe 简单命令是否无需规则（只读、不写文件、不起别的程序）。
func Safe(seg string) bool { return safeHeads[head(seg)] }

// Launcher 简单命令是否是启动器类命令（见 launchers）。
func Launcher(seg string) bool {
	h := head(seg)
	// 带路径的解释器（/usr/bin/python3、C:\Python\python.exe）按文件名看。
	if i := strings.LastIndexAny(h, `/\`); i >= 0 {
		h = h[i+1:]
	}
	if launchers[h] {
		return true
	}
	// Windows 可执行后缀：python.exe、npm.cmd、build.bat 与不带后缀的同一个程序。
	for _, ext := range []string{".exe", ".cmd", ".bat", ".com", ".ps1"} {
		if b, ok := strings.CutSuffix(h, ext); ok && launchers[b] {
			return true
		}
	}
	// 版本号后缀：python3.12、node18 这类。
	if b := strings.TrimRight(h, "0123456789."); b != h && launchers[b] {
		return true
	}
	// 带子命令才执行任意代码的工具：go run、dotnet run、git -c（可配置 core.sshCommand 等）。
	f := strings.Fields(strings.ToLower(seg))
	if len(f) >= 2 {
		switch h {
		case "go", "dotnet", "deno", "bun":
			return f[1] == "run" || f[1] == "exec" || f[1] == "eval"
		case "git":
			return f[1] == "-c" || strings.HasPrefix(f[1], "--config") || f[1] == "--exec-path"
		}
	}
	return false
}

// Prefix 简单命令的规则前缀：首词，第二词是子命令（不以 - / 引号开头、不像路径）时带上。
func Prefix(seg string) string {
	f := strings.Fields(seg)
	if len(f) == 0 {
		return ""
	}
	n := 1
	if len(f) > 1 {
		w := f[1]
		if !strings.ContainsAny(w[:1], "-\"'$@") && !strings.ContainsAny(w, `/\.:=*`) {
			n = 2
		}
	}
	return strings.Join(f[:n], " ")
}
