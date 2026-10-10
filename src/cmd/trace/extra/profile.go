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
// depth == 1: calculate a capacity for each stack having 'func1' as a top frame.
// depth == 2: calculate a capacity for each stack having 'func1|func2:30' as 2 top frames.
// depth >= 3: in a similar manner
//
// When maxStackDepth is set to N, the profile stores an information about all
// depths <= N. While generating a call to a special growslice, the compiler
// checks an inlining depth and looks for a corresponding information in a profile.
//
// For now, we set stack depth to 2 although all other values are supported.
// TODO: pass this value as argument to 'go tool trace'?
const maxStackDepth = 2

// Call stack info for each desirable depth.
type depthStat struct {
	depth  int
	stacks []profileStack
}

type profileFrame struct {
	funchash funcHash
	line     string
}

type profileStack struct {
	frames   []profileFrame
	startCap int
	endCap   int
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
// stack_num_for_depth_1
// frame_num_0 func_id_x line_x start_cap end_cap
// frame_num_1 ...
// ...
// stack_num_for_depth_2
// frame_num_0 func_id_x line_x func_id_y line_y start_cap end_cap
// frame_num_1 ...
// ...
func (g *globalStat) emitProfile(w io.Writer, maxDepth uint) error {
	const header = "EXTRA SLICE PROFILE V1\n"
	allHashes := make(map[funcHash]int)
	for _, ds := range g.depthToStat {
		for _, st := range ds.stacks {
			for _, fr := range st.frames {
				allHashes[fr.funchash] = -1
			}
		}
	}

	hashes := make([]funcHash, 0, len(g.depthToStat))
	for hash := range allHashes {
		hashes = append(hashes, hash)
	}
	slices.Sort(hashes)
	for i, hash := range hashes {
		allHashes[hash] = i
	}
	var b bytes.Buffer
	b.WriteString(header)
	funcNumber := len(hashes)
	fmt.Fprintf(&b, "%d\n", funcNumber)
	for id, hash := range hashes {
		fname := g.hashToString[stringHash(hash)]
		fmt.Fprintf(&b, "%d %s\n", id, fname)
	}

	depth := min(int(maxDepth), len(g.depthToStat))
	fmt.Fprintf(&b, "%d\n", depth)
	for i := 0; i < int(depth); i++ {
		stat := g.depthToStat[i]
		fmt.Fprintf(&b, "%d\n", len(stat.stacks))
		for _, st := range stat.stacks {
			stackLen := len(st.frames)
			fmt.Fprintf(&b, "%d ", stackLen)
			for _, fr := range st.frames {
				funcId := allHashes[fr.funchash]
				fmt.Fprintf(&b, "%d %s ", funcId, fr.line)
			}
			fmt.Fprintf(&b, "%d %d\n", st.startCap, st.endCap)
		}
	}
	_, err := w.Write(b.Bytes())
	return err
}
