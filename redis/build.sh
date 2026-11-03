#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Andrea Casali
#
# Builds redis-server and redis-cli for Covo from the official source
# tarball (SHA-256 from github.com/redis/redis-hashes, checked first),
# without the optional modules or TLS: Covo's Redis listens on 127.0.0.1
# only. Only macOS's own libraries are linked. Smoke-tested with PING, SET
# and GET.
#
# usage: build.sh <version> <sha256> <out dir>
set -euo pipefail

version=$1 sha=$2
mkdir -p "$3"
out=$(cd "$3" && pwd)
arch=$(uname -m)
work=$(mktemp -d)
cd "$work"

curl -fsSL -o src.tar.gz "https://download.redis.io/releases/redis-$version.tar.gz"
echo "$sha  src.tar.gz" | shasum -a 256 -c -
tar xzf src.tar.gz
cd "redis-$version"
export MACOSX_DEPLOYMENT_TARGET=14.0
# Only src/: the core server and tools, not the optional modules
# (RedisJSON, Search…), which need GNU Make 4 and Rust.
make -C src -j"$(sysctl -n hw.ncpu)" BUILD_TLS=no all >/dev/null

mkdir -p "$work/redis"
cp src/redis-server src/redis-cli "$work/redis/"
cp LICENSE.txt "$work/redis/" 2>/dev/null || cp COPYING "$work/redis/" 2>/dev/null || true
cd "$work/redis"
for f in redis-server redis-cli; do codesign --force --sign - "$f" 2>/dev/null; done
if otool -L redis-server redis-cli | grep -E "^\s+/" | grep -vE "^\s+/(usr/lib|System)/"; then
	echo "linked outside macOS"; exit 1
fi

./redis-server --port 56380 --bind 127.0.0.1 --save "" --appendonly no --daemonize yes --pidfile "$work/r.pid" --logfile "$work/r.log"
trap './redis-cli -p 56380 shutdown nosave >/dev/null 2>&1 || true' EXIT
for _ in $(seq 50); do ./redis-cli -p 56380 ping 2>/dev/null | grep -q PONG && break; sleep 0.1; done
./redis-cli -p 56380 set covo ok >/dev/null
[ "$(./redis-cli -p 56380 get covo)" = ok ]
./redis-server --version | grep -q "v=$version"
echo "Redis $version ($arch) works"

name="redis-$version-macos-$arch.tar.gz"
tar --uid 0 --gid 0 --uname root --gname wheel -czf "$out/$name" redis-server redis-cli $(ls LICENSE.txt COPYING 2>/dev/null)
(cd "$out" && shasum -a 256 "$name" >"$name.sha256")
ls -l "$out"
