#!/usr/bin/env bash
# P0 退出判据 1 验收：在 kernel/ 副本里注入一个真实 bug，用缺省档位（auto，不加 -yes）让真实 DeepSeek 找出并修好。
# 用法：bash scripts/accept_p0.sh（在 kernel/ 下执行；key 读自 kernel/.env，不回显）
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
export DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-$YBASE_API_KEY}"
unset YBASE_BASE_URL YBASE_MODEL
EXE="$LOCALAPPDATA/ybase/ybase.exe"
go build -o "$EXE" ./apps/ybase || exit 1
W=$(mktemp -d)
cp -r . "$W/k"; rm -rf "$W/k/.ybase" "$W/k/bin" "$W/k/.env"
cd "$W/k" || exit 1
# 注入 bug：Glob 末段改成前缀匹配（规则 edit(src/**.go) 之类会误判）。
sed -i 's/\treturn strings.HasSuffix(s, last)/\treturn strings.HasPrefix(s, last)/' plugins/permission/permission.go
git init -q && git add -A && git commit -qm base
echo "== 注入后 go test ./plugins/permission"; go test ./plugins/permission 2>&1 | tail -5
( printf '%s\n' 'go test ./plugins/permission 现在失败了。找出原因并修复产品代码（不要改测试），修完再跑一遍 go test ./plugins/permission 确认通过，最后用一句话说明根因。'; sleep 2; printf '/stats\n/exit\n' ) |
  timeout 900 "$EXE" -model deepseek-flash -dir . -session p0 2>&1 | tail -c 5000
echo; echo "== git diff"; git diff --stat; git diff | head -40
echo "== 修复后 go test ./plugins/permission"; go test ./plugins/permission 2>&1 | tail -3
echo "WORKDIR $W/k"
