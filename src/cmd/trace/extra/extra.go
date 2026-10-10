// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package extra

// A trace may contain extra events related to some experimental features like
// collecting statistics of calls to runtime.growslice. Currently only this
// feature is implemented although a support of map growing can be added in
// the future.
//
// Consider the following example:
//
// func doAppend(a []int, n int) []int {
//     for i := range n {
//         a = append(a, i) // stack=1
//     }
//     return a
// }
//
// We don't know possible values of n in compile time but can observe them
// during a trace collection. An event sequence looks like the following:
//
// Growslice dt=dt_1 oldPtr=ptr_1 newPtr=ptr_2 oldCap=4 newCap=8 stack=1
// Growslice dt=dt_2 oldPtr=ptr_2 newPtr=ptr_3 oldCap=8 newCap=16 stack=1
// ...
// Growslice dt=dt_k oldPtr=ptr_m newPtr=ptr_n oldCap=256 newCap=512 stack=1
//
// The first call to runtime.growslice occured in a call site related to stack
// with id=1. A sequence of further events helps to conclude that the maximum
// capacity of this particular slice reaches 512 elements. We can distingush
// different slices by selecting pointer chains like this:
//
// [ptr_1, ptr_2, ..., ptr_m, ptr_n]
//
// Each pointer chain has a corresponding capacity sequence:
//
// [4, 8, 16, ..., 256, 512]
//
// After extracing all chains from the trace, we can calculate an optimal slice
// capacity to increase the slice in advance. In our example, we have the
// following capacity distribution for the stack=1:
//
// 80% slices: max capacity = 32
// 20% slices: max capacity = 512
//
// Then we calculate a geomean and check which capacity is the nearest.
// The best choice is capacity=32.
//
// This was a common case. When we have a context, an estimation accuracy
// may be increased. If doAppend is inlined, we can divide chains according to
// the specific call site:
//
// if cond {
//     doAppend(a, 32)    // optimal capacity = 32 in this call site
// } else {
//     doAppend(a, 512)   // optimal capacity = 512
// }
//
// After statistics is calculated, we generate a special profile to stdout:
// go tool trace -profextra=slice ./trace.out > slice.prof
//
// It can be passed to the compiler as follows:
// go build -gcflags=all='-sliceprofile=slice.prof'
//
// The compiler substitutes a special version of growslice at the corresponding
// callsite to expand an allocation size in advance.
//
// Since we rely on the pointer chains, not on the call site only, sequential
// appends can be considered as well:
//
// b = append(b, str1...) // a special growslice is generated at the first
//                        // callsite of the chain ("root stack")
// b = append(b, str2...)
// b = append(b, str3...)

import (
	"bufio"
	"fmt"
	"internal/trace/raw"
	"internal/trace/tracev2"
	"io"
	"os"
)

func ProcessExtraTrace(trc io.Reader, traceListFile string) error {
	readers := make([]*raw.Reader, 0)
	if traceListFile != "" {
		tracelistf, err := os.Open(traceListFile)
		if err != nil {
			return fmt.Errorf("failed to read trace list file: %w", err)
		}
		defer tracelistf.Close()
		scanner := bufio.NewScanner(tracelistf)
		for scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			fname := scanner.Text()
			tracef, err := os.Open(fname)
			if err != nil {
				return fmt.Errorf("failed to read trace file: %w", err)
			}
			defer tracef.Close()
			rr, err := raw.NewReader(tracef)
			if err != nil {
				return err
			}
			readers = append(readers, rr)
		}
	} else {
		rr, err := raw.NewReader(trc)
		if err != nil {
			return err
		}
		readers = append(readers, rr)
	}

	gs := globalStat{
		hashToString: make(map[stringHash]string),
		globalStacks: make(map[stackHash]*globalStack),
		globalFrames: make(map[frameHash]*globalFrame),
	}
	for _, rr := range readers {
		gen := newGeneration(0)
		for {
			ev, err := rr.ReadEvent()
			if err == io.EOF {
				break
			} else if err != nil {
				return err
			}
			switch ev.Ev {
			case tracev2.EvEndOfGeneration:
				if err := gs.addGeneration(gen); err != nil {
					return err
				}
				if debug > 0 {
					gs.printGenerationStat(gen)
				}
				gen = newGeneration(gen.id + 1)
			case tracev2.EvGrowslice:
				if err := gen.readGrowslice(&ev); err != nil {
					return err
				}
			case tracev2.EvStack:
				if err := gen.readStack(&ev); err != nil {
					return err
				}
			case tracev2.EvString:
				if err := gen.readString(&ev, &gs); err != nil {
					return err
				}
			}
		}
		if len(gen.lastPtrToChain) != 0 {
			if err := gs.addGeneration(gen); err != nil {
				return err
			}
			if debug > 0 {
				gs.printGenerationStat(gen)
			}
		}
	}
	gs.filterStacks()
	if debug > 0 {
		gs.printGlobalStat()
	}
	for i := 1; i <= maxStackDepth; i++ {
		err := gs.findBestSolution(i)
		if err != nil {
			return err
		}
	}
	err := gs.emitProfile(os.Stdout, maxStackDepth)
	return err
}
