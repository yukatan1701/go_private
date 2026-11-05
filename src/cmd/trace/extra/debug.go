// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package extra

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

const (
	debugFindBest  = true
	debug          = true
	maxPrintFrames = 5
)

func printDebug(format string, values ...any) {
	fmt.Fprintf(os.Stderr, format, values...)
}

// How many chains are passed through different stacks.
func (s *stack) multistackChainNumber() (num int, maxLen int) {
	for _, ch := range s.chains {
		if len(ch.stackSeq) > 1 {
			num++
			if len(ch.stackSeq) > maxLen {
				maxLen = len(ch.stackSeq)
			}
		}
	}
	return
}

func (s *stack) printStackSequences() {
	type pair struct {
		str string
		num int
	}
	var b strings.Builder
	seqs := make(map[string]int)
	for _, ch := range s.chains {
		for _, stackId := range ch.stackSeq {
			b.WriteString(strconv.Itoa(int(stackId)))
			b.WriteByte(' ')
		}
		str := b.String()
		seqs[str]++
		b.Reset()
	}
	strs := make([]pair, 0, len(seqs))
	for str, num := range seqs {
		strs = append(strs, pair{str, num})
	}
	slices.SortFunc(strs, func(p1, p2 pair) int {
		return p2.num - p1.num
	})
	for _, p := range strs {
		printDebug("\t[%s]->%d\n", p.str, p.num)
	}
}

func (g *globalStat) printGenerationStat(gen *generation) {
	stacksByNum := make([]*stack, 0, len(gen.stacks))
	for _, st := range gen.stacks {
		stacksByNum = append(stacksByNum, st)
	}
	slices.SortFunc(stacksByNum, func(x, y *stack) int {
		if y.num == x.num {
			return int(x.id) - int(y.id)
		}
		return y.num - x.num
	})
	printDebug("GENERATION=%d\n", gen.id)
	for _, st := range stacksByNum {
		multistackChains, maxLen := st.multistackChainNumber()
		printDebug("-------------------------------\n")
		printDebug("Stack %d (num=%d, hash=%d, chain num=%d, multistack chains=%d, maxlen=%d, intermediate=%v):\n",
			st.id, st.num, st.hash, len(st.chains), multistackChains, maxLen, st.intermediate)
		if multistackChains > 0 {
			printDebug("Multistacks:\n")
			st.printStackSequences()
		}
		maxFrames := maxPrintFrames
		if len(st.frames) < maxFrames {
			maxFrames = len(st.frames)
		}
		for _, fr := range st.frames[:maxFrames] {
			filehash := gen.stringIdToHash[fr.fileId]
			funchash := gen.stringIdToHash[fr.funcId]
			printDebug("\t%s:%s    %s:%s\n", g.hashToString[funchash], fr.line, g.hashToString[filehash], fr.line)
		}
		if maxFrames < len(st.frames) {
			printDebug("\t...\n")
		}
		startCapToNum := make(map[int]int)
		for _, ch := range st.chains {
			startCapToNum[ch.startCap]++
		}
		startCaps := make([]int, 0, len(startCapToNum))
		for c := range startCapToNum {
			startCaps = append(startCaps, c)
		}
		slices.Sort(startCaps)
		printDebug("StartCap -> Number:\n")
		for _, c := range startCaps {
			printDebug("\t%d->%d\n", c, startCapToNum[c])
		}
		newCaps := make([]int, 0, len(st.newCaps))
		for nc := range st.newCaps {
			newCaps = append(newCaps, nc)
		}
		slices.Sort(newCaps)
		printDebug("NewCap -> Number:\n")
		nums := make([]int, 0, len(newCaps))
		for _, newcap := range newCaps {
			number := st.newCaps[newcap]
			nums = append(nums, number)
			printDebug("\t%d -> %d\n", newcap, number)
		}
	}
}

func (g *globalStat) printGlobalStat() {
	stacks := make([]*globalStack, 0, len(g.globalStacks))
	for _, gs := range g.globalStacks {
		stacks = append(stacks, gs)
	}
	slices.SortStableFunc(stacks, func(x, y *globalStack) int {
		return y.num - x.num
	})
	printDebug("MERGED STATISTICS\n")
	for i, gs := range stacks {
		printDebug("--------------------------------------------\n")
		printDebug("Stack %d (num=%d, hash=%d, unique chain num=%d):\n", i, gs.num, gs.hash, len(gs.chains))
		maxFrames := maxPrintFrames
		if len(gs.frames) < maxFrames {
			maxFrames = len(gs.frames)
		}
		for _, fr := range gs.frames[:maxFrames] {
			fileHash := fr.fileHash
			printDebug("\t%s    %s:%s\n", fr.str, g.hashToString[fileHash], fr.line)
		}
		if maxFrames < len(gs.frames) {
			printDebug("\t...\n")
		}
		caps := make([]int, 0, len(gs.newCaps))
		for nc := range gs.newCaps {
			caps = append(caps, nc)
		}
		slices.Sort(caps)
		nums := make([]int, 0, len(caps))
		var totalAllocated, totalAllocations int
		for _, newcap := range caps {
			number := gs.newCaps[newcap]
			nums = append(nums, number)
			printDebug("%d -> %d\n", newcap, number)
			totalAllocated += newcap * number
			totalAllocations += number
		}
	}
	printDebug("--------------------------------------------\n")
}

func (gs *globalStat) printTrimmedStack(st *trimmedStack) {
	printDebug("Stack %d (num=%d, chain num=%d):\n", st.hash, st.num, st.totalChains)
	for _, fr := range st.frames {
		fileHash := fr.fileHash
		printDebug("\t%s    %s:%s\n", fr.str, gs.hashToString[fileHash], fr.line)
	}
	if st.stat == nil {
		printDebug("WARNING: no sorted global chains in the trimmed stack.\n")
		return
	}
	sorted := make([]int, 0, len(st.stat))
	for c := range st.stat {
		sorted = append(sorted, c)
	}
	slices.Sort(sorted)
	for _, c := range sorted {
		chains := st.stat[c]
		for _, ch := range chains {
			allocs := st.chains[ch]
			printDebug("\t%d -> %d: %d chains (allocs/chain=%d, elems/chain=%d, total_allocs=%d, total_elems=%d)\n",
				ch.startCap, ch.endCap, allocs.numChains, allocs.allocsPerChain, allocs.elemsPerChain, allocs.totalAllocs, allocs.totalElems)
		}
		printDebug("===")
	}

	printDebug("--------------------------------------------\n")
}
