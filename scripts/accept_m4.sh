#!/usr/bin/env bash
# M4 验收（真实 DeepSeek）：等确认时杀进程，重启后仍在等；批准后按原调用执行一次。
# 用法：bash scripts/accept_m4.sh（在 kernel/ 下执行；key 读自 kernel/.env，不回显）
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
go build -o /tmp/tenon-m4.exe ./apps/tenon || exit 1
W=$(mktemp -d)
echo hello > "$W/a.txt"
ARGS=(-base https://api.deepseek.com/v1 -model deepseek-chat -dir "$W" -mode ask)

# 第 1 次：模型要写文件 → 等确认 → 杀进程。
( printf '读 a.txt，把内容反转后写入 b.txt。\n'; sleep 90 ) | /tmp/tenon-m4.exe "${ARGS[@]}" > "$W/run1.log" 2>&1 &
PID=$!
for i in $(seq 1 60); do sleep 1; grep -q '允许执行' "$W/run1.log" && break; done
sleep 1
kill -9 $PID 2>/dev/null; taskkill //F //IM tenon-m4.exe >/dev/null 2>&1
echo "== run1（等确认时被杀）"; cat "$W/run1.log"
echo "== b.txt 存在？"; [ -f "$W/b.txt" ] && echo "是（错误）" || echo "否（正确）"

# 第 2 次：重启，应重新提示同一调用；回答 y。
( sleep 8; printf 'y\n'; sleep 30; printf '/exit\n' ) | timeout 120 /tmp/tenon-m4.exe "${ARGS[@]}" > "$W/run2.log" 2>&1
echo "== run2（重启后）"; cat "$W/run2.log"
echo "== b.txt:"; cat "$W/b.txt" 2>&1
echo "== 提示次数（run2）：$(grep -c '允许？' "$W/run2.log")"
