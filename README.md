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

## Signed manifest

`manifest/` (Go, standard library only), run every day by
`.github/workflows/manifest.yml`:

1. starts from the last published `manifest/manifest.json` (the first time,
   `manifest/base.json`, a copy of the manifest compiled into Covo);
2. looks for new versions upstream and checks each download against the
   checksum its upstream publishes: Caddy (SHA-512 `checksums.txt`),
   Mailpit and Adminer (GitHub's SHA-256 digest), wp-cli (SHA-512 file),
   WordPress (wordpress.org SHA-1), Mozilla CA bundle (curl.se SHA-256),
   Xdebug (this repository's `SHA256SUMS`). PHP follows each minor version
   Covo offers, once static-php-cli has published both the FPM and CLI
   builds; static-php-cli publishes no checksums, so the SHA-256 of the
   first download is recorded and never changes;
3. only adds entries (Covo refuses a manifest that changes one), sets a
   higher `serial`, and signs the file with ed25519
   (`manifest/manifest.json.sig`). The private key is the secret
   `COVO_MANIFEST_KEY` of this repository; Covo compiles in the public key.

Covo fetches the manifest from `main` once a day, verifies the signature
and the serial, and offers the updates; nothing is installed without the
user. MySQL is not followed automatically yet (new 8.4 releases are pinned
by hand after checking Oracle's GPG signature). A new PHP version waits for
an Xdebug build when Xdebug is on: run the xdebug workflow for it.

## License

GPL-3.0-only, Copyright (C) 2026 Andrea Casali. Xdebug is distributed under
the Xdebug License, PHP under the PHP License.
