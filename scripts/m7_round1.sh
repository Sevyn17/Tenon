#!/usr/bin/env bash
# M7.1 第一轮自测：在 kernel/ 的副本上用真实 DeepSeek 跑几件日常编码任务，输出留给人工看。
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
export DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-$TENON_API_KEY}"
unset TENON_BASE_URL TENON_MODEL
EXE="$LOCALAPPDATA/tenon/tenon.exe"
go build -C ../client -o "$EXE" ./tenon || exit 1
W=$(mktemp -d)
cp -r . "$W/k"; rm -rf "$W/k/.tenon" "$W/k/bin" "$W/k/.env"
cd "$W/k" && git init -q && git add -A && git commit -qm base
run() {
  echo "=================== $1"
  ( printf '%s\n' "$1"; sleep 2; printf '/stats\n/exit\n' ) | timeout 600 "$EXE" -model deepseek-flash -dir . -yes -session r1 2>&1 | tail -c 4000
  echo; git status --short; git diff --stat
}
run "redact 包的 MinValueLen 是干什么的？在哪里用到？简短回答，给出文件和行号。"
run "给 modules/tools/fs/search.go 的 globMatch 补一个表驱动测试（新文件 modules/tools/fs/glob_extra_test.go），覆盖 **、*、? 和不匹配的情况，跑 go test ./modules/tools/fs/ 确认通过。"
run "apps/tenon/main.go 里 env 函数没有注释，给它加一行中文 doc 注释，然后 go vet ./apps/tenon 确认。"
run "刚才你一共改了哪些文件？用 git 确认后回答。"
echo "WORKDIR $W/k"
