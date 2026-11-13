// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package extra

import (
	"bytes"
	"testing"
)

// TestEmitProfileEmpty checks that a profile without any stacks is emitted as
// the compact func_number == -1 form, even when the depth statistics are
// dense.
func TestEmitProfileEmpty(t *testing.T) {
	for _, stats := range [][]depthStat{
		nil,
		{{}},
		{{}, {}},
	} {
		gs := &globalStat{depthStats: stats}
		var buf bytes.Buffer
		if err := gs.emitProfile(&buf); err != nil {
			t.Fatalf("emitProfile() error: %v", err)
		}
		const want = "EXTRA SLICE PROFILE V1\n-1\n"
		if got := buf.String(); got != want {
			t.Errorf("emitProfile(%v) = %q, want %q", stats, got, want)
		}
	}
}

// TestEmitProfileDenseDepths checks that every depth up to the maximum is
// emitted, including empty ones, so the number of tree sections matches the
// max_depth header.
func TestEmitProfileDenseDepths(t *testing.T) {
	gs := &globalStat{
		hashToString: map[stringHash]string{0: "pkg.doAppend"},
		depthStats: []depthStat{
			{
				stacks: []profileStack{{
					frames:       []profileFrame{{funchash: funcHash(0), line: "10"}},
					startCap:     0,
					predictedCap: 4,
				}},
			},
			{}, // no statistics at depth 1
		},
	}
	var buf bytes.Buffer
	if err := gs.emitProfile(&buf); err != nil {
		t.Fatalf("emitProfile() error: %v", err)
	}
	const want = "EXTRA SLICE PROFILE V1\n1\n0 pkg.doAppend\n1\n1\n1 0 10 0 4\n0\n"
	if got := buf.String(); got != want {
		t.Errorf("emitProfile() = %q, want %q", got, want)
	}
}
