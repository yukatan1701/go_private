// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package extra

import (
	"bufio"
	"fmt"
	"internal/trace/raw"
	"internal/trace/tracev2"
	"io"
	"os"
)

func ProcessExtraTrace(trc io.Reader, traceListFile string) error {
	tracelistf, err := os.Open(traceListFile)
	if err != nil {
		return fmt.Errorf("failed to read trace list file: %w", err)
	}
	defer tracelistf.Close()

	readers := make([]*raw.Reader, 0)
	if traceListFile != "" {
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
		if len(gen.tailPtrToChainPos) != 0 {
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
		err = gs.findBestSolution(i)
		if err != nil {
			return err
		}
	}
	err = gs.emitProfile(os.Stdout, maxStackDepth)
	return err
}
