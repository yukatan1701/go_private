// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pgoir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewSliceProfileTruncated checks that a profile that ends prematurely is
// rejected with an explicit error instead of being parsed as an empty line.
func TestNewSliceProfileTruncated(t *testing.T) {
	const header = "EXTRA SLICE PROFILE V1\n"

	tests := []struct {
		name    string
		profile string
		want    string
	}{
		{
			name:    "missing function table",
			profile: header + "1\n",
			want:    "unexpected end of file while reading function table",
		},
		{
			name:    "missing max depth",
			profile: header + "1\n0 pkg.f\n",
			want:    "unexpected end of file while reading max depth",
		},
		{
			name:    "missing depth header",
			profile: header + "1\n0 pkg.f\n0\n",
			want:    "unexpected end of file while reading depth header",
		},
		{
			name:    "missing stack",
			profile: header + "1\n0 pkg.f\n0\n1\n",
			want:    "unexpected end of file while reading stack",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "slice.prof")
			if err := os.WriteFile(path, []byte(tt.profile), 0644); err != nil {
				t.Fatalf("error writing profile: %v", err)
			}
			_, err := NewSliceProfile(path)
			if err == nil {
				t.Fatalf("NewSliceProfile() = nil error, want error containing %q", tt.want)
			}
			if got := err.Error(); !strings.Contains(got, tt.want) {
				t.Errorf("NewSliceProfile() error = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// TestNewSliceProfileValid checks that a complete profile still parses.
func TestNewSliceProfileValid(t *testing.T) {
	const profile = "EXTRA SLICE PROFILE V1\n" +
		"1\n" +
		"0 pkg.f\n" +
		"0\n" +
		"1\n" +
		"1 0 10 0 4\n"

	path := filepath.Join(t.TempDir(), "slice.prof")
	if err := os.WriteFile(path, []byte(profile), 0644); err != nil {
		t.Fatalf("error writing profile: %v", err)
	}
	sp, err := NewSliceProfile(path)
	if err != nil {
		t.Fatalf("NewSliceProfile() error: %v", err)
	}
	if sp == nil {
		t.Fatalf("NewSliceProfile() = nil, want a profile")
	}
	if len(sp.DepthTrees) != 1 {
		t.Fatalf("len(DepthTrees) = %d, want 1", len(sp.DepthTrees))
	}
	cs := GrowsliceCallsite{Func: "pkg.f", Line: 10}
	node, ok := sp.DepthTrees[0][cs]
	if !ok {
		t.Fatalf("DepthTrees[0] missing callsite %+v", cs)
	}
	if want := (GrowsliceStat{StartCap: 0, PredictedCap: 4}); node.Stat != want {
		t.Errorf("stat = %+v, want %+v", node.Stat, want)
	}
}
