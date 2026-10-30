// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package extra

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
)

type candInfo struct {
	valid       bool
	startCap    int
	endCap      int
	deltaAllocs int
	globalWaste int
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
	hash     stackHash
	frames   []*globalFrame
	chains   map[globalChain]*allocInfo
	num      int
	bestCand candInfo
	stat     map[int][]globalChain
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

func (st *trimmedStack) sortChains() {
	stat := make(map[int][]globalChain)
	for ch := range st.chains {
		stat[ch.startCap] = append(stat[ch.startCap], ch)
	}
	for _, chains := range stat {
		slices.SortFunc(chains, func(x, y globalChain) int {
			if x.startCap != y.startCap {
				return x.startCap - y.startCap
			}
			return x.endCap - y.endCap
		})
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
		trimmedStack.sortChains()
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
	if debug {
		printDebug("TRIMMED STACKS (depth=%d)\n", depth)
	}
	stat := depthStat{
		depth:  depth,
		stacks: make([]profileStack, 0, len(trimmedStacks)),
	}
	for _, s := range trimmedStacks {
		if debug {
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

func (s *trimmedStack) findBest() error {
	if debugFindBest {
		printDebug("ESTIMATION\n")
	}
	if len(s.chains) == 0 || len(s.stat) == 0 {
		return nil
	}
	if len(s.frames) == 0 {
		panic("HERE")
	}
	if debugFindBest {
		if len(s.frames) == 1 {
			printDebug("CHECK STACK %d (num=%d) [%s]\n", s.hash, s.num, s.frames[0].str)
		} else {
			printDebug("CHECK STACK %d (num=%d) [%s|%s]\n", s.hash, s.num, s.frames[0].str, s.frames[1].str)
		}
	}
	candidates := make([]candInfo, 0, len(s.stat))
	for startCap, chains := range s.stat {
		if debugFindBest {
			printDebug("=== Start cap: %d ===\n", startCap)
		}
		hasOneAllocPerChain := false
		skip := false
		for _, ch := range chains {
			if allocs, ok := s.chains[ch]; ok && allocs.allocsPerChain == 1 {
				if hasOneAllocPerChain {
					if debugFindBest {
						printDebug("SKIP: has several allocs/chain == 1")
					}
					skip = true
					break
				} else {
					hasOneAllocPerChain = true
				}
			}
		}
		if skip {
			continue
		}
		localCandidates := make([]candInfo, 0)
		for i, ci := range chains {
			cand := candInfo{startCap: ci.startCap, endCap: ci.endCap}
			globalWaste := 0
			newTotalAllocs := 0
			oldTotalAllocs := 0
			for j := 0; j <= i; j++ {
				cj := chains[j]
				cjAllocs := s.chains[cj]
				allocsPerChain := 1
				elemsPerChain := ci.endCap
				wastePerChain := ci.endCap - cjAllocs.elemsPerChain
				totalWaste := wastePerChain * cjAllocs.numChains
				globalWaste += totalWaste

				totalAllocs := allocsPerChain * cjAllocs.numChains
				totalElems := elemsPerChain * cjAllocs.numChains
				if debugFindBest {
					if j == i {
						printDebug("--->")
					} else {
						printDebug("    ")
					}
					printDebug("End cap: %d: %d chains (allocs/chain=%d, elems/chain=%d, total_allocs=%d, total_elems=%d, waste/chain=%d, total_waste=%d)\n",
						cj.endCap, cjAllocs.numChains, allocsPerChain, elemsPerChain, totalAllocs, totalElems, wastePerChain, totalWaste)
				}
				newTotalAllocs += totalAllocs
				oldTotalAllocs += cjAllocs.totalAllocs
			}
			for j := i + 1; j < len(chains); j++ {
				cj := chains[j]
				cjAllocs := s.chains[cj]
				allocsPerChain := cjAllocs.allocsPerChain
				elemsPerChain := cjAllocs.elemsPerChain
				totalElems := cjAllocs.elemsPerChain * cjAllocs.numChains
				totalAllocs := allocsPerChain * cjAllocs.numChains
				if debugFindBest {
					printDebug("    End cap: %d: %d chains (allocs/chain=%d, elems/chain=%d, total_allocs=%d, total_elems=%d)\n",
						cj.endCap, cjAllocs.numChains, allocsPerChain, elemsPerChain, totalAllocs, totalElems)
				}
				if elemsPerChain > cjAllocs.elemsPerChain {
					return fmt.Errorf("%s: elemsPerChain > ch.allocs.elemsPerChain", parsingErrPrefix)
				}
				deltaElems := (cjAllocs.elemsPerChain - elemsPerChain) * cjAllocs.numChains
				globalWaste -= deltaElems
				newTotalAllocs += totalAllocs
				oldTotalAllocs += cjAllocs.totalAllocs
			}
			deltaAllocs := newTotalAllocs - oldTotalAllocs
			if debugFindBest {
				printDebug("Global waste: %d elements\nDelta allocs: %d\n", globalWaste, deltaAllocs)
				printDebug("-------------------------------------------\n")
			}
			const deltaAllocsThreshold = -300
			if deltaAllocs >= deltaAllocsThreshold || globalWaste > 0 {
				continue
			}
			cand.globalWaste = globalWaste
			cand.deltaAllocs = deltaAllocs
			localCandidates = append(localCandidates, cand)
		}
		if len(localCandidates) < 1 {
			if debugFindBest {
				printDebug("No local candidates.\n")
			}
			continue
		}
		bestCand := localCandidates[0]
		for i := 1; i < len(localCandidates); i++ {
			lc, bc := localCandidates[i], bestCand
			if lc.globalWaste < bc.globalWaste {
				bestCand = localCandidates[i]
			} else if lc.globalWaste == bc.globalWaste && lc.deltaAllocs < bc.deltaAllocs {
				bestCand = lc
			}
		}
		candidates = append(candidates, bestCand)
	}
	if len(candidates) == 0 {
		return nil
	}
	slices.SortFunc(candidates, func(x, y candInfo) int {
		if x.globalWaste != y.globalWaste {
			return x.globalWaste - y.globalWaste
		}
		if x.deltaAllocs != y.deltaAllocs {
			return x.deltaAllocs - y.deltaAllocs
		}
		return x.startCap - y.startCap
	})
	bestCand := candidates[0]
	bestCand.valid = true
	if debugFindBest {
		printDebug("FOUND BEST CHAIN (depth=%d): start_size=%d, end_size=%d, delta_allocs=%d, global_waste=%d\n",
			len(s.frames), bestCand.startCap, bestCand.endCap, bestCand.deltaAllocs, bestCand.globalWaste)
	}
	s.bestCand = bestCand
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
