// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Andrea Casali

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub is the part of the releases API the release step uses.
type fakeGitHub struct {
	mu      sync.Mutex
	srv     *httptest.Server
	exists  bool // the "manifest" release
	assets  []ghAsset
	nextID  int64
	created int // releases created
	fail    string
	calls   []string
	now     time.Time
}

func newFakeGitHub(t *testing.T, now time.Time) *fakeGitHub {
	f := &fakeGitHub{nextID: 100, now: now}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)
	if f.fail != "" && strings.HasPrefix(call, f.fail) {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "no auth", http.StatusUnauthorized)
		return
	}
	rel := func() {
		_ = json.NewEncoder(w).Encode(ghManifestRelease{ID: 1, UploadURL: f.srv.URL + "/upload/assets{?name,label}", Assets: f.assets})
	}
	switch {
	case call == "GET /repos/o/r/releases/tags/manifest":
		if !f.exists {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		rel()
	case call == "POST /repos/o/r/releases":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["tag_name"] != "manifest" || body["make_latest"] != "false" {
			http.Error(w, "bad release", http.StatusUnprocessableEntity)
			return
		}
		f.exists = true
		f.created++
		w.WriteHeader(http.StatusCreated)
		rel()
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/o/r/releases/assets/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/o/r/releases/assets/"), 10, 64)
		for i, a := range f.assets {
			if a.ID == id {
				f.assets = append(f.assets[:i], f.assets[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.NotFound(w, r)
	case call == "POST /upload/assets":
		name := r.URL.Query().Get("name")
		for _, a := range f.assets {
			if a.Name == name {
				http.Error(w, "already_exists", http.StatusUnprocessableEntity)
				return
			}
		}
		_, _ = io.ReadAll(r.Body)
		f.nextID++
		f.assets = append(f.assets, ghAsset{ID: f.nextID, Name: name, Label: r.URL.Query().Get("label"), CreatedAt: f.now})
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) asset(name string) *ghAsset {
	for i := range f.assets {
		if f.assets[i].Name == name {
			return &f.assets[i]
		}
	}
	return nil
}

func writeSigned(t *testing.T, dir string, serial int64) {
	t.Helper()
	body, _ := json.Marshal(Manifest{Serial: serial, Expires: "2026-11-19T00:00:00Z"})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json.sig"), []byte("sig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPublishRelease(t *testing.T) {
	day := time.Date(2026, 11, 6, 5, 17, 0, 0, time.UTC)
	tests := []struct {
		name string
		// setup prepares the fake server; serial is the manifest on disk.
		setup    func(f *fakeGitHub)
		serial   int64
		at       time.Time
		wantRows []string // CSV lines after the header
		wantNote string
		wantErr  bool
		wantSame bool // the manifest.json asset was not replaced
	}{
		{
			name:     "first run: no release",
			setup:    func(f *fakeGitHub) {},
			serial:   2026110601,
			at:       day,
			wantNote: "asset uploaded",
		},
		{
			name:     "release without assets",
			setup:    func(f *fakeGitHub) { f.exists = true },
			serial:   2026110601,
			at:       day,
			wantNote: "asset uploaded",
		},
		{
			name: "daily run: count recorded, asset replaced",
			setup: func(f *fakeGitHub) {
				f.exists = true
				f.assets = []ghAsset{
					{ID: 1, Name: "manifest.json", Label: "serial 2026110501", CreatedAt: day.Add(-24 * time.Hour), DownloadCount: 37},
					{ID: 2, Name: "manifest.json.sig", Label: "serial 2026110501", CreatedAt: day.Add(-24 * time.Hour), DownloadCount: 37},
				}
			},
			serial:   2026110501,
			at:       day,
			wantRows: []string{"2026-11-05T05:17:00Z,2026-11-06T05:17:00Z,37"},
			wantNote: "asset replaced, 37 checks",
		},
		{
			name: "second run the same day: nothing touched",
			setup: func(f *fakeGitHub) {
				f.exists = true
				f.assets = []ghAsset{
					{ID: 1, Name: "manifest.json", Label: "serial 2026110601", CreatedAt: day.Add(-2 * time.Hour), DownloadCount: 5},
					{ID: 2, Name: "manifest.json.sig", Label: "serial 2026110601", CreatedAt: day.Add(-2 * time.Hour)},
				}
			},
			serial:   2026110601,
			at:       day,
			wantNote: "asset kept",
			wantSame: true,
		},
		{
			name: "manifest changed: replaced even if young",
			setup: func(f *fakeGitHub) {
				f.exists = true
				f.assets = []ghAsset{
					{ID: 1, Name: "manifest.json", Label: "serial 2026110600", CreatedAt: day.Add(-2 * time.Hour), DownloadCount: 5},
					{ID: 2, Name: "manifest.json.sig", Label: "serial 2026110600", CreatedAt: day.Add(-2 * time.Hour)},
				}
			},
			serial:   2026110601,
			at:       day,
			wantRows: []string{"2026-11-06T03:17:00Z,2026-11-06T05:17:00Z,5"},
			wantNote: "asset replaced, 5 checks",
		},
		{
			name: "signature missing: both uploaded again",
			setup: func(f *fakeGitHub) {
				f.exists = true
				f.assets = []ghAsset{{ID: 1, Name: "manifest.json", Label: "serial 2026110601", CreatedAt: day.Add(-time.Hour), DownloadCount: 2}}
			},
			serial:   2026110601,
			at:       day,
			wantRows: []string{"2026-11-06T04:17:00Z,2026-11-06T05:17:00Z,2"},
			wantNote: "asset replaced",
		},
		{
			name: "delete fails: nothing recorded",
			setup: func(f *fakeGitHub) {
				f.exists = true
				f.fail = "DELETE"
				f.assets = []ghAsset{
					{ID: 1, Name: "manifest.json", Label: "serial 2026110501", CreatedAt: day.Add(-24 * time.Hour), DownloadCount: 37},
					{ID: 2, Name: "manifest.json.sig", Label: "serial 2026110501", CreatedAt: day.Add(-24 * time.Hour)},
				}
			},
			serial:   2026110501,
			at:       day,
			wantErr:  true,
			wantSame: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			stats := filepath.Join(dir, "stats", "manifest-checks.csv")
			writeSigned(t, dir, tt.serial)
			f := newFakeGitHub(t, tt.at)
			tt.setup(f)
			before := f.asset("manifest.json")
			var beforeID int64
			if before != nil {
				beforeID = before.ID
			}
			r := releaser{api: f.srv.URL, repo: "o/r", token: "tok", client: f.srv.Client(), now: func() time.Time { return tt.at }}

			note, err := r.publishRelease(dir, stats)
			if (err != nil) != tt.wantErr {
				t.Fatalf("publishRelease = %q, %v", note, err)
			}
			if !strings.HasPrefix(note, tt.wantNote) {
				t.Errorf("note = %q, want prefix %q", note, tt.wantNote)
			}
			got := f.asset("manifest.json")
			if tt.wantSame {
				if beforeID != 0 && (got == nil || got.ID != beforeID) {
					t.Errorf("manifest.json asset replaced: %+v", got)
				}
			} else {
				if got == nil || got.ID == beforeID || got.Label != serialLabel(tt.serial) {
					t.Errorf("manifest.json asset = %+v, want a new one labelled %q", got, serialLabel(tt.serial))
				}
				if f.asset("manifest.json.sig") == nil {
					t.Error("signature asset missing")
				}
			}
			if f.created > 1 {
				t.Errorf("release created %d times", f.created)
			}

			b, err := os.ReadFile(stats)
			if len(tt.wantRows) == 0 {
				if err == nil {
					t.Errorf("stats written: %q", b)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := statsHeader + strings.Join(tt.wantRows, "\n") + "\n"
			if string(b) != want {
				t.Errorf("stats = %q, want %q", b, want)
			}
		})
	}
}

func TestAppendStatsKeepsOneHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.csv")
	t0 := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
	for i := range 3 {
		if err := appendStats(path, t0.Add(time.Duration(i)*24*time.Hour), t0.Add(time.Duration(i+1)*24*time.Hour), int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	if n := strings.Count(string(b), "period_start"); n != 1 || strings.Count(string(b), "\n") != 4 {
		t.Fatalf("stats file:\n%s", b)
	}
}
