#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Andrea Casali
#
# Builds a self-contained, relocatable MySQL 5.7 for Benbow (to test old
# sites against the server they ran on) from Oracle's mysql-boost source
# tarball, with OpenSSL 3.0 built from source and linked statically. Oracle
# never shipped 5.7 for Apple Silicon. Only macOS's own libraries are
# linked. The result is smoke-tested: initialize, start, query, dump.
#
# usage: build.sh <mysql version> <sha256> <openssl version> <openssl sha256> <out dir>
# Needs cmake 3.x on PATH (GitHub's macOS runners have it).
set -euo pipefail

version=$1 sha=$2 ssl_version=$3 ssl_sha=$4
mkdir -p "$5"
out=$(cd "$5" && pwd)
arch=$(uname -m) # arm64 or x86_64
work=${WORK:-$(mktemp -d)}
mkdir -p "$work"
cd "$work"
jobs=$(sysctl -n hw.ncpu)
export MACOSX_DEPLOYMENT_TARGET=14.0

# OpenSSL, static.
if [ ! -f "$work/ssl/lib/libssl.a" ]; then
	curl -fsSL -o openssl.tar.gz "https://github.com/openssl/openssl/releases/download/openssl-$ssl_version/openssl-$ssl_version.tar.gz"
	echo "$ssl_sha  openssl.tar.gz" | shasum -a 256 -c -
	rm -rf "openssl-$ssl_version" && tar xzf openssl.tar.gz
	(cd "openssl-$ssl_version" &&
		./Configure "darwin64-$arch-cc" no-shared no-tests --prefix="$work/ssl" --libdir=lib >/dev/null &&
		make -j"$jobs" >/dev/null && make install_sw >/dev/null)
fi

# MySQL. Oracle publishes MD5 only; the SHA-256 is pinned from the first
# download (it matches the one Homebrew pinned for its mysql@5.7).
if [ ! -d "mysql-$version" ]; then
	[ -f src.tar.gz ] || curl -fsSL -o src.tar.gz "https://cdn.mysql.com/archives/mysql-5.7/mysql-boost-$version.tar.gz"
	echo "$sha  src.tar.gz" | shasum -a 256 -c -
	tar xzf src.tar.gz
fi
cd "mysql-$version"
# The bundled zlib 1.2.13 defines fdopen away on Macs, which clashes with
# today's SDK headers (fixed in zlib 1.3).
sed -i '' 's|^ *# *define fdopen(fd,mode) NULL.*$||' extra/zlib/zlib-1.2.13/zutil.h
prefix="$work/mysql"
# Today's libc++ dropped C++98 pieces 5.7 still uses; clang turned some old
# warnings into errors.
cxxflags="-D_LIBCPP_ENABLE_CXX17_REMOVED_FEATURES -D_LIBCPP_ENABLE_CXX20_REMOVED_FEATURES -Wno-error -Wno-register -Wno-deprecated-declarations -Wno-enum-constexpr-conversion -Wno-incompatible-function-pointer-types"
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$prefix" \
	-DCMAKE_OSX_DEPLOYMENT_TARGET=14.0 -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
	-DWITH_BOOST=boost -DWITH_SSL="$work/ssl" -DWITH_ZLIB=system -DWITH_LZ4=bundled -DWITH_EDITLINE=bundled \
	-DWITH_UNIT_TESTS=OFF -DWITH_EMBEDDED_SERVER=OFF -DWITH_EMBEDDED_SHARED_LIBRARY=OFF -DWITHOUT_SERVER=OFF \
	-DWITH_ROUTER=OFF -DWITH_TEST_TRACE_PLUGIN=OFF -DINSTALL_MYSQLTESTDIR= -DENABLED_LOCAL_INFILE=OFF \
	-DWITH_INNODB_MEMCACHED=OFF -DWITH_RAPID=OFF \
	-DCMAKE_C_FLAGS="-Wno-error -Wno-incompatible-function-pointer-types -Wno-int-conversion" -DCMAKE_CXX_FLAGS="$cxxflags" >"$work/cmake.log" 2>&1 ||
	{ tail -40 "$work/cmake.log"; exit 1; }
cmake --build build -j"$jobs" >"$work/build.log" 2>&1 || { grep -E "error|Error" "$work/build.log" | head -40; exit 1; }
cmake --install build >/dev/null

# Keep what Benbow runs: the server, the client, mysqldump, mysqladmin, the
# error messages and charsets.
cd "$prefix"
keep="mysqld mysql mysqldump mysqladmin my_print_defaults"
for f in bin/*; do
	case " $keep " in *" $(basename "$f") "*) ;; *) rm -f "$f" ;; esac
done
rm -rf include lib/*.a lib/pkgconfig lib/plugin/debug docs man mysql-test support-files LICENSE* README* COPYING*
find bin lib share -type l | while read -r link; do
	target=$(realpath "$link")
	rm "$link"
	cp "$target" "$link"
done
for f in bin/* $(find lib -name '*.dylib' -o -name '*.so'); do
	chmod u+w "$f"
	codesign --force --sign - "$f" 2>/dev/null
done
if otool -L bin/* | grep -E "^\s+/" | grep -vE "^\s+/(usr/lib|System)/"; then
	echo "linked outside macOS"; exit 1
fi

# Smoke test from another place, as Benbow will run it.
moved="$work/moved/mysql"
rm -rf "$work/moved" "$work/data"
mkdir -p "$(dirname "$moved")"
cp -R "$prefix" "$moved"
data="$work/data"
sock="$(mktemp -d /tmp/m57.XXXXXX)/mysql.sock" # sockets need a short path
"$moved/bin/mysqld" --no-defaults --basedir="$moved" --lc-messages-dir="$moved/share" --datadir="$data" --initialize-insecure >/dev/null 2>&1
"$moved/bin/mysqld" --no-defaults --basedir="$moved" --lc-messages-dir="$moved/share" --datadir="$data" \
	--port=53307 --bind-address=127.0.0.1 --socket="$sock" 2>"$work/mysqld.err" &
pid=$!
trap 'kill $pid 2>/dev/null || true' EXIT
for _ in $(seq 60); do
	"$moved/bin/mysqladmin" -uroot -h127.0.0.1 -P53307 ping >/dev/null 2>&1 && break
	sleep 1
done
"$moved/bin/mysql" -uroot -h127.0.0.1 -P53307 -N -e "CREATE DATABASE t; CREATE TABLE t.x (v VARCHAR(10)) DEFAULT CHARSET=utf8mb4; INSERT INTO t.x VALUES ('ok'); SELECT VERSION();" | grep -q "^$version"
"$moved/bin/mysqldump" -uroot -h127.0.0.1 -P53307 t >"$work/dump.sql"
grep -q "INSERT INTO \`x\` VALUES ('ok')" "$work/dump.sql"
"$moved/bin/mysqladmin" -uroot -h127.0.0.1 -P53307 shutdown
wait $pid || true
trap - EXIT
echo "MySQL $version ($arch) works after moving: initialize, start, query, dump"

name="mysql-$version-macos-$arch.tar.gz"
tar -C "$prefix" --uid 0 --gid 0 --uname root --gname wheel -czf "$out/$name" bin lib share
(cd "$out" && shasum -a 256 "$name" >"$name.sha256")
ls -l "$out"
