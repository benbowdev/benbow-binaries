#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Andrea Casali
#
# Turns shivammathur/php's Homebrew build of an end-of-life PHP version
# (5.6 to 7.4, with security fixes backported by shivammathur/php-src-backports)
# into two self-contained archives for Benbow, like static-php-cli's:
#
#   php-<version>-fpm-macos-aarch64.tar.gz   php-fpm + lib/ + ext/
#   php-<version>-cli-macos-aarch64.tar.gz   php     + lib/ + ext/
#
# ext/ holds the extensions built as shared objects (intl in 7.4, opcache):
# Benbow loads every .so in it from its php.ini.
#
# Every library the binary needs from Homebrew is copied into lib/ and
# relinked to @executable_path/lib, then everything is ad-hoc signed. The
# result is checked with Homebrew moved out of the way.
#
# Runs on a GitHub Actions macOS runner only: it installs Homebrew packages.
#
# usage: build.sh <minor> <out dir>
set -euo pipefail

minor=$1
mkdir -p "$2"
out=$(cd "$2" && pwd)

brew tap shivammathur/php >/dev/null
# Versions built earlier in the same run would clash in /opt/homebrew/bin.
brew list --formula | grep -E '^php(@|$)' | xargs -n1 brew unlink >/dev/null 2>&1 || true
brew install --quiet "shivammathur/php/php@$minor"
prefix=$(brew --prefix "php@$minor")
brew_root=$(brew --prefix)
version=$("$prefix/bin/php" -n -r 'echo PHP_VERSION;')
echo "PHP $version from $prefix"

# relink <file> <lib dir> <origin dir of file>: copies every Homebrew
# dependency of file into lib dir (recursively) and points file at it.
relink() {
	local file=$1 lib=$2 origin=$3 dep base src
	while read -r dep; do
		case "$dep" in
		"$brew_root"/*) src=$dep ;;
		@rpath/* | @loader_path/*) src="$origin/${dep#*/}" ;;
		*) continue ;; # /usr/lib, /System: part of macOS
		esac
		base=$(basename "$dep")
		if [ ! -e "$lib/$base" ]; then
			src=$(realpath "$src")
			cp "$src" "$lib/$base"
			chmod u+w "$lib/$base"
			install_name_tool -id "@executable_path/lib/$base" "$lib/$base" 2>/dev/null
			relink "$lib/$base" "$lib" "$(dirname "$src")"
		fi
		install_name_tool -change "$dep" "@executable_path/lib/$base" "$file" 2>/dev/null
	done < <(otool -L "$file" | tail -n +2 | awk '{print $1}')
}

work=$(mktemp -d)
for kind in fpm cli; do
	dir="$work/$kind"
	mkdir -p "$dir/lib"
	if [ "$kind" = fpm ]; then bin="$dir/php-fpm"; cp "$prefix/sbin/php-fpm" "$bin"; else bin="$dir/php"; cp "$prefix/bin/php" "$bin"; fi
	chmod u+w "$bin"
	relink "$bin" "$dir/lib" "$prefix/bin"
	mkdir -p "$dir/ext"
	# The formula installs shared extensions under lib/php/<api> in its keg.
	while IFS= read -r so; do
		cp "$so" "$dir/ext/"
		chmod u+w "$dir/ext/$(basename "$so")"
		relink "$dir/ext/$(basename "$so")" "$dir/lib" "$(dirname "$so")"
	done < <(find "$(realpath "$prefix")/lib/php" -name '*.so' -type f)
	for f in "$bin" "$dir"/lib/* "$dir"/ext/*; do [ -e "$f" ] && codesign --force --sign - "$f" 2>/dev/null; done
	if otool -L "$bin" "$dir"/lib/* "$dir"/ext/* 2>/dev/null | grep -q "$brew_root"; then
		echo "still linked to Homebrew:"; otool -L "$bin" "$dir"/lib/* "$dir"/ext/* | grep "$brew_root"; exit 1
	fi
done

# Prove they run without Homebrew.
sudo mv "$brew_root" "$brew_root.off"
trap 'sudo mv "$brew_root.off" "$brew_root"' EXIT
"$work/cli/php" -n -v | head -1 | grep -q "^PHP $version " || { echo "php -v failed"; exit 1; }
"$work/fpm/php-fpm" -n -v | head -1 | grep -q "^PHP $version " || { echo "php-fpm -v failed"; exit 1; }
# Load the shared extensions the way Benbow's php.ini does.
load=(-n -d "extension_dir=$work/cli/ext")
for so in "$work"/cli/ext/*.so; do
	[ -e "$so" ] || continue
	n=$(basename "$so" .so)
	if [ "$n" = opcache ]; then load+=(-d "zend_extension=$so"); else load+=(-d "extension=$n.so"); fi
done
mods=$("$work/cli/php" "${load[@]}" -m)
for m in curl openssl mysqli pdo_mysql mbstring gd zip intl xml json; do
	grep -qix "$m" <<<"$mods" || { echo "PHP $version lacks $m"; exit 1; }
done
"$work/cli/php" "${load[@]}" -r 'exit(strlen(hash("sha256", "benbow")) === 64 && openssl_random_pseudo_bytes(8) !== false ? 0 : 1);'
echo "PHP $version runs without Homebrew; shared: $(ls "$work/cli/ext" | tr '\n' ' '); modules: $(echo $mods | tr '\n' ' ')"
sudo mv "$brew_root.off" "$brew_root"
trap - EXIT

for kind in fpm cli; do
	name="php-$version-$kind-macos-aarch64.tar.gz"
	# Fixed owner and dates: the archive depends only on its files.
	bin=php; [ "$kind" = fpm ] && bin=php-fpm
	tar -C "$work/$kind" --uid 0 --gid 0 --uname root --gname wheel -czf "$out/$name" "$bin" lib ext
	(cd "$out" && shasum -a 256 "$name" >"$name.sha256")
done
ls -l "$out"
