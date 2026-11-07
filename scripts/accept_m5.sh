#!/usr/bin/env bash
# M5 验收（真实 DeepSeek）：在本仓库 kernel/ 的副本上用 tenon 完成 3 个真实小任务。
# 用法：bash scripts/accept_m5.sh（在 framework/ 下执行；key 读自 framework/.env，不回显）
# 每个任务一次进程：发出任务后立刻 /exit（回合结束后才退出），同一会话续聊。
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
go build -C ../client -o /tmp/tenon-m5.exe ./tenon || exit 1
W=$(mktemp -d)
cp -r . "$W/kernel"; rm -rf "$W/kernel/.tenon" "$W/kernel/.env"
cd "$W/kernel" && git init -q . && cd - >/dev/null
ARGS=(-base https://api.deepseek.com/v1 -model deepseek-chat -dir "$W/kernel" -session m5 -yes)

run() { # $1 名称 $2 任务
  echo "===== $1"
  ( printf '%s\n' "$2"; sleep 2; printf '/exit\n' ) | timeout 400 /tmp/tenon-m5.exe "${ARGS[@]}" 2>&1 | tail -c 6000
  echo
}

run "任务1 只读定位" '工具执行的默认超时、最大并发和产物落盘阈值分别是多少？在哪个文件定义？只回答数字和 文件:行号。'
run "任务2 修改+测试" '给 toolhub 包的 Preview 函数补一个单元测试：覆盖 head_tail 模式在文本超长时保留首尾。写在 toolhub/preview_extra_test.go，然后用 shell 运行 go test ./toolhub/ 确认通过。'
run "任务3 文档生成" '列出 tools 包里每个对模型暴露的工具名和它的 Risk 等级，写成 markdown 表格保存到 docs/tool-risks.md（新文件），完成后读一遍确认。'
echo "===== /stats"
( printf '/stats\n/exit\n' ) | timeout 60 /tmp/tenon-m5.exe "${ARGS[@]}" 2>&1 | tail -c 3000
echo "===== 产物"
ls "$W/kernel/toolhub/" | grep extra
cd "$W/kernel" && go test ./toolhub/ 2>&1 | tail -3
