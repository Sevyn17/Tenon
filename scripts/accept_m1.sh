#!/usr/bin/env bash
# M1 验收：真实模型（DeepSeek）回合进行中杀进程，重启后续跑。
# 用法：bash scripts/accept_m1.sh（在 kernel/ 下执行；key 读自 kernel/.env，不回显）
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
go build -o /tmp/ybase-m1.exe ./apps/ybase || exit 1
W=$(mktemp -d)
echo "workdir=$W"

# 第 1 次：让模型执行一条慢命令（约 20 秒），在命令执行中杀进程。
( printf '请用 shell 执行这条命令：Start-Sleep -Seconds 20; "done" | Out-File slow.txt ，然后告诉我 slow.txt 的内容。\n'; sleep 60 ) |
  /tmp/ybase-m1.exe -base https://api.deepseek.com/v1 -model deepseek-chat -dir "$W" -yes > "$W/run1.log" 2>&1 &
PID=$!
for i in $(seq 1 40); do
  sleep 1
  grep -q '⚙ shell' "$W/run1.log" && break
done
sleep 3
kill -9 $PID 2>/dev/null; taskkill //F //IM ybase-m1.exe >/dev/null 2>&1
echo "== run1（被杀）"; cat "$W/run1.log"
echo "== run_state after kill"
/tmp/ybase-m1.exe -dir "$W" export default 2>/dev/null | head -c 400; echo

# 第 2 次：重启同一会话，应自动续跑上次中断的回合。
echo "/exit" | timeout 120 /tmp/ybase-m1.exe -base https://api.deepseek.com/v1 -model deepseek-chat -dir "$W" -yes > "$W/run2.log" 2>&1
echo "== run2（重启续跑）"; cat "$W/run2.log"
echo "== slow.txt exists?"; ls "$W"
