#!/usr/bin/env bash
# 完整构建 viewit:前端构建 → 预压缩嵌入资源 → 编译并裁剪单二进制。
set -euo pipefail

cd "$(dirname "$0")"

# 1. 前端构建(仅首次安装依赖,之后复用 node_modules)
if [ ! -d frontend/node_modules ]; then
  (cd frontend && npm install)
fi
(cd frontend && npm run build)

# 2. 预 gzip 成 frontend/dist.gz(BestCompression),供 go:embed 嵌入
go generate .

# 3. 编译并裁剪二进制:
#    CGO_ENABLED=0  纯 Go DNS resolver,静态链接,摆脱 libc
#    -trimpath      去除本地源码路径,构建可复现
#    -s -w          剥离符号表与 DWARF 调试信息
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o viewit .

ls -lh viewit
