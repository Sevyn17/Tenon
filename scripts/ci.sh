#!/usr/bin/env bash
# 框架本地检查：gofmt、vet、全量测试（含回归快照、公开面快照、示例）、改动包的 -race。
# 不需要网络与 API key。用法：在仓库根运行 `bash scripts/ci.sh`。
set -euo pipefail
cd "$(dirname "$0")/.."
echo "== gofmt"
bad=$(gofmt -l .)
if [ -n "$bad" ]; then echo "未 gofmt：$bad"; exit 1; fi
echo "== 文件名（Go 模块 zip 只收字母、数字和 ASCII !#\$%&()+,-.=@[]^_{}~ 空格，全角标点会让 go get 失败）"
badpaths=$(git ls-files -z | perl -CS -0ne 'chomp; for $c (split //) { if (ord($c) < 128) { next if $c =~ m{[A-Za-z0-9!#$%&()+,\-.=@\[\]^_{}~ /]} } else { next if $c =~ /\p{L}/ } print "  $_\n"; last }')
if [ -n "$badpaths" ]; then echo "文件名含 go 模块不接受的字符："; echo "$badpaths"; exit 1; fi
echo "== 文档链接"
if command -v perl >/dev/null 2>&1; then
  perl scripts/doclinks.pl
elif command -v python3 >/dev/null 2>&1; then
  python3 scripts/doclinks.py
else
  echo "缺 perl 与 python3，跳过文档链接检查" >&2
fi
echo "== go vet"
go vet ./...
echo "== go test"
go test -count=1 ./...
echo "== go test -race（宿主、存储、会话、工具、权限、装配、扩展进程）"
go test -race -count=1 ./runtime/host/ ./runtime/store/... ./runtime/session/ ./runtime/toolhub/ ./runtime/agent/ ./runtime/bus/ ./runtime/inject/ \
  ./modules/security/... ./modules/collab/... ./modules/extend/... ./runtime/extproc/ ./runtime/component/ ./runtime/callctx/ \
  ./tenontest/ ./apphost/ ./transport/...
echo "== 通过"
