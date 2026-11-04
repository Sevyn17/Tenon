package cmdline

import "testing"

// B8：启动器停止线（「本次会话内都允许」不为它们生成前缀规则）要认得 Windows 可执行后缀、
// 版本号后缀，以及带子命令才执行任意代码的工具。
func TestLauncherVariants(t *testing.T) {
	for _, seg := range []string{
		"python.exe x.py", `C:\Py\python.exe x.py`, "node.exe a.js", "bash.exe -c ls", "npm.cmd test",
		"python3.12 x.py", "/usr/bin/python3 x.py",
		"wsl ls", "java -jar a.jar", "cscript a.vbs", "mshta x.hta", "rundll32 a.dll,f",
		"go run ./cmd", "dotnet run", "git -c core.sshCommand=x fetch", "nohup make",
	} {
		if !Launcher(seg) {
			t.Errorf("%q 应算启动器", seg)
		}
	}
	for _, seg := range []string{"go test ./...", "go build", "git status", "git push", "ls -la", "dotnet build", "timeout 5"} {
		if Launcher(seg) {
			t.Errorf("%q 不应算启动器（否则正常命令也生成不了会话规则）", seg)
		}
	}
}
