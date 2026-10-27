package extra

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
)

type candInfo struct {
	valid       bool
	startSize   int
	endSize     int
	deltaAllocs int
	globalWaste int
}

type allocInfo struct {
	allocsPerChain int // how many allocations are done when going through one chain
	elemsPerChain  int // how many new elems are allocated when going through one chain
	totalElems     int
	totalAllocs    int
}

type chainInfo struct {
	startSize int
	endSize   int
	numchain  int
	allocs    allocInfo
}

type chainsStat struct {
	startSize int
	chains    []chainInfo
}

type globalChain struct {
	startSize      int
	endSize        int
	elemsPerChain  int
	allocsPerChain int
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
	chains   []globalChain
	num      int
	newcaps  map[int]int
	bestCand candInfo
	stat     []chainsStat
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
	totalCalleeSensitiveBests map[bestTopFrames]candInfo
}

var debugFindBest bool = false
var debug bool = false

func (g *globalStat) newGlobalFrame(gen *generation, f *frame) (*globalFrame, error) {
	funchash := gen.stringIdToHash[f.funcId]
	funcname := g.hashToString[funchash]
	framestr := funcname + ":" + f.line
	hash, err := hashString64(framestr)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", errPrefix, err)
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

func (ch *chain) allocsPerChain() int {
	elems := 0
	for _, c := range ch.caps {
		elems += c
	}
	return elems
}

func (ch *chain) elemsPerChain() int {
	elems := 0
	for _, c := range ch.caps {
		elems += c
	}
	return elems
}

func (g *globalStat) addToGlobalStack(gen *generation, st *stack) (*globalStack, error) {
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
		return nil, fmt.Errorf("%s: %v", errPrefix, err)
	}
	stackHash := stackHash(hash)
	if gs, ok := g.globalStacks[stackHash]; ok {
		gs.num += st.num
		for ncap, num := range st.newcaps {
			gs.newcaps[ncap] += num
		}
		gchains := &gs.chains
		for _, ch := range st.chains {
			*gchains = append(*gchains, globalChain{
				allocsPerChain: ch.allocsPerChain(),
				elemsPerChain:  ch.elemsPerChain(),
				startSize:      ch.startSize,
				endSize:        ch.endSize,
			})
		}
		return gs, nil
	}
	gchains := make([]globalChain, 0, len(st.chains))
	for _, ch := range st.chains {
		if len(ch.ptrs) < 2 {
			return nil, fmt.Errorf("%s: not enough pointers", errPrefix)
		}
		// TODO: why not just copy chain?
		gchains = append(gchains, globalChain{
			allocsPerChain: ch.allocsPerChain(),
			elemsPerChain:  ch.elemsPerChain(),
			startSize:      ch.startSize,
			endSize:        ch.endSize,
		})
	}
	gs := &globalStack{
		hash:    stackHash,
		frames:  gframes,
		num:     st.num,
		newcaps: st.newcaps,
		chains:  gchains,
	}
	g.globalStacks[stackHash] = gs
	return gs, nil
}

func (g *globalStat) addGeneration(gen *generation) {
	for _, st := range gen.stacks {
		g.addToGlobalStack(gen, st)
	}
}

func (g *globalStat) filterStacks() {
	const stackNumThreshold = 100
	for hash, stack := range g.globalStacks {
		if stack.num < stackNumThreshold || len(stack.newcaps) < 2 {
			delete(g.globalStacks, hash)
		}
	}
}

func (gs *globalStack) calculateChainsStat() {
	type chainEnd struct {
		numchain       int
		allocsPerChain int
		elemsPerChain  int
	}
	chains := gs.chains
	// start size -> [end size -> number]
	sizeStat := make(map[int]map[int]*chainEnd, 0)
	for _, ch := range chains {
		start := ch.startSize
		if _, ok := sizeStat[start]; !ok {
			sizeStat[start] = make(map[int]*chainEnd)
		}
		if _, ok := sizeStat[start][ch.endSize]; !ok {
			sizeStat[start][ch.endSize] = &chainEnd{}
		}
		sizeStat[start][ch.endSize].numchain++
		sizeStat[start][ch.endSize].allocsPerChain = ch.allocsPerChain
		sizeStat[start][ch.endSize].elemsPerChain = ch.elemsPerChain
	}
	startSizes := make([]chainsStat, 0, len(sizeStat))
	for size, _ := range sizeStat {
		startSizes = append(startSizes, chainsStat{startSize: size})
	}
	slices.SortStableFunc(startSizes, func(x, y chainsStat) int {
		return x.startSize - y.startSize
	})
	for i, st := range startSizes {
		start := st.startSize
		infos := make([]chainInfo, 0, len(sizeStat[start]))
		for endSize, end := range sizeStat[start] {
			numchain := end.numchain
			allocsPerChain := end.allocsPerChain
			allocs := allocInfo{
				allocsPerChain: allocsPerChain,
				elemsPerChain:  end.elemsPerChain,
				totalAllocs:    allocsPerChain * numchain,
			}
			infos = append(infos, chainInfo{
				startSize: start,
				endSize:   endSize,
				numchain:  numchain,
				allocs:    allocs,
			})
		}
		slices.SortFunc(infos, func(x, y chainInfo) int {
			return x.endSize - y.endSize
		})
		startSizes[i].chains = infos
	}
	for _, st := range startSizes {
		for i := range st.chains {
			info := &st.chains[i]
			info.allocs.totalElems = info.allocs.elemsPerChain * info.numchain
		}
	}
	gs.stat = startSizes
}

func printDebug(format string, values ...any) {
	if debug {
		fmt.Fprintf(os.Stderr, format, values...)
	}
}

func printChainsStat(stat []chainsStat) {
	for _, st := range stat {
		start := st.startSize
		printDebug("Start size %d:\n", start)
		for _, info := range st.chains {
			allocs := info.allocs
			printDebug("\tEnd size: %d: %d chains (allocs/chain=%d, elems/chain=%d, total_allocs=%d, total_elems=%d)\n",
				info.endSize, info.numchain, allocs.allocsPerChain, allocs.elemsPerChain, allocs.totalAllocs, allocs.totalElems)
		}
	}
}

const maxPrintFrames = 5

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
		printDebug("--------------------------------------------")
		printDebug("Stack %d (num=%d, hash=%d, chain num=%d):\n", i, gs.num, gs.hash, len(gs.chains))
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
		caps := make([]int, 0, len(gs.newcaps))
		for nc := range gs.newcaps {
			caps = append(caps, nc)
		}
		slices.Sort(caps)
		nums := make([]int, 0, len(caps))
		var totalAllocated, totalAllocations int
		for _, newcap := range caps {
			number := gs.newcaps[newcap]
			nums = append(nums, number)
			printDebug("%d -> %d\n", newcap, number)
			totalAllocated += newcap * number
			totalAllocations += number
		}
		printDebug("Allocated: %d elems, allocations: %d\n", totalAllocated, totalAllocations)
		printChainsStat(gs.stat)
		best_cand := gs.bestCand
		if best_cand.valid {
			printDebug("BEST CHAIN: start_size=%d, end_size=%d, delta_allocs=%d, global_waste=%d\n",
				best_cand.startSize, best_cand.endSize, best_cand.deltaAllocs, best_cand.globalWaste)
		}
	}
}

func (g *globalStat) findBestSolutions() error {
	var err error
	for _, s := range g.globalStacks {
		s.calculateChainsStat()
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
	stat := s.stat
	candidates := make([]candInfo, 0, len(stat))
	for _, st := range stat {
		// find top capacity among chain with start size == st.startSize
		if debugFindBest {
			if len(s.frames) == 1 {
				printDebug("CHECK STACK %d [%s]", s.hash, s.frames[0].str)
			} else {
				printDebug("CHECK STACK %d [%s|%s]", s.hash, s.frames[0].str, s.frames[1].str)
			}
		}
		localCandidates := make([]candInfo, 0, len(st.chains))
		if debugFindBest {
			printDebug("Start size: %d\n", st.startSize)
		}
		if len(st.chains) < 1 {
			if debugFindBest {
				printDebug("SKIP: too few chains")
			}
			continue
		}
		hasOneAllocPerChain := false
		skip := false
		for _, ch := range st.chains {
			if ch.allocs.allocsPerChain == 1 {
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
		for ci, ch := range st.chains {
			if debugFindBest {
				printDebug("Select candidate with end size %d:\n", ch.endSize)
			}
			cand := candInfo{startSize: ch.startSize, endSize: ch.endSize}
			newcap := ch.endSize
			globalWaste := 0
			newTotalAllocs := 0
			oldTotalAllocs := 0
			for j := 0; j <= ci; j++ {
				cj := st.chains[j]
				allocsPerChain := 1
				elemsPerChain := newcap
				wastePerChain := newcap - cj.allocs.elemsPerChain
				totalWaste := wastePerChain * cj.numchain
				globalWaste += totalWaste

				totalAllocs := ch.numchain
				totalElems := elemsPerChain * cj.numchain
				if debugFindBest {
					printDebug("\tEnd size: %d: %d chains (allocs/chain=%d, elems/chain=%d, total_allocs=%d, total_elems=%d, waste/chain=%d, total_waste=%d)\n",
						cj.endSize, cj.numchain, allocsPerChain, elemsPerChain, totalAllocs, totalElems, wastePerChain, totalWaste)
				}
				newTotalAllocs += totalAllocs
				oldTotalAllocs += cj.allocs.totalAllocs
			}
			for j := ci + 1; j < len(st.chains); j++ {
				cj := st.chains[j]
				allocsPerChain := cj.allocs.allocsPerChain
				elemsPerChain := cj.allocs.elemsPerChain
				totalElems := cj.allocs.elemsPerChain * cj.numchain
				totalAllocs := allocsPerChain * cj.numchain
				if debugFindBest {
					printDebug("\tEnd size: %d: %d chains (allocs/chain=%d, elems/chain=%d, total_allocs=%d, total_elems=%d)\n",
						cj.endSize, cj.numchain, allocsPerChain, elemsPerChain, totalAllocs, totalElems)
				}
				if elemsPerChain > cj.allocs.elemsPerChain {
					return candInfo{}, fmt.Errorf("%s: elemsPerChain > ch.allocs.elemsPerChain", errPrefix)
				}
				deltaElems := (cj.allocs.elemsPerChain - elemsPerChain) * cj.numchain
				globalWaste -= deltaElems
				newTotalAllocs += totalAllocs
				oldTotalAllocs += cj.allocs.totalAllocs
			}
			deltaAllocs := newTotalAllocs - oldTotalAllocs
			if debugFindBest {
				printDebug("Global waste: %d elements\nDelta allocs: %d\n", globalWaste, deltaAllocs)
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
				printDebug("Too few candidates")
			}
			continue
		}
		best_cand := localCandidates[0]
		for i := 1; i < len(localCandidates); i++ {
			if localCandidates[i].globalWaste < best_cand.globalWaste {
				best_cand = localCandidates[i]
			}
		}
		candidates = append(candidates, best_cand)
	}
	if len(candidates) == 0 {
		return candInfo{}, nil
	}
	best_idx := len(candidates) - 1
	for i := 0; i < len(candidates)-1; i++ {
		cur := candidates[i]
		next := candidates[i+1]
		if next.globalWaste > cur.globalWaste {
			best_idx = i
			break
		}
	}
	best_cand := candidates[best_idx]
	best_cand.valid = true
	if best_cand.valid {
		if debugFindBest {
			printDebug("FOUND BEST CHAIN: start_size=%d, end_size=%d, delta_allocs=%s, global_waste=%d\n",
				best_cand.startSize, best_cand.endSize, best_cand.deltaAllocs, best_cand.globalWaste)
		}
	}
	return best_cand, nil
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
		accumCand := accumCandInfo{startSize: s.bestCand.startSize, endSize: s.bestCand.endSize}
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
			if cand.startSize != best.startSize && debug {
				printDebug("FRAME '%s': expected start_size=%d, found=%d\n", fr.str, best.startSize, cand.startSize)
			}
			if cand.deltaAllocs < best.deltaAllocs {
				best = cand
			}
		}
		if (best.startSize == 0 && best.endSize == 1) || (best.startSize == 1 && best.endSize == 2) {
			continue
		}
		if debug {
			printDebug("FRAME '%s': overall best candidate: start_size=%d, end_size=%d (delta_allocs=%d, global_waste=%d\n)",
				fr.str, best.startSize, best.endSize, best.deltaAllocs, best.globalWaste)
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
		accumCand := accumCandInfo{startSize: s.bestCand.startSize, endSize: s.bestCand.endSize}
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
			if cand.startSize != best.startSize && debug {
				printDebug("FRAME PAIR '<%s|%s>': expected start_size=%d, found=%d\n", caller.str, callee.str, best.startSize, cand.startSize)
			}
			if cand.deltaAllocs < best.deltaAllocs {
				best = cand
			}
		}
		if (best.startSize == 0 && best.endSize == 1) || (best.startSize == 1 && best.endSize == 2) {
			continue
		}
		if debug {
			printDebug("FRAME PAIR '<%s|%s>': overall best candidate: start_size=%d, end_size=%d (delta_allocs=%d, global_waste=%d\n)",
				caller.str, callee.str, best.startSize, best.endSize, best.deltaAllocs, best.globalWaste)
		}
		btf := bestTopFrames{callerHash: caller.funcHash, callerLine: caller.line, calleeHash: callee.funcHash, calleeLine: callee.line}
		totalBest[btf] = best
	}
	g.totalCalleeSensitiveBests = totalBest
}

func (g *globalStat) emitProfile(w io.Writer, callerSensitive bool) error {
	allHashes := make(map[funcHash]struct{})
	hashes := make([]funcHash, 0, len(g.totalBests))
	for btf := range g.totalCalleeSensitiveBests {
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
	const header = "EXTRA TRACE PROFILE V1\n"
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
			s := fmt.Sprintf("%d %s %d %d\n", id, line, cand.startSize, cand.endSize)
			lines = append(lines, s)
		}
	}
	slices.Sort(lines)
	for _, l := range lines {
		b.WriteString(l)
	}
	if callerSensitive {
		lineNum = len(g.totalCalleeSensitiveBests)
		b.WriteString(fmt.Sprintf("%d\n", lineNum))
		lines := make([]string, 0, len(g.totalCalleeSensitiveBests))
		for btf, cand := range g.totalCalleeSensitiveBests {
			// caller_id line callee_id line start_size end_size
			callerId, ok := hashToId[btf.callerHash]
			if !ok {
				return fmt.Errorf("%s: no callerId in hashToId", errPrefix)
			}
			calleeId, ok := hashToId[btf.calleeHash]
			if !ok {
				return fmt.Errorf("%s: no calleeId in hashToId", errPrefix)
			}
			s := fmt.Sprintf("%d %s %d %s %d %d\n", callerId, btf.callerLine, calleeId, btf.calleeLine, cand.startSize, cand.endSize)
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

func (s *stack) multistackChainsNumber() (num int, maxLen int) {
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

func (s *stack) printStackSeqs() {
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
	slices.SortStableFunc(stacksByNum, func(x, y *stack) int {
		return y.num - x.num
	})
	printDebug("GENERATION=%d\n", gen.id)
	for _, st := range stacksByNum {
		multistackChains, maxLen := st.multistackChainsNumber()
		printDebug("-------------------------------")
		printDebug("Stack %d (num=%d, hash=%d, chain num=%d, multistack chains=%d, maxlen=%d, intermediate=%d):\n",
			st.id, st.num, st.hash, len(st.chains), multistackChains, maxLen, st.intermediate)
		if multistackChains > 0 {
			printDebug("Multistacks:\n")
			st.printStackSeqs()
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
		caps := make([]int, 0, len(st.newcaps))
		for nc := range st.newcaps {
			caps = append(caps, nc)
		}
		startSizeToNum := make(map[int]int)
		for _, ch := range st.chains {
			startSizeToNum[ch.startSize]++
		}
		for ss, num := range startSizeToNum {
			printDebug("StartSize->num: %d->%d\n", ss, num)
		}
		slices.Sort(caps)
		nums := make([]int, 0, len(caps))
		for _, newcap := range caps {
			number := st.newcaps[newcap]
			nums = append(nums, number)
			printDebug("%d -> %d\n", newcap, number)
		}
	}
}
