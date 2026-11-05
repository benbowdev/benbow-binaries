// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Andrea Casali

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The signed manifest is also attached to the release tagged "manifest".
// Covo's daily automatic check downloads it from there (at most once every
// 24 hours per Mac), so the asset's download count is roughly the number of
// Macs running Covo that day. No identifier is involved: GitHub only counts
// downloads. Every run reads the count, appends it to the stats CSV and
// replaces the asset, which starts the count again from zero.

const (
	releaseTag   = "manifest"
	assetName    = "manifest.json"
	sigAssetName = "manifest.json.sig"
	// minAssetAge: a run sooner than this after the last replacement (a
	// manual run, a second run the same day) leaves the asset and its
	// count alone, unless the manifest changed.
	minAssetAge = 20 * time.Hour
	statsHeader = "period_start,period_end,checks\n"
)

type ghAsset struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Label         string    `json:"label"`
	CreatedAt     time.Time `json:"created_at"`
	DownloadCount int64     `json:"download_count"`
}

type ghManifestRelease struct {
	ID        int64     `json:"id"`
	UploadURL string    `json:"upload_url"`
	Assets    []ghAsset `json:"assets"`
}

// releaser talks to the GitHub REST API for one repository.
type releaser struct {
	api    string // https://api.github.com
	repo   string // owner/name
	token  string
	client *http.Client
	now    func() time.Time
}

// publishToRelease runs publishRelease for the workflow: GITHUB_TOKEN and
// GITHUB_REPOSITORY from the environment, problems as warnings.
func publishToRelease(dir, statsPath string) {
	r := releaser{
		api:    "https://api.github.com",
		repo:   os.Getenv("GITHUB_REPOSITORY"),
		token:  os.Getenv("GITHUB_TOKEN"),
		client: &http.Client{Timeout: 2 * time.Minute},
		now:    time.Now,
	}
	if r.repo == "" || r.token == "" {
		fmt.Println("::warning::release step skipped: GITHUB_TOKEN and GITHUB_REPOSITORY are needed")
		return
	}
	note, err := r.publishRelease(dir, statsPath)
	if err != nil {
		fmt.Printf("::warning::manifest release: %v\n", err)
		return
	}
	fmt.Println("manifest release:", note)
}

func serialLabel(serial int64) string { return "serial " + strconv.FormatInt(serial, 10) }

// publishRelease attaches the signed manifest in dir to the "manifest"
// release, recording the old asset's download count in statsPath first.
// It returns what it did, for the log.
func (r releaser) publishRelease(dir, statsPath string) (string, error) {
	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", err
	}
	sig, err := os.ReadFile(filepath.Join(dir, "manifest.json.sig"))
	if err != nil {
		return "", err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return "", fmt.Errorf("parse manifest.json: %w", err)
	}
	label := serialLabel(m.Serial)

	rel, err := r.release()
	if err != nil {
		return "", err
	}
	var cur, curSig *ghAsset
	for i, a := range rel.Assets {
		switch a.Name {
		case assetName:
			cur = &rel.Assets[i]
		case sigAssetName:
			curSig = &rel.Assets[i]
		}
	}
	now := r.now().UTC()
	if cur != nil && curSig != nil && cur.Label == label && now.Sub(cur.CreatedAt) < minAssetAge {
		return fmt.Sprintf("asset kept (%s, %d checks so far)", label, cur.DownloadCount), nil
	}

	note := "asset uploaded"
	if cur != nil {
		// The count is final once the asset is gone: delete first, then
		// record, so a failed run never records the same period twice.
		if err := r.deleteAsset(cur.ID); err != nil {
			return "", err
		}
		if err := appendStats(statsPath, cur.CreatedAt, now, cur.DownloadCount); err != nil {
			return "", err
		}
		note = fmt.Sprintf("asset replaced, %d checks since %s", cur.DownloadCount, cur.CreatedAt.UTC().Format(time.RFC3339))
	}
	if curSig != nil {
		if err := r.deleteAsset(curSig.ID); err != nil {
			return "", err
		}
	}
	// The signature first: a Covo that finds manifest.json also finds its .sig.
	if err := r.upload(rel.UploadURL, sigAssetName, label, "text/plain", sig); err != nil {
		return "", err
	}
	if err := r.upload(rel.UploadURL, assetName, label, "application/json", body); err != nil {
		return "", err
	}
	return note, nil
}

// release returns the "manifest" release, creating it the first time.
func (r releaser) release() (ghManifestRelease, error) {
	var rel ghManifestRelease
	status, err := r.do(http.MethodGet, r.api+"/repos/"+r.repo+"/releases/tags/"+releaseTag, nil, "", &rel)
	if err == nil || status != http.StatusNotFound {
		return rel, err
	}
	create, _ := json.Marshal(map[string]any{
		"tag_name":         releaseTag,
		"target_commitish": "main",
		"name":             "Signed manifest",
		"body": "Covo's signed component manifest, replaced every day by the manifest workflow. " +
			"Covo's daily update check downloads it from here; the daily download totals are in stats/manifest-checks.csv. " +
			"Not a Covo release: see getcovo/covo.",
		"make_latest": "false",
	})
	_, err = r.do(http.MethodPost, r.api+"/repos/"+r.repo+"/releases", create, "application/json", &rel)
	return rel, err
}

func (r releaser) deleteAsset(id int64) error {
	_, err := r.do(http.MethodDelete, r.api+"/repos/"+r.repo+"/releases/assets/"+strconv.FormatInt(id, 10), nil, "", nil)
	return err
}

func (r releaser) upload(uploadURL, name, label, contentType string, body []byte) error {
	base, _, _ := strings.Cut(uploadURL, "{") // ".../assets{?name,label}"
	u := base + "?" + url.Values{"name": {name}, "label": {label}}.Encode()
	_, err := r.do(http.MethodPost, u, body, contentType, nil)
	return err
}

// do sends one API request and decodes a JSON answer into out (if any).
// It returns the HTTP status and an error for anything but 2xx.
func (r releaser) do(method, u string, body []byte, contentType string, out any) (int, error) {
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("User-Agent", "covo-manifest")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, u, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s", method, u, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %w", method, u, err)
		}
	}
	return resp.StatusCode, nil
}

// appendStats adds one period to the CSV, with a header the first time.
// Periods never overlap: each is the life of one asset.
func appendStats(path string, start, end time.Time, checks int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err == nil && st.Size() == 0 {
		_, err = f.WriteString(statsHeader)
	}
	if err == nil {
		_, err = fmt.Fprintf(f, "%s,%s,%d\n", start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), checks)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
