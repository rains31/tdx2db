#!/usr/bin/env bash
#
# 构建 tdx2db 二进制（默认输出到当前目录的 ./tdx2db）。
#
# 为什么需要这个脚本，而不是直接用 makefile：
#   1. 部署环境（如远程服务器上的代码副本）通过 rsync 同步时是非 git 副本，
#      且曾残留一个损坏的 .git 目录（缺 HEAD/config），会让 go 的
#      VCS stamping 报错 "error obtaining VCS status: exit status 128" 而直接构建失败。
#      这里统一加 -buildvcs=false，构建结果不再依赖目录里的 .git 状态。
#   2. tdx/embed（datatool 等）已随源码存在，不需要 makefile 里 download 那条联网链路。
#   3. 统一注入 version/commit/date，方便用 `tdx2db version` 确认线上跑的是哪个版本。
#
# 用法：
#   ./build.sh                          # 就地构建 ./tdx2db
#   OUT=/tmp/tdx2db-new ./build.sh      # 输出到指定路径
#   VERSION=dev+myfix ./build.sh        # 自定义版本号
#
# 注意：本项目的 duckdb 依赖需要 cgo，跨平台构建（如 macOS 上编 linux）需要对应
# 交叉编译工具链，因此默认按本机平台构建，不做 GOOS/GOARCH 强制。
set -euo pipefail

cd "$(dirname "$0")"

VERSION="${VERSION:-dev+local}"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
OUT="${OUT:-tdx2db}"

echo "==> building ${OUT}"
echo "    version=${VERSION} commit=${COMMIT} date=${DATE} go=$(go version | awk '{print $3}')"

CGO_ENABLED="${CGO_ENABLED:-1}" go build \
    -buildvcs=false \
    -ldflags "-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o "${OUT}" .

echo "==> done: $(ls -la "${OUT}" | awk '{print $5" bytes"}')"
"./${OUT}" version
