#!/bin/sh
set -eu

export GOTOOLCHAIN=local
export GOWORK=off

required_go=go1.26.10
version=1.12.12
source_sum='h1:brSb4zdL5CfqXB0ss1jrT+srPUbKanyWhbswkte/z5Y='
tags=with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale

if [ "$#" -ne 1 ]; then
    echo '用法: build-sing-box.sh /绝对路径/sing-box' >&2
    exit 1
fi
case "$1" in
    /*) output=$1 ;;
    *) echo '输出文件必须使用绝对路径' >&2; exit 1 ;;
esac
if [ "${SINGBOX_VERSION:-$version}" != "$version" ]; then
    echo "仅支持已校验的 sing-box $version 源码" >&2
    exit 1
fi
if [ "$(go env GOVERSION)" != "$required_go" ]; then
    echo "必须使用 $required_go 编译 sing-box" >&2
    exit 1
fi

# 校验模块归档，避免版本标签变化或误用未修复的预编译内核。
metadata=$(go mod download -json "github.com/sagernet/sing-box@v$version")
if ! printf '%s\n' "$metadata" | grep -F "\"Sum\": \"$source_sum\"" >/dev/null; then
    echo 'sing-box 源码归档校验失败' >&2
    exit 1
fi
source_dir="$(go env GOMODCACHE)/github.com/sagernet/sing-box@v$version"
mkdir -p "$(dirname "$output")"
CGO_ENABLED=0 go build -C "$source_dir" -mod=readonly -trimpath -buildvcs=false \
    -tags "$tags" \
    -ldflags "-X github.com/sagernet/sing-box/constant.Version=$version -s -w -buildid=" \
    -o "$output" ./cmd/sing-box

"$(dirname "$0")/verify-go-build.sh" "$output"
