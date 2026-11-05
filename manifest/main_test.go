// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Andrea Casali

package main

import (
	"testing"
	"time"
)

func TestNeedsRenewal(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		expires string
		want    bool
	}{
		{"missing", "", true},
		{"garbage", "soon", true},
		{"expired", now.Add(-time.Hour).Format(time.RFC3339), true},
		{"six days left", now.Add(6 * 24 * time.Hour).Format(time.RFC3339), true},
		{"eight days left", now.Add(8 * 24 * time.Hour).Format(time.RFC3339), false},
		{"fresh", now.Add(lifetime).Format(time.RFC3339), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsRenewal(tt.expires, now); got != tt.want {
				t.Errorf("needsRenewal(%q) = %v, want %v", tt.expires, got, tt.want)
			}
		})
	}
}

func TestNextSerial(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if got := nextSerial(2026110300, now); got != 2026110500 {
		t.Errorf("new day: got %d", got)
	}
	if got := nextSerial(2026110500, now); got != 2026110501 {
		t.Errorf("same day: got %d", got)
	}
}
