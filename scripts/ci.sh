#!/usr/bin/env bash
# kernel 本地检查（计划/01 第一节「每步的完成标志」1–2 条）：vet、全量测试（含两套回归快照）、
# 改动包的 -race。不需要网络与 API key。用法：在 kernel/ 下运行 `bash scripts/ci.sh`。
set -euo pipefail
cd "$(dirname "$0")/.."
echo "== gofmt"
bad=$(gofmt -l . ../client)
if [ -n "$bad" ]; then echo "未 gofmt：$bad"; exit 1; fi
echo "== go vet"
go vet ./...
echo "== go test"
go test -count=1 ./...
echo "== go test -race（宿主、存储、会话、工具、权限、装配）"
go test -race -count=1 ./runtime/host/ ./runtime/store/... ./runtime/session/ ./runtime/toolhub/ ./runtime/agent/ ./runtime/bus/ ./runtime/inject/ \
  ./modules/security/... ./modules/collab/... ./tenontest/
echo "== client：go vet / go test / -race（装配）"
cd ../client
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./internal/boot/
echo "== 通过"
