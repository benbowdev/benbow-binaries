# covo-binaries

Binaries that [Covo](https://getcovo.dev) downloads and that have no
suitable official build, built in public by GitHub Actions from the scripts in
this repository.

Covo pins the SHA-256 of every file it uses in its embedded manifest and
refuses anything else. Release assets are never replaced: a rebuild gets a new
tag.

## Xdebug

`xdebug/build.sh`, run by `.github/workflows/xdebug.yml` on macOS arm64:

1. downloads, and checks against pinned SHA-256, the PHP source from php.net,
   the Xdebug source from xdebug.org (same hash as PECL) and the
   static-php-cli PHP-FPM build Covo runs for that PHP version;
2. builds a minimal PHP of the same version (NTS, non-debug) for `phpize` and
   the headers, then `xdebug.so` (`MACOSX_DEPLOYMENT_TARGET` 12.0, as the
   static-php-cli builds);
3. checks that every symbol `xdebug.so` needs is exported by that PHP-FPM
   binary or by the system, and that the binary loads it and reports the
   expected Xdebug version.

Each release holds `xdebug-<xdebug>-php-<php>-macos-arm64.so` files and a
`SHA256SUMS`.

## License

GPL-3.0-only, Copyright (C) 2026 Andrea Casali. Xdebug is distributed under
the Xdebug License, PHP under the PHP License.
