#!/usr/bin/env bash
# 框架本地检查：gofmt、vet、全量测试（含回归快照、公开面快照、示例）、改动包的 -race。
# 不需要网络与 API key。用法：在仓库根运行 `bash scripts/ci.sh`。
set -euo pipefail
cd "$(dirname "$0")/.."
echo "== gofmt"
bad=$(gofmt -l .)
if [ -n "$bad" ]; then echo "未 gofmt：$bad"; exit 1; fi
echo "== go vet"
go vet ./...
echo "== go test"
go test -count=1 ./...
echo "== go test -race（宿主、存储、会话、工具、权限、装配）"
go test -race -count=1 ./runtime/host/ ./runtime/store/... ./runtime/session/ ./runtime/toolhub/ ./runtime/agent/ ./runtime/bus/ ./runtime/inject/ \
  ./modules/security/... ./modules/collab/... ./tenontest/ ./apphost/ ./transport/...
echo "== 通过"
