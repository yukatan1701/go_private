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
	hash     stackHash
	frames   []*globalFrame
	chains   map[globalChain]*allocInfo
	num      int
	newcaps  map[int]int
	bestCand candInfo
	// startCap -> sorted global chains
	stat map[int][]globalChain
}

type bestTopFrames struct {
	callerHash funcHash
	callerLine string
	calleeHash funcHash
	calleeLine string
}

type globalStat struct {
	hashToString              map[stringHash]string
	globalStacks              map[stackHash]*globalStack
	globalFrames              map[frameHash]*globalFrame
	totalBests                map[funcHash]map[string]candInfo
	totalCallerSensitiveBests map[bestTopFrames]candInfo
}

func (g *globalStat) newGlobalFrame(gen *generation, f *frame) (*globalFrame, error) {
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
		gf, err := g.newGlobalFrame(gen, &fr)
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
			gs.newcaps[ncap] += num
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
		newcaps: st.newCaps,
		chains:  gchains,
	}
	g.globalStacks[stackHash] = gs
	return gs, nil
}

// Adds the generation to the globalStat and updates stack hashes of the generation.
func (g *globalStat) addGeneration(gen *generation) error {
	for _, st := range gen.stacks {
		gs, err := g.addToGlobalStacks(gen, st)
		if err != nil {
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
		if stack.num < stackNumThreshold || len(stack.newcaps) < 2 {
			delete(g.globalStacks, hash)
		}
	}
}

func (st *globalStack) sortGlobalChains() {
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

func (gs *globalStat) sortGlobalChains() {
	for _, st := range gs.globalStacks {
		st.sortGlobalChains()
	}
}

func (g *globalStat) findBestSolutions() error {
	g.sortGlobalChains()
	var err error
	for _, s := range g.globalStacks {
		s.bestCand, err = s.findBest()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *globalStack) findBest() (candInfo, error) {
	if debugFindBest {
		printDebug("ESTIMATION\n")
	}
	if len(s.chains) == 0 || len(s.stat) == 0 {
		return candInfo{}, nil
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
					return candInfo{}, fmt.Errorf("%s: elemsPerChain > ch.allocs.elemsPerChain", parsingErrPrefix)
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
		return candInfo{}, nil
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
		printDebug("FOUND BEST CHAIN: start_size=%d, end_size=%d, delta_allocs=%d, global_waste=%d\n",
			bestCand.startCap, bestCand.endCap, bestCand.deltaAllocs, bestCand.globalWaste)
	}
	return bestCand, nil
}

type accumCandInfo struct {
	startSize int
	endSize   int
}

func (g *globalStat) bestForTopFrames() {
	// frame -> accum_cand -> []cand
	topToBests := make(map[frameHash]map[accumCandInfo]candInfo)
	for _, s := range g.globalStacks {
		if !s.bestCand.valid {
			continue
		}
		top := s.frames[0]
		if topToBests[top.hash] == nil {
			topToBests[top.hash] = make(map[accumCandInfo]candInfo)
		}
		accumCand := accumCandInfo{startSize: s.bestCand.startCap, endSize: s.bestCand.endCap}
		if cand, ok := topToBests[top.hash][accumCand]; ok {
			cand.globalWaste += s.bestCand.globalWaste
			cand.deltaAllocs += s.bestCand.deltaAllocs
			topToBests[top.hash][accumCand] = cand
		} else {
			topToBests[top.hash][accumCand] = s.bestCand
		}
	}
	// func -> lines -> candidates
	totalBest := make(map[funcHash]map[string]candInfo)
	for framehash, cands := range topToBests {
		fr := g.globalFrames[framehash]
		best := candInfo{}
		for _, cand := range cands {
			if !best.valid {
				best = cand
				continue
			}
			if cand.startCap != best.startCap && debug {
				printDebug("FRAME '%s': expected start_size=%d, found=%d\n", fr.str, best.startCap, cand.startCap)
			}
			if cand.deltaAllocs < best.deltaAllocs {
				best = cand
			}
		}
		if (best.startCap == 0 && best.endCap == 1) || (best.startCap == 1 && best.endCap == 2) {
			continue
		}
		if debug {
			printDebug("FRAME '%s': overall best candidate: start_size=%d, end_size=%d (delta_allocs=%d, global_waste=%d)\n",
				fr.str, best.startCap, best.endCap, best.deltaAllocs, best.globalWaste)
		}
		if _, ok := totalBest[fr.funcHash]; !ok {
			totalBest[fr.funcHash] = make(map[string]candInfo)
		}
		totalBest[fr.funcHash][fr.line] = best
	}
	g.totalBests = totalBest
}

func (g *globalStat) bestForTopFramesWithCaller() {
	type topFramesPair struct {
		callee frameHash
		caller frameHash
	}
	// frame_pair -> accum_cand -> []cand
	topToBests := make(map[topFramesPair]map[accumCandInfo]candInfo)
	for _, s := range g.globalStacks {
		if !s.bestCand.valid {
			continue
		}
		top := topFramesPair{callee: s.frames[0].hash, caller: s.frames[1].hash}
		if topToBests[top] == nil {
			topToBests[top] = make(map[accumCandInfo]candInfo)
		}
		accumCand := accumCandInfo{startSize: s.bestCand.startCap, endSize: s.bestCand.endCap}
		if cand, ok := topToBests[top][accumCand]; ok {
			cand.globalWaste += s.bestCand.globalWaste
			cand.deltaAllocs += s.bestCand.deltaAllocs
			topToBests[top][accumCand] = cand
		} else {
			topToBests[top][accumCand] = s.bestCand
		}
	}
	// func -> lines -> candidates
	totalBest := make(map[bestTopFrames]candInfo)
	for frPair, cands := range topToBests {
		caller := g.globalFrames[frPair.caller]
		callee := g.globalFrames[frPair.callee]
		best := candInfo{}
		for _, cand := range cands {
			if !best.valid {
				best = cand
				continue
			}
			if cand.startCap != best.startCap && debug {
				printDebug("FRAME PAIR '<%s|%s>': expected start_size=%d, found=%d\n", caller.str, callee.str, best.startCap, cand.startCap)
			}
			if cand.deltaAllocs < best.deltaAllocs {
				best = cand
			}
		}
		if (best.startCap == 0 && best.endCap == 1) || (best.startCap == 1 && best.endCap == 2) {
			continue
		}
		if debug {
			printDebug("FRAME PAIR '<%s|%s>': overall best candidate: start_size=%d, end_size=%d (delta_allocs=%d, global_waste=%d)\n",
				caller.str, callee.str, best.startCap, best.endCap, best.deltaAllocs, best.globalWaste)
		}
		btf := bestTopFrames{callerHash: caller.funcHash, callerLine: caller.line, calleeHash: callee.funcHash, calleeLine: callee.line}
		totalBest[btf] = best
	}
	g.totalCallerSensitiveBests = totalBest
}

func (g *globalStat) emitProfile(w io.Writer, callerSensitive bool) error {
	const header = "EXTRA TRACE PROFILE V1\n"
	allHashes := make(map[funcHash]struct{})
	hashes := make([]funcHash, 0, len(g.totalBests))
	for btf := range g.totalCallerSensitiveBests {
		if _, ok := allHashes[btf.callerHash]; !ok {
			allHashes[btf.callerHash] = struct{}{}
			hashes = append(hashes, btf.callerHash)
		}
		if _, ok := allHashes[btf.calleeHash]; !ok {
			allHashes[btf.calleeHash] = struct{}{}
			hashes = append(hashes, btf.calleeHash)
		}
	}
	slices.Sort(hashes)
	var b bytes.Buffer
	number := fmt.Sprintf("%d\n", len(hashes))
	b.WriteString(header)
	b.WriteString(number)
	if callerSensitive {
		b.WriteString("1\n")
	} else {
		b.WriteString("0\n")
	}
	for id, hash := range hashes {
		fname := g.hashToString[stringHash(hash)]
		s := fmt.Sprintf("%d %s\n", id, fname)
		b.WriteString(s)
	}
	hashToId := make(map[funcHash]int)
	lineNum := 0
	for _, hash := range hashes {
		lineNum += len(g.totalBests[hash])
	}
	b.WriteString(fmt.Sprintf("%d\n", lineNum))
	lines := make([]string, 0, len(hashes))
	for id, hash := range hashes {
		hashToId[hash] = id
		for line, cand := range g.totalBests[hash] {
			s := fmt.Sprintf("%d %s %d %d\n", id, line, cand.startCap, cand.endCap)
			lines = append(lines, s)
		}
	}
	slices.Sort(lines)
	for _, l := range lines {
		b.WriteString(l)
	}
	if callerSensitive {
		lineNum = len(g.totalCallerSensitiveBests)
		b.WriteString(fmt.Sprintf("%d\n", lineNum))
		lines := make([]string, 0, len(g.totalCallerSensitiveBests))
		for btf, cand := range g.totalCallerSensitiveBests {
			// caller_id line callee_id line start_size end_size
			callerId, ok := hashToId[btf.callerHash]
			if !ok {
				return fmt.Errorf("failed to emit profile: no callerId in hashToId")
			}
			calleeId, ok := hashToId[btf.calleeHash]
			if !ok {
				return fmt.Errorf("failed to emit profile: no calleeId in hashToId")
			}
			s := fmt.Sprintf("%d %s %d %s %d %d\n", callerId, btf.callerLine, calleeId, btf.calleeLine, cand.startCap, cand.endCap)
			lines = append(lines, s)
		}
		slices.Sort(lines)
		for _, l := range lines {
			b.WriteString(l)
		}
	}
	_, err := w.Write(b.Bytes())
	return err
}
