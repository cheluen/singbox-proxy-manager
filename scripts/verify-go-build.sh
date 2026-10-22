#!/bin/sh
set -eu

if [ "$#" -eq 0 ]; then
    echo '用法: verify-go-build.sh /绝对路径/二进制 [...]' >&2
    exit 1
fi
for binary in "$@"; do
    case "$binary" in
        /*) ;;
        *) echo '二进制文件必须使用绝对路径' >&2; exit 1 ;;
    esac
    metadata=$(GOTOOLCHAIN=local go version -m "$binary")
    version=$(printf '%s\n' "$metadata" | awk 'NR == 1 { print $NF }')
    if [ "$version" != go1.26.10 ]; then
        echo "$binary 的编译器版本不是 go1.26.10: $version" >&2
        exit 1
    fi
    printf '%s\n' "$binary: $version"
done
