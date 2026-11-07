#!/usr/bin/env bash
# 权限改版验收（真实 DeepSeek）：
#   1 缺省档位 workspace：工作区内 edit / shell 写不确认；越界写被沙箱拒，模型申请升权 → 人拒绝 → 文件不存在
#   2 /mode 切换后重启仍生效
#   3 ask 档位下“本次会话内都允许”：同一命令第二次不再确认
# 用法：bash scripts/accept_perm.sh（在 kernel/ 下执行；key 读自 kernel/.env，不回显）
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
go build -o /tmp/tenon-perm.exe ./apps/tenon || exit 1
W=$(mktemp -d)
mkdir -p "$W/ws"
printf 'hello\n' > "$W/ws/a.txt"
OUT=$(cygpath -m "$W/outside.txt")
printf '[[plugin]]
name = "sandbox"
enabled = true
' > "$W/profile.toml"  # 沙箱缺省关闭（§9.9），验收 1 需要开启
ARGS=(-base https://api.deepseek.com/v1 -model deepseek-chat -dir "$W/ws" -profile "$(cygpath -m "$W/profile.toml")")
pass=0; fail=0
check() { if eval "$2"; then echo "PASS: $1"; pass=$((pass+1)); else echo "FAIL: $1"; fail=$((fail+1)); fi; }

# 输入按时间喂：每行之前等待若干秒（确认提示会吃掉下一行）
feed() { for x in "$@"; do sleep "${x%%|*}"; printf '%s\n' "${x#*|}"; done; }
run() { # $1 会话 $2.. 输入
  local s=$1; shift
  feed "$@" | timeout 300 /tmp/tenon-perm.exe "${ARGS[@]}" -session "$s" 2>&1 | tail -c 3000
  echo
}

echo "===== 1 workspace 档位"
run p1 "0|用 edit 工具把 a.txt 里的 hello 改成 world，然后用 shell 执行 Set-Content -Path b.txt -Value ok。最后用 shell 原样执行 Set-Content -Path '$OUT' -Value pwned ，如果被沙箱拒绝，就按提示申请更宽的沙箱重试一次。" \
  "90|n" "20|n" "20|/exit"
check "edit 免确认写入工作区" "grep -q world '$W/ws/a.txt'"
check "shell 在沙箱内写工作区" "[ -e '$W/ws/b.txt' ]"
check "越界写被拒（升权被人拒绝）" "[ ! -e '$W/outside.txt' ]"

echo "===== 2 /mode 持久"
run p2 "0|/mode read-only" "3|/exit" >/dev/null
out=$(run p2 "0|/mode" "3|/exit")
echo "$out" | grep -E "档位" | head -3
check "重启后档位仍为 read-only" "echo \"\$out\" | grep -q '当前档位：read-only'"

echo "===== 3 本次会话内都允许"
run p3 "0|/mode ask" "2|用 shell 执行 Write-Output hi1 。完成后再用 shell 原样执行同一条命令 Write-Output hi1 一次，然后告诉我两次输出。" \
  "40|a" "60|/exit" > "$W/p3.txt"
tail -c 2000 "$W/p3.txt"
n=$(grep -c "允许？" "$W/p3.txt")
check "只确认一次（实际 $n 次）" "[ '$n' = 1 ]"

echo "===== 结果：$pass 通过，$fail 失败"
/tmp/tenon-perm.exe -dir "$W/ws" sandbox-reset && echo "sandbox-reset OK"
