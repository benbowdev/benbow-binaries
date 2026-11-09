# benbow-binaries

Binaries that [Benbow](https://benbow.dev) downloads and that have no
suitable official build, built in public by GitHub Actions from the scripts in
this repository.

Benbow pins the SHA-256 of every file it uses in its embedded manifest and
refuses anything else. Release assets are never replaced: a rebuild gets a new
tag.

## Xdebug

`xdebug/build.sh`, run by `.github/workflows/xdebug.yml` on macOS arm64:

1. downloads, and checks against pinned SHA-256, the PHP source from php.net,
   the Xdebug source from xdebug.org (same hash as PECL) and the
   static-php-cli PHP-FPM build Benbow runs for that PHP version;
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
   `manifest/base.json`, a copy of the manifest compiled into Benbow);
2. looks for new versions upstream and checks each download against the
   checksum its upstream publishes: Caddy (SHA-512 `checksums.txt`),
   Mailpit and Adminer (GitHub's SHA-256 digest), wp-cli (SHA-512 file),
   WordPress (wordpress.org SHA-1), Mozilla CA bundle (curl.se SHA-256),
   Xdebug (this repository's `SHA256SUMS`). PHP follows each minor version
   Benbow offers, once static-php-cli has published both the FPM and CLI
   builds; static-php-cli publishes no checksums, so the SHA-256 of the
   first download is recorded and never changes;
3. only adds entries (Benbow refuses a manifest that changes one), sets a
   higher `serial`, and signs the file with ed25519
   (`manifest/manifest.json.sig`). The private key is the secret
   `COVO_MANIFEST_KEY` of this repository (the name from before the rename to Benbow: the seed exists only there, so the secret keeps it); Benbow compiles in the public key.
   The manifest expires after 14 days and is signed again when less than 7
   are left, even with nothing new;
4. in a separate step that never sees the key, attaches the signed manifest
   to the release tagged `manifest` (see below).

Benbow's daily automatic check downloads the manifest from that release and
falls back to `main` (raw) when the release is unreachable; manual checks
use `main` directly. It verifies the signature, the expiry and the serial,
and offers the updates; nothing is installed without the user.

### Daily update checks

A Mac running Benbow checks automatically at most once every 24 hours, so
the download count of the release's `manifest.json` is about the number of
Macs that ran Benbow that day. No identifier is sent and nothing else is
collected: GitHub only counts downloads. Each run of the workflow:

- leaves the asset alone if it is less than 20 hours old and the manifest
  has not changed (manual runs, a second run the same day);
- otherwise deletes the asset, then appends its life and count to
  `stats/manifest-checks.csv` (`period_start,period_end,checks`), and
  uploads the new one, labelled with its serial. Periods never overlap, so
  nothing is counted twice; a failed run records nothing.

The first row appears the day after the release is created. Older Benbow
versions (before 0.1.0-beta.4) fetch from `main` and are not counted, nor
are Macs that turned the automatic check off. MySQL is not followed automatically yet (new 8.4 releases are pinned
by hand after checking Oracle's GPG signature). A new PHP version waits for
an Xdebug build when Xdebug is on: run the xdebug workflow for it.

## License

GPL-3.0-only, Copyright (C) 2026 Andrea Casali. Xdebug is distributed under
the Xdebug License, PHP under the PHP License.
