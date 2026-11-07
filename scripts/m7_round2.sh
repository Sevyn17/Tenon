#!/usr/bin/env bash
# M7.1 第二轮自测（缺省 auto 档位，不加 -yes；确认一律回答 n）：在 framework/ + client/ 的副本上用真实 DeepSeek 跑几件日常编码任务，输出留给人工看。
set -u
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a
export DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-$TENON_API_KEY}"
unset TENON_BASE_URL TENON_MODEL
EXE="$LOCALAPPDATA/tenon/tenon.exe"
go build -C ../client -o "$EXE" ./tenon || exit 1
W=$(mktemp -d)
mkdir -p "$W/r"; cp -r . "$W/r/framework"; cp -r ../client "$W/r/client"
rm -rf "$W/r/framework/.tenon" "$W/r/framework/bin" "$W/r/framework/.env" "$W/r/framework/web/node_modules"
cd "$W/r" && git init -q && git add -A && git commit -qm base
run() {
  echo "=================== $1"
  ( printf '%s\n' "$1"; sleep 2; printf '/stats\n/exit\n' ) | timeout 600 "$EXE" -model deepseek-flash -dir . -session r2 2>&1 | tail -c 4000
  echo; git status --short; git diff --stat
}
run "redact 包的 MinValueLen 是干什么的？在哪里用到？简短回答，给出文件和行号。"
run "给 framework/modules/tools/fs/search.go 的 globMatch 补一个表驱动测试（新文件 framework/modules/tools/fs/glob_extra_test.go），覆盖 **、*、? 和不匹配的情况，在 framework/ 下跑 go test ./modules/tools/fs/ 确认通过。"
run "client/tenon/main.go 里 env 函数没有注释，给它加一行中文 doc 注释，然后在 client/ 下 go vet ./tenon 确认。"
run "刚才你一共改了哪些文件？用 git 确认后回答。"
echo "WORKDIR $W/r"
