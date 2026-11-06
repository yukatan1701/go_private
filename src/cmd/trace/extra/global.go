// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package extra

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
)

type candInfo struct {
	valid       bool
	startCap    int
	endCap      int
	deltaAllocs int
	deltaElems  int
}

type allocInfo struct {
	numChains      int
	totalElems     int
	totalAllocs    int
	allocsPerChain int // how many allocations are done when going through one chain
	elemsPerChain  int // how many new elems are allocated when going through one chain
}

type globalChain struct {
	startCap int
	endCap   int
}

type globalFrame struct {
	hash     frameHash
	funcHash funcHash
	fileHash stringHash
	line     string
	str      string
}

type globalStack struct {
	hash    stackHash
	frames  []*globalFrame
	chains  map[globalChain]*allocInfo
	num     int
	newCaps map[int]int
}

type trimmedStack struct {
	hash        stackHash
	frames      []*globalFrame
	chains      map[globalChain]*allocInfo
	totalChains int
	num         int
	bestCand    candInfo
	stat        map[int][]globalChain
}

type globalStat struct {
	hashToString map[stringHash]string
	globalStacks map[stackHash]*globalStack
	globalFrames map[frameHash]*globalFrame
	depthToStat  []depthStat
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

type depthStat struct {
	depth  int
	stacks []profileStack
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
// Example (capacity list): 2 -> 4 -> 8.
// Total objects allocated: 2 + 4 + 8 = 14.
func (ch *chain) elemsPerChain() int {
	elems := 0
	for _, c := range ch.caps {
		elems += c
	}
	return elems
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
	// TODO: calculate better?
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

func (g *globalStat) trimStacks(depth int) (map[stackHash]*trimmedStack, error) {
	if depth < 1 {
		// TODO: return error?
		return nil, nil
	}
	trimmedStacks := make(map[stackHash]*trimmedStack)
	for _, gs := range g.globalStacks {
		var b strings.Builder
		length := min(len(gs.frames), depth)
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
	// TODO: return sorted?
	return trimmedStacks, nil
}

func (g *globalStat) findBestSolution(depth int) error {
	// STEP 1: prepare trimmed stacks
	trimmedStacks, err := g.trimStacks(depth)
	if err != nil {
		return err
	}
	if debug > 0 {
		printDebug("TRIMMED STACKS (depth=%d)\n", depth)
	}
	stat := depthStat{
		depth:  depth,
		stacks: make([]profileStack, 0, len(trimmedStacks)),
	}
	for _, s := range trimmedStacks {
		if len(s.stat) == 0 {
			continue
		}
		if debug > 0 {
			g.printTrimmedStack(s)
		}
		if err = s.findBest(); err != nil {
			return err
		}
		if !s.bestCand.valid {
			continue
		}
		ps := profileStack{
			frames:   make([]profileFrame, 0, len(s.frames)),
			startCap: s.bestCand.startCap,
			endCap:   s.bestCand.endCap,
		}
		for _, fr := range s.frames {
			ps.frames = append(ps.frames, profileFrame{
				funchash: fr.funcHash,
				line:     fr.line,
			})
		}
		stat.stacks = append(stat.stacks, ps)
	}
	g.depthToStat = append(g.depthToStat, stat)
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
		allocsPerChain := ciAllocs.allocsPerChain - bestIdx
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

func (s *trimmedStack) findBest() error {
	if len(s.chains) == 0 || len(s.stat) == 0 || len(s.frames) == 0 {
		return nil
	}
	totalChains := s.totalChains
	if debugFindBest {
		printDebug("ESTIMATION\n")
		strs := make([]string, 0, len(s.frames))
		for _, fr := range s.frames {
			strs = append(strs, fr.str)
		}
		printDebug("CHECK STACK %d (num=%d, chains=%d) [%s]\n", s.hash, s.num, totalChains, strings.Join(strs, "|"))
	}
	candidates := make([]candInfo, 0, len(s.stat))
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
		if oneAllocPerChain > 1 {
			printDebug("SKIP: has %d allocs/chain == 1\n", oneAllocPerChain)
			continue
		}

		localTotalChains := 0
		for _, ch := range chains {
			localTotalChains += s.chains[ch].numChains
		}
		geomean := float64(1)
		for _, ch := range chains {
			numchains := float64(s.chains[ch].numChains)
			geomean *= math.Pow(float64(ch.endCap), numchains/float64(localTotalChains))
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
		bestCand := candInfo{
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
	slices.SortFunc(candidates, func(x, y candInfo) int {
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

/***
Profile structure:
HEADER
hashes_num
id fname
id fname

maxDepth
stacknum_1
frame_len_i func_id line_num ... func_id line_num start end
frame_len_j
frame_len_k


stacknum_2



***/

func (g *globalStat) emitProfile(w io.Writer, maxDepth uint) error {
	const header = "EXTRA TRACE PROFILE V1\n"
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
