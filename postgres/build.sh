#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Andrea Casali
#
# Builds a self-contained, relocatable PostgreSQL for Covo from the official
# source tarball (its published SHA-256 is checked first): server, psql,
# pg_dump/pg_restore and a few contrib extensions. Only macOS's own
# libraries are linked (no ICU, no OpenSSL: Covo's PostgreSQL listens on
# 127.0.0.1 only); libpq is relinked to @executable_path. The result is
# smoke-tested with initdb, pg_ctl and psql.
#
# usage: build.sh <version> <sha256> <out dir>
set -euo pipefail

version=$1 sha=$2
mkdir -p "$3"
out=$(cd "$3" && pwd)
arch=$(uname -m) # arm64 or x86_64
work=$(mktemp -d)
cd "$work"

curl -fsSL -o src.tar.bz2 "https://ftp.postgresql.org/pub/source/v$version/postgresql-$version.tar.bz2"
echo "$sha  src.tar.bz2" | shasum -a 256 -c -
tar xjf src.tar.bz2
cd "postgresql-$version"

export MACOSX_DEPLOYMENT_TARGET=14.0
prefix="$work/pg"
./configure --prefix="$prefix" --without-icu --without-openssl --with-libedit-preferred \
	--with-uuid=e2fs --without-llvm --without-gssapi --without-ldap --without-pam --without-perl --without-python --without-tcl \
	>/dev/null
make -j"$(sysctl -n hw.ncpu)" >/dev/null
make install >/dev/null
for ext in citext hstore pg_trgm unaccent fuzzystrmatch btree_gin btree_gist uuid-ossp; do
	make -C "contrib/$ext" install >/dev/null
done

# Relocatable: binaries find libpq next to them; Postgres finds share/ and
# lib/postgresql relative to its own location already.
cd "$prefix"
rm -rf include lib/pkgconfig lib/*.a
# Plain files only (Covo's installer refuses unlisted symbolic links):
# libpq.dylib -> libpq.5.dylib -> libpq.5.18.dylib become copies.
find bin lib share -type l | while read -r link; do
	target=$(realpath "$link")
	rm "$link"
	cp "$target" "$link"
done
for f in bin/* lib/*.dylib lib/postgresql/*.dylib; do
	[ -f "$f" ] && [ ! -L "$f" ] || continue
	chmod u+w "$f"
	otool -L "$f" | tail -n +2 | awk '{print $1}' | while read -r dep; do
		case "$dep" in "$prefix"/lib/*)
			install_name_tool -change "$dep" "@executable_path/../lib/$(basename "$dep")" "$f" ;;
		esac
	done
	case "$f" in lib/*.dylib) install_name_tool -id "@rpath/$(basename "$f")" "$f" ;; esac
	codesign --force --sign - "$f" 2>/dev/null
done
if otool -L bin/* lib/*.dylib lib/postgresql/*.dylib | grep -E "^\s+/" | grep -vE "^\s+/(usr/lib|System)/"; then
	echo "linked outside macOS"; exit 1
fi
if [ -n "$(find . -type l)" ]; then echo "symbolic links left"; exit 1; fi

# Smoke test from another place, as Covo will run it.
moved="$work/moved/postgres"
mkdir -p "$(dirname "$moved")"
cp -R "$prefix" "$moved"
data="$work/data"
"$moved/bin/initdb" -D "$data" -U postgres --auth=trust -E UTF8 --locale=C >/dev/null
"$moved/bin/pg_ctl" -D "$data" -o "-p 55432 -k $work -c listen_addresses=127.0.0.1" -l "$work/log" -w start >/dev/null
trap '"$moved/bin/pg_ctl" -D "$data" -m fast stop >/dev/null 2>&1 || true' EXIT
"$moved/bin/psql" -h 127.0.0.1 -p 55432 -U postgres -Atc "create extension citext; create extension pg_trgm; create extension \"uuid-ossp\"; select version();" | tail -1 | grep -q "PostgreSQL $version"
"$moved/bin/pg_dump" -h 127.0.0.1 -p 55432 -U postgres postgres | grep -q "PostgreSQL database dump"
echo "PostgreSQL $version ($arch) works after moving: initdb, pg_ctl, psql, pg_dump, extensions"

name="postgresql-$version-macos-$arch.tar.gz"
tar -C "$prefix" --uid 0 --gid 0 --uname root --gname wheel -czf "$out/$name" bin lib share
(cd "$out" && shasum -a 256 "$name" >"$name.sha256")
ls -l "$out"
