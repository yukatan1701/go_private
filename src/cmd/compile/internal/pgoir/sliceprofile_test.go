// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pgoir

import "testing"

func testTree(chain []GrowsliceCallsite, stat GrowsliceStat) map[GrowsliceCallsite]*GrowsliceNode {
	tree := make(map[GrowsliceCallsite]*GrowsliceNode)
	if len(chain) == 0 {
		return tree
	}
	root := &GrowsliceNode{
		Callsite: chain[0],
		Callers:  make(map[GrowsliceCallsite]*GrowsliceNode),
	}
	tree[chain[0]] = root
	node := root
	for _, cs := range chain[1:] {
		child := &GrowsliceNode{
			Callsite: cs,
			Callers:  make(map[GrowsliceCallsite]*GrowsliceNode),
		}
		node.Callers[cs] = child
		node = child
	}
	node.Stat = stat
	return tree
}

func TestFindAppendStat(t *testing.T) {
	root := GrowsliceCallsite{Func: "pkg.doAppend", Line: 10}
	callerA := GrowsliceCallsite{Func: "pkg.A", Line: 20}
	callerB := GrowsliceCallsite{Func: "pkg.B", Line: 30}
	other := GrowsliceCallsite{Func: "pkg.other", Line: 40}
	inlChain := []GrowsliceCallsite{root, callerA, callerB}

	tests := []struct {
		name      string
		trees     [][]GrowsliceCallsite // frames for each depth's tree
		stats     []GrowsliceStat
		inlChain  []GrowsliceCallsite
		depth     int
		wantStat  GrowsliceStat
		wantDepth int
		wantOK    bool
	}{
		{
			name:      "exact depth",
			trees:     [][]GrowsliceCallsite{{root}, {root, callerA}, {root, callerA, callerB}},
			stats:     []GrowsliceStat{{0, 1}, {0, 64}, {0, 128}},
			inlChain:  inlChain,
			depth:     2,
			wantStat:  GrowsliceStat{0, 128},
			wantDepth: 2,
			wantOK:    true,
		},
		{
			name:      "fallback when deepest depth is missing",
			trees:     [][]GrowsliceCallsite{{root}, nil},
			stats:     []GrowsliceStat{{0, 4}, {}},
			inlChain:  inlChain[:2],
			depth:     1,
			wantStat:  GrowsliceStat{0, 4},
			wantDepth: 0,
			wantOK:    true,
		},
		{
			name:      "fallback when inlining chain does not match",
			trees:     [][]GrowsliceCallsite{{root}, {root, other}},
			stats:     []GrowsliceStat{{0, 8}, {0, 64}},
			inlChain:  inlChain[:2],
			depth:     1,
			wantStat:  GrowsliceStat{0, 8},
			wantDepth: 0,
			wantOK:    true,
		},
		{
			name:     "no statistics",
			trees:    [][]GrowsliceCallsite{nil, nil},
			stats:    []GrowsliceStat{{}, {}},
			inlChain: inlChain[:2],
			depth:    1,
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prof := &SliceProfile{DepthTrees: make([]map[GrowsliceCallsite]*GrowsliceNode, len(tt.trees))}
			for i := range tt.trees {
				var stat GrowsliceStat
				if i < len(tt.stats) {
					stat = tt.stats[i]
				}
				prof.DepthTrees[i] = testTree(tt.trees[i], stat)
			}
			stat, depth, ok := findAppendStat(prof, root, tt.inlChain, tt.depth)
			if ok != tt.wantOK {
				t.Fatalf("findAppendStat() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if stat != tt.wantStat {
				t.Errorf("findAppendStat() stat = %+v, want %+v", stat, tt.wantStat)
			}
			if depth != tt.wantDepth {
				t.Errorf("findAppendStat() depth = %d, want %d", depth, tt.wantDepth)
			}
		})
	}
}
