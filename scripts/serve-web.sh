#!/usr/bin/env bash
# 改完前端后看效果：构建前端 → 重建网页服务（前端经 go:embed 编进二进制，只跑 pnpm build 不会生效）→ 启动。
#
#   bash framework/scripts/serve-web.sh                       # 缺省：127.0.0.1:8765，工作目录 = 当前目录
#   bash framework/scripts/serve-web.sh -dir D:/proj -profile p.toml   # 其余参数原样交给 tenon-server
#
# 端口被占用时直接退出：Windows 上运行中的 exe 被锁住，重建会失败或留下旧文件。先停掉旧的服务（Ctrl+C）再运行。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ADDR="${TENON_ADDR:-127.0.0.1:8765}"
EXT=""
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXT=".exe" ;; esac
OUT="${TENON_SERVER_OUT:-$ROOT/framework/bin/tenon-server$EXT}"

port="${ADDR##*:}"
if command -v netstat >/dev/null 2>&1 && netstat -an 2>/dev/null | grep -E "[:.]$port[[:space:]].*LISTEN" >/dev/null; then
  echo "端口 $port 已被占用：先停掉正在运行的 tenon-server，再重新运行本脚本。" >&2
  exit 1
fi

echo "== 构建前端"
(cd "$ROOT/framework/web" && pnpm build)
echo "== 重建网页服务 → $OUT"
mkdir -p "$(dirname "$OUT")"
(cd "$ROOT/client" && go build -o "$OUT" ./server)

if [ "$#" -eq 0 ]; then set -- -dir "$PWD"; fi
echo "== 启动"
exec "$OUT" -addr "$ADDR" "$@"
