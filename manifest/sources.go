// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Andrea Casali

package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5"  //nolint:gosec // only to compare with a published MD5
	"crypto/sha1" //nolint:gosec // only to compare with wordpress.org's SHA-1
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

type source struct {
	name  string
	check func(Manifest) ([]Artifact, error)
}

var sources = []source{
	{"caddy", checkCaddy},
	{"mailpit", checkMailpit},
	{"adminer", checkAdminer},
	{"wpcli", checkWPCLI},
	{"wordpress", checkWordPress},
	{"cabundle", checkCABundle},
	{"php", checkPHP},
	{"legacy-php", checkLegacyPHP},
	{"xdebug", checkXdebug},
}

var client = &http.Client{Timeout: 10 * time.Minute}

// temps are the downloads, removed when the program ends.
var temps []string

func removeTemps() {
	for _, p := range temps {
		_ = os.Remove(p)
	}
}

func get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

func getText(url string) (string, error) {
	resp, err := get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), err
}

func getJSON(url string, v any) error {
	resp, err := get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// sums of one download.
type sums struct{ sha256, sha512, sha1, md5, path string }

// download fetches url into a temporary file and hashes it.
func download(url string) (sums, error) {
	resp, err := get(url)
	if err != nil {
		return sums{}, err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp("", "covo-manifest-*")
	if err != nil {
		return sums{}, err
	}
	temps = append(temps, f.Name())
	defer f.Close()
	h256, h512, h1, h5 := sha256.New(), sha512.New(), sha1.New(), md5.New() //nolint:gosec
	if _, err := io.Copy(io.MultiWriter(f, h256, h512, h1, h5), resp.Body); err != nil {
		return sums{}, fmt.Errorf("download %s: %w", url, err)
	}
	hx := func(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }
	return sums{hx(h256), hx(h512), hx(h1), hx(h5), f.Name()}, nil
}

func mustMatch(what, got, want, url string) error {
	if !strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want)) {
		return fmt.Errorf("%s of %s is %s, upstream says %s", what, url, got, want)
	}
	return nil
}

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func latest(repo string) (ghRelease, error) {
	var r ghRelease
	err := getJSON("https://api.github.com/repos/"+repo+"/releases/latest", &r)
	return r, err
}

func (r ghRelease) asset(name string) (url, digest string, ok bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, strings.TrimPrefix(a.Digest, "sha256:"), true
		}
	}
	return "", "", false
}

// fromDigest downloads a GitHub release asset and checks GitHub's digest.
func fromDigest(r ghRelease, name string) (string, sums, error) {
	url, digest, ok := r.asset(name)
	if !ok {
		return "", sums{}, fmt.Errorf("%s: no asset %s", r.TagName, name)
	}
	if digest == "" {
		return "", sums{}, fmt.Errorf("%s: asset %s has no digest", r.TagName, name)
	}
	s, err := download(url)
	if err != nil {
		return "", sums{}, err
	}
	return url, s, mustMatch("sha256", s.sha256, digest, url)
}

var goArch = map[string]string{"arm64": "arm64", "amd64": "amd64"}

func checkCaddy(m Manifest) ([]Artifact, error) {
	r, err := latest("caddyserver/caddy")
	if err != nil || r.Prerelease {
		return nil, err
	}
	v := strings.TrimPrefix(r.TagName, "v")
	if known(m, "caddy", v) {
		return nil, nil
	}
	sumsURL, _, ok := r.asset("caddy_" + v + "_checksums.txt")
	if !ok {
		return nil, fmt.Errorf("caddy %s: no checksums file", v)
	}
	text, err := getText(sumsURL)
	if err != nil {
		return nil, err
	}
	var out []Artifact
	for arch := range goArch {
		name := "caddy_" + v + "_mac_" + arch + ".tar.gz"
		want := ""
		for _, line := range strings.Split(text, "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[1] == name {
				want = f[0]
			}
		}
		url, _, ok := r.asset(name)
		if !ok || want == "" {
			return nil, fmt.Errorf("caddy %s: no %s", v, name)
		}
		s, err := download(url)
		if err != nil {
			return nil, err
		}
		if err := mustMatch("sha512", s.sha512, want, url); err != nil {
			return nil, err
		}
		out = append(out, Artifact{Component: "caddy", Version: v, Arch: arch, URL: url, SHA256: s.sha256, SHA512: s.sha512, Files: []string{"caddy"}})
	}
	return out, nil
}

func checkMailpit(m Manifest) ([]Artifact, error) {
	r, err := latest("axllent/mailpit")
	if err != nil || r.Prerelease {
		return nil, err
	}
	v := strings.TrimPrefix(r.TagName, "v")
	if known(m, "mailpit", v) {
		return nil, nil
	}
	var out []Artifact
	for arch := range goArch {
		url, s, err := fromDigest(r, "mailpit-darwin-"+arch+".tar.gz")
		if err != nil {
			return nil, err
		}
		out = append(out, Artifact{Component: "mailpit", Version: v, Arch: arch, URL: url, SHA256: s.sha256, Files: []string{"mailpit"}})
	}
	return out, nil
}

func checkAdminer(m Manifest) ([]Artifact, error) {
	r, err := latest("vrana/adminer")
	if err != nil || r.Prerelease {
		return nil, err
	}
	v := strings.TrimPrefix(r.TagName, "v")
	if known(m, "adminer", v) {
		return nil, nil
	}
	url, s, err := fromDigest(r, "adminer-"+v+"-mysql.php")
	if err != nil {
		return nil, err
	}
	return []Artifact{{Component: "adminer", Version: v, Arch: "all", Format: "file", URL: url, SHA256: s.sha256, Files: []string{"adminer.php"}}}, nil
}

func checkWPCLI(m Manifest) ([]Artifact, error) {
	r, err := latest("wp-cli/wp-cli")
	if err != nil || r.Prerelease {
		return nil, err
	}
	v := strings.TrimPrefix(r.TagName, "v")
	if known(m, "wpcli", v) {
		return nil, nil
	}
	url, _, ok := r.asset("wp-cli-" + v + ".phar")
	sumURL, _, ok2 := r.asset("wp-cli-" + v + ".phar.sha512")
	if !ok || !ok2 {
		return nil, fmt.Errorf("wp-cli %s: missing phar or sha512", v)
	}
	want, err := getText(sumURL)
	if err != nil {
		return nil, err
	}
	s, err := download(url)
	if err != nil {
		return nil, err
	}
	if err := mustMatch("sha512", s.sha512, strings.Fields(want)[0], url); err != nil {
		return nil, err
	}
	return []Artifact{{Component: "wpcli", Version: v, Arch: "all", Format: "file", URL: url, SHA256: s.sha256, SHA512: s.sha512, Files: []string{"wp-cli.phar"}}}, nil
}

// checkWordPress keeps the file list of the newest WordPress entry and
// checks that the new archive has every one of those entries.
func checkWordPress(m Manifest) ([]Artifact, error) {
	var vc struct {
		Offers []struct {
			Current string `json:"current"`
		} `json:"offers"`
	}
	if err := getJSON("https://api.wordpress.org/core/version-check/1.7/", &vc); err != nil {
		return nil, err
	}
	if len(vc.Offers) == 0 {
		return nil, errors.New("no WordPress offer")
	}
	v := vc.Offers[0].Current
	if known(m, "wordpress", v) {
		return nil, nil
	}
	var tmpl *Artifact
	for i, a := range m.Artifacts {
		if a.Component == "wordpress" {
			tmpl = &m.Artifacts[i]
		}
	}
	if tmpl == nil {
		return nil, errors.New("no WordPress entry to take the file list from")
	}
	url := "https://wordpress.org/wordpress-" + v + ".tar.gz"
	want, err := getText(url + ".sha1")
	if err != nil {
		return nil, err
	}
	s, err := download(url)
	if err != nil {
		return nil, err
	}
	if err := mustMatch("sha1", s.sha1, strings.Fields(want)[0], url); err != nil {
		return nil, err
	}
	if err := hasEntries(s.path, tmpl.Strip, tmpl.Files); err != nil {
		return nil, fmt.Errorf("wordpress %s: %w", v, err)
	}
	return []Artifact{{Component: "wordpress", Version: v, Arch: "all", URL: url, SHA256: s.sha256, Strip: tmpl.Strip, Files: tmpl.Files}}, nil
}

func hasEntries(archive, strip string, files []string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(strings.TrimPrefix(h.Name, "./"), strip+"/")
		seen[name] = true
		if i := strings.Index(name, "/"); i >= 0 {
			seen[name[:i+1]] = true
		}
	}
	for _, f := range files {
		if !seen[f] {
			return fmt.Errorf("no %s in the archive", f)
		}
	}
	return nil
}

var asOf = regexp.MustCompile(`Certificate data from Mozilla as of: \w+ (\w+) +(\d+) [\d:]+ (\d{4})`)

func checkCABundle(m Manifest) ([]Artifact, error) {
	want, err := getText("https://curl.se/ca/cacert.pem.sha256")
	if err != nil {
		return nil, err
	}
	head, err := getText("https://curl.se/ca/cacert.pem")
	if err != nil {
		return nil, err
	}
	mm := asOf.FindStringSubmatch(head)
	if mm == nil {
		return nil, errors.New("cacert.pem has no date")
	}
	d, err := time.Parse("Jan 2 2006", mm[1]+" "+mm[2]+" "+mm[3])
	if err != nil {
		return nil, err
	}
	v := d.Format("2006-01-02")
	if known(m, "cabundle", v) {
		return nil, nil
	}
	url := "https://curl.se/ca/cacert-" + v + ".pem"
	s, err := download(url)
	if err != nil {
		return nil, err
	}
	if err := mustMatch("sha256", s.sha256, strings.Fields(want)[0], url); err != nil {
		return nil, err
	}
	return []Artifact{{Component: "cabundle", Version: v, Arch: "all", Format: "file", URL: url, SHA256: s.sha256, Files: []string{"cacert.pem"}}}, nil
}

var staticArch = map[string]string{"arm64": "aarch64", "amd64": "x86_64"}

// phpMinors are the PHP versions Covo offers, old ones included (the app
// warns about those without security fixes).
var phpMinors = []string{"8.0", "8.1", "8.2", "8.3", "8.4", "8.5"}

// checkPHP follows each minor version Covo offers: the newest release on
// php.net, once static-php-cli has both the FPM and CLI builds for it.
// static-php-cli publishes no checksums: the SHA-256 of the first
// download is recorded and never changes afterwards.
func checkPHP(m Manifest) ([]Artifact, error) {
	minors := map[string]bool{}
	for _, minor := range phpMinors {
		minors[minor] = true
	}
	for _, v := range versions(m, "php") {
		p := strings.SplitN(v, ".", 3)
		minors[p[0]+"."+p[1]] = true
	}
	var out []Artifact
	for minor := range minors {
		var rel struct {
			Version string `json:"version"`
		}
		if err := getJSON("https://www.php.net/releases/index.php?json&version="+minor, &rel); err != nil {
			return out, err
		}
		v := rel.Version
		if v == "" || !strings.HasPrefix(v, minor+".") || known(m, "php", v) {
			continue
		}
		var found []Artifact
		ok := true
		for arch, sarch := range staticArch {
			for _, kind := range []string{"fpm", "cli"} {
				url := "https://dl.static-php.dev/static-php-cli/bulk/php-" + v + "-" + kind + "-macos-" + sarch + ".tar.gz"
				s, err := download(url)
				if err != nil {
					ok = false // not built yet: try again tomorrow
					break
				}
				a := Artifact{Component: "php", Version: v, Arch: arch, URL: url, SHA256: s.sha256, Files: []string{"php-fpm"}}
				if kind == "cli" {
					a = Artifact{Component: "php-cli", Version: v, For: v, Arch: arch, URL: url, SHA256: s.sha256, Files: []string{"php"}}
				}
				found = append(found, a)
			}
		}
		if ok {
			out = append(out, found...)
		}
	}
	return out, nil
}

var xdebugName = regexp.MustCompile(`^xdebug-([\d.]+)-php-([\d.]+)-macos-(arm64|amd64)\.so$`)

// checkXdebug adds the xdebug.so files published by this repository's
// xdebug workflow (their SHA-256 come from the release's SHA256SUMS,
// checked against the download).
func checkXdebug(m Manifest) ([]Artifact, error) {
	var rels []ghRelease
	if err := getJSON("https://api.github.com/repos/getcovo/covo-binaries/releases?per_page=30", &rels); err != nil {
		return nil, err
	}
	var out []Artifact
	for _, r := range rels {
		if r.Draft || !strings.HasPrefix(r.TagName, "xdebug-") {
			continue
		}
		sumsURL, _, ok := r.asset("SHA256SUMS")
		if !ok {
			continue
		}
		text, err := getText(sumsURL)
		if err != nil {
			return out, err
		}
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			mm := xdebugName.FindStringSubmatch(f[1])
			url, _, ok := r.asset(f[1])
			if mm == nil || !ok || knownFor(m, "xdebug", mm[1], mm[2], mm[3]) {
				continue
			}
			s, err := download(url)
			if err != nil {
				return out, err
			}
			if err := mustMatch("sha256", s.sha256, f[0], url); err != nil {
				return out, err
			}
			out = append(out, Artifact{Component: "xdebug", Version: mm[1], For: mm[2], Arch: mm[3], Format: "file",
				URL: url, SHA256: s.sha256, Files: []string{"xdebug.so"}})
		}
	}
	return out, nil
}

var legacyName = regexp.MustCompile(`^php-((?:5|7)\.\d+\.\d+)-(fpm|cli)-macos-aarch64\.tar\.gz$`)

// checkLegacyPHP adds PHP 5.6 to 7.4 published by this repository's
// legacy-php workflow (SHA-256 from the release's SHA256SUMS, checked
// against the download). Built on macOS 15, for Apple Silicon only.
func checkLegacyPHP(m Manifest) ([]Artifact, error) {
	var rels []ghRelease
	if err := getJSON("https://api.github.com/repos/getcovo/covo-binaries/releases?per_page=30", &rels); err != nil {
		return nil, err
	}
	var out []Artifact
	for _, r := range rels {
		if r.Draft || !strings.HasPrefix(r.TagName, "legacy-php-") {
			continue
		}
		sumsURL, _, ok := r.asset("SHA256SUMS")
		if !ok {
			continue
		}
		text, err := getText(sumsURL)
		if err != nil {
			return out, err
		}
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			mm := legacyName.FindStringSubmatch(f[1])
			if mm == nil {
				continue
			}
			v, kind := mm[1], mm[2]
			component, bin := "php", "php-fpm"
			if kind == "cli" {
				component, bin = "php-cli", "php"
			}
			if known(m, component, v) || slices.ContainsFunc(out, func(a Artifact) bool { return a.Component == component && a.Version == v }) {
				continue
			}
			url, _, ok := r.asset(f[1])
			if !ok {
				continue
			}
			s, err := download(url)
			if err != nil {
				return out, err
			}
			if err := mustMatch("sha256", s.sha256, f[0], url); err != nil {
				return out, err
			}
			a := Artifact{Component: component, Version: v, Arch: "arm64", URL: url, SHA256: s.sha256,
				MinMacOS: "15.0", Files: []string{bin, "lib/", "ext/"}}
			if kind == "cli" {
				a.For = v
			}
			out = append(out, a)
		}
	}
	return out, nil
}
