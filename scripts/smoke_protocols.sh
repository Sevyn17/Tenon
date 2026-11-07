#!/usr/bin/env bash
# M7 冒烟：同一 DeepSeek 账号的三种协议（chat completions / responses / anthropic）各跑一个多步工具任务。
# 用法：bash scripts/smoke_protocols.sh [model]（在 framework/ 下执行；key 读自 framework/.env，不回显）
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
export DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-$TENON_API_KEY}"
unset TENON_BASE_URL TENON_MODEL
M="${1:-deepseek-flash}"
go build -C ../client -o /tmp/tenon-m7.exe ./tenon || exit 1
for p in deepseek deepseek-responses deepseek-anthropic; do
  W=$(mktemp -d)
  printf 'hello\n' > "$W/a.txt"
  echo "===== $p/$M"
  ( printf '%s\n' "读 a.txt，把内容倒序写到 b.txt（只要倒序后的单词，不要换行），再读 b.txt 确认，最后只回答 b.txt 的内容。"; sleep 2; printf '/stats\n/exit\n' ) \
    | timeout 300 /tmp/tenon-m7.exe -model "$p/$M" -dir "$W" -yes -session smoke > "$W/out.log" 2>&1
  tail -c 2500 "$W/out.log"
  echo
  if grep -q '回合结束：Failed' "$W/out.log"; then echo "FAIL $p: turn failed"
  elif [ "$(cat "$W/b.txt" 2>/dev/null)" = "olleh" ]; then echo "PASS $p"; else echo "FAIL $p: b.txt=$(cat "$W/b.txt" 2>/dev/null)"; fi
done
