// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package extra

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Each generation has its own indices of stacks, frames, and strings. One
// generation is not enough to make a conclusion. To select the same things
// among all generations, we do the following steps:
//
// 1. Match each string (function name, file name) with its hash.
// 2. Represent a frame as a string of the form "func_name:line". Calculate
//    the string hash. The new hash is an identifier of the frame among all
//    generations (we call it "global frame").
// 3. Represent a stack as a concatenation of the frame strings. Calculate a hash.
//    Similarly, the hash specifies the "global stack" identifier.
//
// It is not convenient to use a call graph instead of the string representation.
// Consider the following example:
//
// A ──┐     ┌──> E
//     ├──C──┤
// B ──┘     └──> F
//
// If calls to E and F are indirect and they share a common callsite in
// function C, we cannot easily distingush execution paths `A->C->E` with
// `A->C->F` and `B->C->F` with `B->C->E` when attaching the chain information
// to edges. It is possible but requires a complicated graph structure. Linking
// stacks and frames with their hashes is clearer.
//
// After that, we merge chain statistics. Unlike a generation chain, a global
// chain aggregates common information about capacities (globalChain itself)
// and allocations (a corresponding allocInfo).
//
// Then we calculate optimal capacities for each call stack. We do this for all
// desirable depths (see a comment in profile.go). For example, when processing
// depth=0, we merge all stacks with the same top frame and form a "trimmed
// stack" of length=1. Then we find a best solution for each one. When
// processing depth=1, we merge stacks with the same top-2 frames
// and find the best solution for these trimmed stacks of length=2. And so on.
// We need this to support context-sensitivity and inlining.

type globalStack struct {
	hash    stackHash
	frames  []*globalFrame
	chains  map[globalChain]*allocInfo
	num     int
	newCaps map[int]int // How many times a newCap appeared in all chains.
}

type globalFrame struct {
	hash     frameHash
	funcHash funcHash
	fileHash stringHash
	line     string
	str      string
}

// A global chain is represented as a pair {startCap, endCap}.
//
// Although it may seem that the end capacity is enough, we need to save the
// start capacity as well. Consider the following example:
//
//	func format(cond bool, s1, s2 string) []byte {
//	    var b []byte
//	    if cond {
//	        b = append(b, s1...) // stack=1: b grows to cap=64
//	    }
//	    b = append(b, s2...) // stack=2: b grows to 4096 if !cond, 128 otherwise
//	    return b
//	}
//
// If cond is true, b grows to cap=128 in most cases taking the execution path
// `stack=1 -> stack=2`. If false, it grows to 4096 at `stack=2` skipping
// `stack=1`. It is not a problem to calculate optimal values separately
// for each root stack (128 for the stack=1 and 4096 for the stack=2) but
// we should avoid growing slice to 4096 bytes when taking a path
// `stack=1 -> stack=2`. For this reason, we should consider an old capacity
// when entering growslice.
//
// There are two cases when calling growslice at stack=2:
//  1. oldCap == 64 means that we came from stack=1, i.e. cond is true.
//     Choose 128 as newCap.
//  2. oldCap == 0 means that cond is false and stack=1 was skipped.
//     Choose 4096 as newCap.
//
// To handle such cases, we pass two extra arguments in a specialized growslice:
//
// func growsliceSpecial(oldPtr unsafe.Pointer, newLen, oldCap, num int, et *_type, startCap, predCap int)
//
// For the `append` at stack=2, the following specialized growslice will be generated:
//
// b = growsliceSpecial(oldPtr, newLen, oldCap, num, et, 0, 4096)
//
// It means that, if oldCap == 0, grow to 4096, work as usual otherwise.
// On the path `stack=1 -> stack=2`, oldCap is 64 at `stack=2`, and a usual
// growslice implementation is used.
type globalChain struct {
	startCap int
	endCap   int
}

type allocInfo struct {
	numChains      int
	elemsPerChain  int // How many elements are allocated when forming one chain
	allocsPerChain int // How many allocations are done when forming one chain
	totalElems     int // Total elements allocated
	totalAllocs    int // Total allocation number
}

type trimmedStack struct {
	hash        stackHash
	frames      []*globalFrame
	chains      map[globalChain]*allocInfo
	totalChains int
	num         int
	bestCand    candidateInfo
	stat        map[int][]globalChain
}

type candidateInfo struct {
	valid       bool
	startCap    int
	endCap      int
	deltaAllocs int
	deltaElems  int
}

// globalStat aggregates statistics about all string, stacks and frames appeared
// in all generations.
type globalStat struct {
	hashToString map[stringHash]string
	globalStacks map[stackHash]*globalStack
	globalFrames map[frameHash]*globalFrame
	depthStats   []depthStat
}

func (g *globalStat) addToGlobalFrame(gen *generation, f *frame) (*globalFrame, error) {
	funchash := gen.stringIdToHash[f.funcId]
	funcname := g.hashToString[funchash]
	framestr := funcname + ":" + f.line
	hash, err := hashString64(framestr)
	if err != nil {
		return nil, fmt.Errorf("failed to create new global frame: %v", err)
	}
	framehash := frameHash(hash)
	if fr, ok := g.globalFrames[framehash]; ok {
		return fr, nil
	}
	filehash := gen.stringIdToHash[f.fileId]
	gf := &globalFrame{
		hash:     framehash,
		funcHash: funcHash(funchash),
		fileHash: filehash,
		line:     f.line,
		str:      framestr,
	}
	g.globalFrames[framehash] = gf
	return gf, nil
}

// How many allocations were performed when going from oldCap to newCap.
func (ch *chain) allocsPerChain() int {
	return len(ch.ptrs) - 1
}

// How many objects were allocated in sum when going from oldCap to newCap.
// Example (capacity list): 0 -> 2 -> 4 -> 8.
// Total objects allocated: 2 + 4 + 8 = 14.
func (ch *chain) elemsPerChain() (elems int) {
	for _, c := range ch.caps {
		elems += c
	}
	return
}

func (g *globalStat) addToGlobalStacks(gen *generation, st *stack) (*globalStack, error) {
	if len(st.frames) == 0 {
		return nil, nil
	}
	gframes := make([]*globalFrame, 0, len(st.frames))
	var b strings.Builder
	for _, fr := range st.frames {
		gf, err := g.addToGlobalFrame(gen, &fr)
		if err != nil {
			return nil, err
		}
		gframes = append(gframes, gf)
		b.WriteString(gf.str)
	}
	hash, err := hashString64(b.String())
	if err != nil {
		return nil, fmt.Errorf("failed to add stack: %v", err)
	}
	mergeChains := func(chains []chain, gchains map[globalChain]*allocInfo) error {
		for _, ch := range chains {
			if len(ch.ptrs) < 2 {
				return fmt.Errorf("failed to merge chain: not enough pointers")
			}
			allocsPerChain := ch.allocsPerChain()
			elemsPerChain := ch.elemsPerChain()
			gch := globalChain{startCap: ch.startCap, endCap: ch.endCap}
			allocinfo, ok := gchains[gch]
			if !ok {
				allocinfo = &allocInfo{allocsPerChain: allocsPerChain, elemsPerChain: elemsPerChain}
			}
			allocinfo.allocsPerChain = max(allocinfo.allocsPerChain, allocsPerChain)
			allocinfo.elemsPerChain = max(allocinfo.elemsPerChain, elemsPerChain)
			allocinfo.totalAllocs += allocsPerChain
			allocinfo.totalElems += elemsPerChain
			allocinfo.numChains++
			gchains[gch] = allocinfo
		}
		return nil
	}
	stackHash := stackHash(hash)
	if gs, ok := g.globalStacks[stackHash]; ok {
		// We have a corresponding global stack, just update it.
		gs.num += st.num
		for ncap, num := range st.newCaps {
			gs.newCaps[ncap] += num
		}
		if err := mergeChains(st.chains, gs.chains); err != nil {
			return nil, err
		}
		return gs, nil
	}
	// Create a new globalStack.
	gchains := make(map[globalChain]*allocInfo)
	if err := mergeChains(st.chains, gchains); err != nil {
		return nil, err
	}
	gs := &globalStack{
		hash:    stackHash,
		frames:  gframes,
		num:     st.num,
		newCaps: st.newCaps,
		chains:  gchains,
	}
	g.globalStacks[stackHash] = gs
	return gs, nil
}

// Adds the generation to the globalStat and updates stack hashes of the generation.
func (g *globalStat) addGeneration(gen *generation) error {
	for _, st := range gen.stacks {
		gs, err := g.addToGlobalStacks(gen, st)
		if err != nil || gs == nil {
			return err
		}
		st.hash = gs.hash
	}
	return nil
}

func (g *globalStat) filterStacks() {
	const stackNumThreshold = 100
	for hash, stack := range g.globalStacks {
		// Exclude rare stacks and stacks without several capacities.
		if stack.num < stackNumThreshold || len(stack.newCaps) < 2 {
			delete(g.globalStacks, hash)
		}
	}
}

func (st *trimmedStack) filterAndSortChains() {
	for _, ch := range st.chains {
		st.totalChains += ch.numChains
	}
	const totalChainsThreshold = 100
	if st.totalChains < totalChainsThreshold {
		if debugFindBest {
			printDebug("Stack %d: too few chains to analyze: %d (min=%d)\n", st.hash, st.totalChains, totalChainsThreshold)
		}
		return
	}
	stat := make(map[int][]globalChain)
	for ch := range st.chains {
		stat[ch.startCap] = append(stat[ch.startCap], ch)
	}
	for start, chains := range stat {
		totalChains := 0
		for _, ch := range chains {
			totalChains += st.chains[ch].numChains
		}
		const totalSubchainsThreshold = 0.05
		ratio := float64(totalChains) / float64(st.totalChains)
		if ratio < totalSubchainsThreshold {
			if debugFindBest {
				printDebug("Stack %d (start cap=%d): too few chains to analyze (%.2f%%, min %.2f%%)\n",
					st.hash, start, ratio*100, totalSubchainsThreshold*100)
			}
			delete(stat, start)
		}
	}
	for idx, chains := range stat {
		slices.SortFunc(chains, func(x, y globalChain) int {
			if x.startCap != y.startCap {
				return x.startCap - y.startCap
			}
			return x.endCap - y.endCap
		})
		const chainWeightThreshold = 0.02
		j := 0
		for i := 0; i < len(chains); i++ {
			ch := chains[i]
			allocs := st.chains[ch]
			ratio := float64(allocs.numChains) / float64(st.totalChains)
			if ratio > chainWeightThreshold {
				chains[j] = ch
				j++
			} else {
				if debugFindBest {
					printDebug("Skip chain <start=%d, end=%d>: small weight (%.2f%%, min %.2f%%)\n",
						ch.startCap, ch.endCap, ratio*100, chainWeightThreshold*100)
				}
			}
		}
		if j == 0 {
			delete(stat, idx)
			continue
		}
		stat[idx] = chains[:j]
	}
	st.stat = stat
}

func (g *globalStat) trimStacks(depth uint) ([]*trimmedStack, error) {
	trimmedStacks := make(map[stackHash]*trimmedStack)
	for _, gs := range g.globalStacks {
		if len(gs.frames) < 1 {
			continue
		}
		var b strings.Builder
		length := min(uint(len(gs.frames)), depth+1)
		frames := gs.frames[:length]
		for _, fr := range frames {
			b.WriteString(fr.str)
		}
		hash, err := hashString64(b.String())
		if err != nil {
			return nil, fmt.Errorf("failed to add stack: %v", err)
		}
		stackHash := stackHash(hash)
		if _, ok := trimmedStacks[stackHash]; !ok {
			trimmedStacks[stackHash] = &trimmedStack{
				hash:   stackHash,
				chains: make(map[globalChain]*allocInfo),
				frames: frames,
			}
		}
		trimmedStack := trimmedStacks[stackHash]
		trimmedStack.num += gs.num
		mergedChains := trimmedStack.chains
		for gc, gAllocs := range gs.chains {
			if _, ok := mergedChains[gc]; !ok {
				mergedChains[gc] = &allocInfo{}
			}
			allocs := mergedChains[gc]
			allocs.numChains += gAllocs.numChains
			allocs.totalElems += gAllocs.totalElems
			allocs.totalAllocs += gAllocs.totalAllocs
			allocs.allocsPerChain = max(allocs.allocsPerChain, gAllocs.allocsPerChain)
			allocs.elemsPerChain = max(allocs.elemsPerChain, gAllocs.elemsPerChain)
		}
	}
	for _, ts := range trimmedStacks {
		ts.filterAndSortChains()
		if len(ts.stat) == 0 {
			delete(trimmedStacks, ts.hash)
		}
	}
	// Sort stacks by hash to make a profile deterministic.
	sorted := make([]*trimmedStack, 0, len(trimmedStacks))
	for _, st := range trimmedStacks {
		sorted = append(sorted, st)
	}
	slices.SortFunc(sorted, func(x, y *trimmedStack) int {
		if x.hash < y.hash {
			return -1
		} else if x.hash > y.hash {
			return 1
		}
		return 0
	})
	return sorted, nil
}

func (g *globalStat) findBestSolution(depth uint) error {
	trimmedStacks, err := g.trimStacks(depth)
	if err != nil {
		return err
	}
	if debug > 0 {
		printDebug("TRIMMED STACKS (depth=%d)\n", depth)
	}
	stat := depthStat{
		stacks: make([]profileStack, 0, len(trimmedStacks)),
	}
	for _, s := range trimmedStacks {
		if debug > 0 {
			g.printTrimmedStack(s)
		}
		if err = s.findBest(); err != nil {
			return err
		}
		// Could not find the best candidate for this stack.
		if !s.bestCand.valid {
			continue
		}
		ps := profileStack{
			frames:       make([]profileFrame, 0, len(s.frames)),
			startCap:     s.bestCand.startCap,
			predictedCap: s.bestCand.endCap,
		}
		for _, fr := range s.frames {
			ps.frames = append(ps.frames, profileFrame{
				funchash: fr.funcHash,
				line:     fr.line,
			})
		}
		stat.stacks = append(stat.stacks, ps)
	}
	g.depthStats = append(g.depthStats, stat)
	return nil
}

func (s *trimmedStack) calculateProfit(chains []globalChain, bestIdx int) (deltaElems, deltaAllocs int) {
	newTotalAllocs := 0
	oldTotalAllocs := 0
	bestChain := chains[bestIdx]
	bestAllocs := s.chains[bestChain]
	expectedElems := 0
	actualElems := 0
	for i := 0; i <= bestIdx; i++ {
		ci := chains[i]
		ciAllocs := s.chains[ci]
		allocsPerChain := 1
		elemsPerChain := bestChain.endCap
		wastePerChain := bestChain.endCap - ciAllocs.elemsPerChain
		totalWaste := wastePerChain * ciAllocs.numChains
		deltaElems += totalWaste

		totalAllocs := allocsPerChain * ciAllocs.numChains
		totalElems := elemsPerChain * ciAllocs.numChains
		if debugFindBest {
			if bestIdx == i {
				printDebug("--->")
			} else {
				printDebug("    ")
			}
			perc := float64(ciAllocs.numChains) / float64(s.totalChains) * 100
			printDebug("End cap: %d: %d chains [%.2f%%] (allocs/chain=%d, elems/chain=%d, total_allocs=%d, total_elems=%d, waste/chain=%d, total_waste=%d)\n",
				ci.endCap, ciAllocs.numChains, perc, allocsPerChain, elemsPerChain, totalAllocs, totalElems, wastePerChain, totalWaste)
		}
		newTotalAllocs += totalAllocs
		oldTotalAllocs += ciAllocs.totalAllocs
		expectedElems += ciAllocs.numChains * ci.endCap
		actualElems += totalElems
	}

	for i := bestIdx + 1; i < len(chains); i++ {
		ci := chains[i]
		ciAllocs := s.chains[ci]
		allocsPerChain := ciAllocs.allocsPerChain - bestAllocs.allocsPerChain + 1
		elemsPerChain := ciAllocs.elemsPerChain - bestAllocs.elemsPerChain + bestChain.endCap
		totalElems := elemsPerChain * ciAllocs.numChains
		totalAllocs := allocsPerChain * ciAllocs.numChains
		if debugFindBest {
			perc := float64(ciAllocs.numChains) / float64(s.totalChains) * 100
			printDebug("    End cap: %d: %d chains [%.2f%%] (allocs/chain=%d, elems/chain=%d, total_allocs=%d, total_elems=%d)\n",
				ci.endCap, ciAllocs.numChains, perc, allocsPerChain, elemsPerChain, totalAllocs, totalElems)
		}
		deltaElems -= (ciAllocs.elemsPerChain - elemsPerChain) * ciAllocs.numChains
		newTotalAllocs += totalAllocs
		oldTotalAllocs += ciAllocs.totalAllocs
		expectedElems += ciAllocs.numChains * ci.endCap
		actualElems += ci.endCap * ciAllocs.numChains
	}
	deltaAllocs = newTotalAllocs - oldTotalAllocs
	if debugFindBest {
		printDebug("Delta elems: %d elements\nDelta allocs: %d\n", deltaElems, deltaAllocs)
		ratio := (float64(actualElems - expectedElems)) / float64(expectedElems) * 100
		printDebug("Expected/actual elems: %d/%d (%+.2f%%)\n", expectedElems, actualElems, ratio)
	}
	return
}

// Finds the best capacity value for a trimmed stack.
//
// Consider the example:
//
//	func doAppend(a *[]int, n int) {
//	    for i := range n {
//	        *a = append(*a, i)
//	    }
//	}
//
// We get the following distribution for capacities (see the explanation on
// startCap and endCap above the globalChain type):
//
// total chain number = 1000
// startCap == 0 (70% chains):
//
//	endCap == 4: 350 chains (35%)
//	endCap == 64: 350 chains (35%)
//
// startCap == 8 (30% chains):
//
//	endCap == 256: 150 chains (15%)
//	endCap == 512: 150 chains (15%)
//
// To find the best {startCap, endCap} pair among all chains, we will process
// start capacities one by one, choose the best one for each (local bests),
// and choose the best among local bests afterwards.
//
// We do the following steps:
//  1. Select startCap == 0.
//  2. Calculate geomean for all end capacities. Geomean is 16 in our case.
//  3. Choose the closest endCap. It is 4. Save the pair {0, 4} as the best local
//     candidate. Calculate a potential profit for it (see below).
//  4. Find the best local candidates for the other endCaps (8 in our case).
//  5. Choose
//
// It takes 3 allocations to reach endCap == 4 from startCap == 0:
//
//	0 -> 1 -> 2 -> 4
//
// Total allocated elements per one chain: 1 + 2 + 4 = 7. So:
//
//	allocs/chain = 3
//	elems/chain  = 7
//
// After pre-allocating memory for capacity == 4, we get the improvement:
//
//	allocs/chain = 1
//	elems/chain  = 4
//
// For endCap == 64, we had the following values initially:
//
//	allocs/chain = 7
//	elems/chain  = 127
//
// When pre-allocating 4 elements, we get the improvement:
//
//	allocs/chain = 5
//	elems/chain  = 124
//
// Before:
//
//	allocs_4     = allocs/chain(4) * num_chains(4) = 3 * 350 = 1050
//	elems_4      = elems/chain(4) * num_chains(4) = 7 * 350 = 2450
//	allocs_64    = 7 * 350 = 2450
//	elems_64     = 127 * 350 = 44450
//	total_allocs = 1050 + 2450 = 3500
//	total_elems  = 2450 + 44450 = 46900
//
// After:
//
//	allocs_4     = 1 * 350 = 350
//	elems_4      = 4 * 350 = 1400
//	allocs_64    = 5 * 350 = 1750
//	elems_64     = 124 * 350 = 43400
//	total_allocs = 350 + 1750 = 2100
//	total_elems  = 1400 + 43400 = 44800
//
// Total:
//
//	deltaAllocs = -1400
//	deltaElems  = 43400 - 46900 = -2100
//
//	candidate_0 = {startCap: 0, endCap: 4, deltaAllocs: -1400, deltaElems: -2100}
//
// For startCap == 8, the result is the following (geomean=362):
//
//	candidate_8 = {startCap: 8, endCap: 256, deltaAllocs: -1200, deltaElems: -72000}
//
// When choosing the best candidate, we will use the following criteria to
// save more memory:
// 1. Select a candidate whose deltaElems is minimal.
// 2. If equal, choose a candidate whose deltaAllocs is minimal.
//
// In our case, candidate_8 is the best one.
func (s *trimmedStack) findBest() error {
	if len(s.chains) == 0 || len(s.stat) == 0 || len(s.frames) == 0 {
		return nil
	}
	totalChains := s.totalChains
	if debugFindBest {
		strs := make([]string, 0, len(s.frames))
		for _, fr := range s.frames {
			strs = append(strs, fr.str)
		}
		printDebug("FIND BEST: check stack %d (num=%d, chains=%d) [%s]\n", s.hash, s.num, totalChains, strings.Join(strs, "|"))
	}
	candidates := make([]candidateInfo, 0, len(s.stat))
	for startCap, chains := range s.stat {
		if debugFindBest {
			printDebug("=== Start cap: %d ===\n", startCap)
		}
		oneAllocPerChain := 0
		for _, ch := range chains {
			if allocs, ok := s.chains[ch]; ok && allocs.allocsPerChain == 1 {
				oneAllocPerChain++
			}
		}
		// For some slices, growslice is called only once, and a final capacity
		// is set during the first allocation. If there are several final
		// capacities reached by one allocation, we will just skip such
		// cases in order to not complicate the estimation.
		if oneAllocPerChain > 1 {
			if debugFindBest {
				printDebug("SKIP: has %d allocs/chain == 1\n", oneAllocPerChain)
			}
			continue
		}

		numChainsForStart := 0
		for _, ch := range chains {
			numChainsForStart += s.chains[ch].numChains
		}
		geomean := float64(1)
		for _, ch := range chains {
			numchains := float64(s.chains[ch].numChains)
			geomean *= math.Pow(float64(ch.endCap), numchains/float64(numChainsForStart))
		}
		if debugFindBest {
			printDebug("GEOMEAN=%.3f\n", geomean)
		}
		bestIdx := 0
		for i := 0; i < len(chains)-1; i++ {
			endLeft := float64(chains[i].endCap)
			endRight := float64(chains[i+1].endCap)
			if endLeft <= geomean && geomean < endRight {
				distLeft := geomean - endLeft
				distRight := endRight - geomean
				if distLeft <= distRight {
					bestIdx = i
				} else {
					bestIdx = i + 1
				}
				break
			}
		}

		deltaElems, deltaAllocs := s.calculateProfit(chains, bestIdx)
		for deltaElems >= 0 && bestIdx > 0 {
			bestIdx--
			deltaElems, deltaAllocs = s.calculateProfit(chains, bestIdx)
		}
		if deltaElems >= 0 {
			continue
		}
		bestCand := candidateInfo{
			startCap:    chains[bestIdx].startCap,
			endCap:      chains[bestIdx].endCap,
			deltaElems:  deltaElems,
			deltaAllocs: deltaAllocs,
		}
		candidates = append(candidates, bestCand)
	}
	if len(candidates) == 0 {
		return nil
	}
	slices.SortFunc(candidates, func(x, y candidateInfo) int {
		if x.deltaElems != y.deltaElems {
			return x.deltaElems - y.deltaElems
		}
		if x.deltaAllocs != y.deltaAllocs {
			return x.deltaAllocs - y.deltaAllocs
		}
		return x.startCap - y.startCap
	})
	totalBestCand := candidates[0]
	totalBestCand.valid = true
	if debugFindBest {
		printDebug("FOUND BEST CHAIN (depth=%d): start_size=%d, end_size=%d, delta_allocs=%d, delta_elems=%d\n",
			len(s.frames), totalBestCand.startCap, totalBestCand.endCap, totalBestCand.deltaAllocs, totalBestCand.deltaElems)
	}
	s.bestCand = totalBestCand
	return nil
}
