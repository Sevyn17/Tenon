# 启动 tenon：读 framework/.env 里的 key，再在指定目录里开会话。
# 用法（PowerShell）：
#   & "<Tenon>\framework\scripts\tenon.ps1" -Dir D:\某项目 [-Session 名字] [-Model deepseek-flash] [其它 tenon 参数]
param(
  [string]$Dir = (Get-Location).Path,
  [string]$Session = "default",
  [string]$Model = "deepseek-flash",
  [Parameter(ValueFromRemainingArguments = $true)] [string[]]$Rest
)
$kernel = Split-Path -Parent $PSScriptRoot
Get-Content (Join-Path $kernel ".env") | ForEach-Object {
  if ($_ -match '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$') { Set-Item "env:$($Matches[1])" $Matches[2].Trim() }
}
if (-not $env:DEEPSEEK_API_KEY) { $env:DEEPSEEK_API_KEY = $env:TENON_API_KEY }
# 走内置目录里的 deepseek 提供方，不用 .env 里的 base/model 覆盖
Remove-Item env:TENON_BASE_URL, env:TENON_MODEL -ErrorAction SilentlyContinue
[Console]::OutputEncoding = [Text.Encoding]::UTF8
# 可执行文件放在 %LOCALAPPDATA%\tenon：工作区被沙箱标成 Low 完整性后，放在工作区里的 exe
# 会以 Low 完整性启动，连 .tenon 都建不了。每次启动先重新编译，保证用的是最新代码。
$exe = Join-Path $env:LOCALAPPDATA "tenon\tenon.exe"
Push-Location $kernel
go build -C ../client -o $exe ./tenon
$ok = $LASTEXITCODE -eq 0
Pop-Location
if (-not $ok) { Write-Error "编译失败"; exit 1 }
& $exe -dir $Dir -session $Session -model $Model @Rest
