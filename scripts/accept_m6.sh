#!/usr/bin/env bash
# M6 验收（真实 DeepSeek）：沙箱拒越界写；接一个真实 MCP server；子智能体完成一次委派。
# 用法：bash scripts/accept_m6.sh（在 kernel/ 下执行；key 读自 kernel/.env，不回显）
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
go build -o /tmp/tenon-m6.exe ./apps/tenon || exit 1
W=$(mktemp -d)
mkdir -p "$W/ws/.tenon"
printf 'hello from workspace\n' > "$W/ws/note.txt"
WS=$(cygpath -m "$W/ws"); OUT=$(cygpath -m "$W/outside.txt")
cat > "$W/ws/.tenon/mcp.toml" <<EOF
[[server]]
name = "fs"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "$WS"]
EOF
# 插件开关后 mcp / subagent 缺省关：用 -profile 打开；项目 mcp.toml 需项目受信（-trust-project）；
# -yes 即 full 档位（沙箱随档位放开），这里固定 -sandbox workspace-write 验越界写。
cat > "$W/profile.toml" <<EOF
[[plugin]]
name = "mcp"
enabled = true
[[plugin]]
name = "subagent"
enabled = true
EOF
ARGS=(-base https://api.deepseek.com/v1 -model deepseek-chat -dir "$W/ws" -yes -sandbox workspace-write -trust-project -profile "$(cygpath -m "$W/profile.toml")")

run() { # $1 名称 $2 会话 $3 任务
  echo "===== $1"
  ( printf '%s\n' "$3"; sleep 2; printf '/exit\n' ) | timeout 400 /tmp/tenon-m6.exe "${ARGS[@]}" -session "$2" 2>&1 | tail -c 4000
  echo
}

run "1 沙箱越界写" sb "用 shell 工具执行这条命令（原样执行，不要换别的工具，失败也不要申请更宽的权限）：Set-Content -Path '$OUT' -Value pwned ；然后告诉我命令输出和是否成功。"
if [ -e "$W/outside.txt" ]; then echo "FAIL: 越界文件被写入"; else echo "PASS: 越界文件不存在"; fi

run "2 真实 MCP" mcp "只用 MCP 工具（名字以 mcp__fs__ 开头，可先用 tool_search 找）列出允许目录里的文件，并读出 note.txt 的内容原样告诉我。"

run "3 子智能体委派" sub "用 subagent 工具委派一个子任务：统计工作区 note.txt 的字符数（不含换行）。拿到子智能体结果后告诉我数字和子会话 id。"
echo "===== 会话行"
sqlite3 "$W/ws/.tenon/tenon.db" "select id, parent_id, origin from sessions" 2>/dev/null || ls "$W/ws/.tenon"
