// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package extra

import (
	"bytes"
	"fmt"
	"io"
	"slices"
)

// Depth of a context-sensitive profile. Consider the following call stack:
//
// func1      // 'append' here
// func2:20
// func3:30
//
// Since a trace does not save information about inlined call sites,
// we will calculate an optimal slice capacity for each desirable call stack
// depth. For example:
//
// depth == 0: calculate a capacity for each stack having 'func1' as a top frame.
// depth == 1: calculate a capacity for each stack having 'func1|func2:30' as 2 top frames.
// depth >= 2: in a similar manner
//
// When maxStackDepth is set to N, the profile stores an information about all
// depths <= N. While generating a call to a special growslice, the compiler
// checks an inlining depth and looks for a corresponding information in a profile.
//
// For now, we set stack depth to 1 although all other values are supported.
// TODO: pass this value as argument to 'go tool trace'?
const maxStackDepth = 1

// Call stack info for each desirable depth.
type depthStat struct {
	stacks []profileStack
}

type profileFrame struct {
	funchash funcHash
	line     string
}

type profileStack struct {
	frames       []profileFrame
	startCap     int
	predictedCap int
}

// Slice profile structure:
//
// header
// func_number
// func_id_1 func_name_1
// func_id_2 func_name_2
// ...
// func_id_n func_name_n
// max_depth
// stack_num_for_depth_0
// frame_num_0 func_id_x line_x start_cap predicted_cap
// frame_num_1 ...
// ...
// stack_num_for_depth_1
// frame_num_0 func_id_x line_x func_id_y line_y start_cap predicted_cap
// frame_num_1 ...
// ...
func (g *globalStat) emitProfile(w io.Writer) error {
	const header = "EXTRA SLICE PROFILE V1\n"

	var b bytes.Buffer
	b.WriteString(header)

	if len(g.depthStats) == 0 {
		// No statistics, emit an empty profile with func_number == -1.
		fmt.Fprintln(&b, "-1")
		_, err := w.Write(b.Bytes())
		return err
	}

	allHashes := make(map[funcHash]int)
	for _, ds := range g.depthStats {
		for _, st := range ds.stacks {
			for _, fr := range st.frames {
				allHashes[fr.funchash] = -1
			}
		}
	}
	hashes := make([]funcHash, 0, len(allHashes))
	for hash := range allHashes {
		hashes = append(hashes, hash)
	}
	// Sort function hashes and assign profile indices to each.
	slices.Sort(hashes)
	for i, hash := range hashes {
		allHashes[hash] = i
	}

	funcNumber := len(hashes)
	fmt.Fprintf(&b, "%d\n", funcNumber)
	for id, hash := range hashes {
		fname := g.hashToString[stringHash(hash)]
		fmt.Fprintf(&b, "%d %s\n", id, fname)
	}
	depth := len(g.depthStats) - 1
	fmt.Fprintf(&b, "%d\n", depth)
	for _, stat := range g.depthStats {
		fmt.Fprintf(&b, "%d\n", len(stat.stacks))
		for _, st := range stat.stacks {
			stackLen := len(st.frames)
			fmt.Fprintf(&b, "%d ", stackLen)
			for _, fr := range st.frames {
				funcId := allHashes[fr.funchash]
				fmt.Fprintf(&b, "%d %s ", funcId, fr.line)
			}
			fmt.Fprintf(&b, "%d %d\n", st.startCap, st.predictedCap)
		}
	}
	_, err := w.Write(b.Bytes())
	return err
}
