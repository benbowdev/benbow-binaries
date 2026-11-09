// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Andrea Casali

// Command benbow-manifest looks for new upstream versions of the components
// Benbow runs, verifies each download against the checksum its upstream
// publishes (or, where there is none, records the SHA-256 of the first
// download), and writes Benbow's signed manifest.
//
// It only adds entries: an artifact already in the manifest is never
// changed, and Benbow refuses a manifest that tries. Run in this folder:
//
//	go run . -sign      # COVO_MANIFEST_KEY: base64 ed25519 seed
//	go run . -release   # then attach it to the "manifest" release
//	                    # (GITHUB_TOKEN, GITHUB_REPOSITORY; release.go)
//	go run .            # dry run: report what would be added
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// Artifact mirrors Benbow's components.Artifact.
type Artifact struct {
	Component string   `json:"component"`
	Version   string   `json:"version"`
	Arch      string   `json:"arch"`
	For       string   `json:"for,omitempty"`
	Format    string   `json:"format,omitempty"`
	URL       string   `json:"url"`
	SHA256    string   `json:"sha256"`
	SHA512    string   `json:"sha512,omitempty"`
	MD5       string   `json:"md5,omitempty"`
	MinMacOS  string   `json:"min_macos,omitempty"`
	Strip     string   `json:"strip,omitempty"`
	Files     []string `json:"files"`
}

// Manifest mirrors Benbow's components.Manifest.
type Manifest struct {
	Serial int64 `json:"serial"`
	// Expires (RFC 3339, UTC): Benbow refuses a published manifest past this
	// time, so a stuck or hijacked server cannot keep it on an old manifest
	// unnoticed. Renewed before it gets close (see lifetime, renewBefore).
	Expires   string     `json:"expires"`
	Artifacts []Artifact `json:"artifacts"`
}

const (
	lifetime    = 14 * 24 * time.Hour
	renewBefore = 7 * 24 * time.Hour
)

// needsRenewal: no expiry yet, unreadable, or less than renewBefore left.
func needsRenewal(expires string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, expires)
	return err != nil || t.Sub(now) < renewBefore
}

func key(a Artifact) string { return a.Component + "|" + a.Version + "|" + a.Arch + "|" + a.For }

func main() {
	sign := flag.Bool("sign", false, "sign with COVO_MANIFEST_KEY and write manifest/manifest.json(.sig)")
	dir := flag.String("dir", ".", "folder with base.json and the published manifest")
	release := flag.Bool("release", false, "only attach the signed manifest to the \"manifest\" release, recording the old asset's download count")
	stats := flag.String("stats", "../stats/manifest-checks.csv", "CSV of daily update checks (with -release)")
	flag.Parse()
	defer removeTemps()
	if *release {
		// A step of its own, after -sign: it never sees the signing key, and
		// it never fails the run (the manifest on main matters more).
		publishToRelease(*dir, *stats)
		return
	}

	published := filepath.Join(*dir, "manifest.json")
	m, err := load(published)
	if os.IsNotExist(err) {
		m, err = load(filepath.Join(*dir, "base.json"))
	}
	if err != nil {
		log.Fatal(err)
	}
	have := map[string]bool{}
	for _, a := range m.Artifacts {
		have[key(a)] = true
	}

	var added []Artifact
	for _, src := range sources {
		found, err := src.check(m)
		if err != nil {
			// One upstream being down must not block the others.
			log.Printf("%s: %v", src.name, err)
			continue
		}
		for _, a := range found {
			if have[key(a)] {
				continue
			}
			have[key(a)] = true
			added = append(added, a)
			log.Printf("add %s %s %s %s", a.Component, a.Version, a.Arch, a.For)
		}
	}
	now := time.Now().UTC()
	if len(added) == 0 && fileExists(published) && !needsRenewal(m.Expires, now) {
		log.Print("nothing new")
		return
	}
	m.Artifacts = append(m.Artifacts, added...)
	m.Serial = nextSerial(m.Serial, now)
	m.Expires = now.Add(lifetime).Format(time.RFC3339)
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	body = append(body, '\n')
	if !*sign {
		log.Printf("dry run: serial %d, %d new artifacts (not written)", m.Serial, len(added))
		return
	}
	seed, err := base64.StdEncoding.DecodeString(os.Getenv("COVO_MANIFEST_KEY"))
	if err != nil || len(seed) != ed25519.SeedSize {
		removeTemps()
		log.Fatal("COVO_MANIFEST_KEY must be a base64 ed25519 seed")
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), body)
	if err := os.WriteFile(published, body, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(published+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("serial %d, %d new artifacts, expires %s\n", m.Serial, len(added), m.Expires)
}

// nextSerial is YYYYMMDDnn, always greater than prev.
func nextSerial(prev int64, now time.Time) int64 {
	day, _ := strconv.ParseInt(now.Format("20060102"), 10, 64)
	s := day * 100
	if s <= prev {
		s = prev + 1
	}
	return s
}

func load(path string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// known reports whether m already has component at version: nothing to
// download again (and entries are never changed anyway).
func known(m Manifest, component, version string) bool {
	return slices.Contains(versions(m, component), version)
}

func knownFor(m Manifest, component, version, forVersion, arch string) bool {
	return slices.ContainsFunc(m.Artifacts, func(a Artifact) bool {
		return a.Component == component && a.Version == version && a.For == forVersion && a.Arch == arch
	})
}

// versions lists the versions of component in m, for any arch.
func versions(m Manifest, component string) []string {
	var out []string
	for _, a := range m.Artifacts {
		if a.Component == component && !slices.Contains(out, a.Version) {
			out = append(out, a.Version)
		}
	}
	return out
}
