#!/bin/sh
set -eu

export GOTOOLCHAIN=local
export GOWORK=off
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ "$#" -ne 1 ]; then
    echo '用法: build-release.sh /绝对路径/发行文件' >&2
    exit 1
fi
case "$1" in
    /*) output=$1 ;;
    *) echo '输出文件必须使用绝对路径' >&2; exit 1 ;;
esac
test -s "$root/frontend/dist/index.html" || {
    echo '请先构建前端' >&2
    exit 1
}
# 同一工作区禁止并发交叉编译，避免把错误架构内核嵌入管理程序。
lock="$root/internal/kernel/.build-lock"
mkdir "$lock" || { echo '发行构建已在运行' >&2; exit 1; }
cleanup() { rm -f "$root/internal/kernel/sing-box"; rmdir "$lock"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
"$root/scripts/build-sing-box.sh" "$root/internal/kernel/sing-box"
mkdir -p "$(dirname "$output")"
CGO_ENABLED=0 go build -C "$root" -mod=readonly -trimpath \
    -tags frontend_dist,bundled_kernel -ldflags='-s -w' -o "$output" ./backend
"$root/scripts/verify-go-build.sh" "$output"
