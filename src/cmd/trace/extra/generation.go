package extra

import (
	"fmt"
	"internal/trace/raw"
	"internal/trace/tracev2"
	"strconv"
)

type frame struct {
	funcId int
	fileId int
	line   string
}

type chain struct {
	ptrs     []uintptr
	caps     []int
	startCap int
	endCap   int
	stackSeq []uint64 // list of stackId
}

type stack struct {
	id           uint64
	frames       []frame
	newCaps      map[int]int // newCap -> number (how many times a capacity occured)
	num          int         // how many times stack appeared in a trace
	hash         stackHash
	chains       []chain
	intermediate bool
}

type chainPtrInfo struct {
	rootStack *stack
	// Chain index in rootStack.chains list
	chainIdx int
	lastCap  int
}

type generation struct {
	id int
	// stackId -> *stack
	stacks         map[uint64]*stack
	stringIdToHash map[int]stringHash
	// Each root stack holds pointer chains started at a callsite related to this root stack.
	//
	//
	// 0xa -> 0xb -> [0xc]
	// 0xe -> [0xf]
	//
	tailPtrToChainPos map[uintptr]chainPtrInfo
}

func newStack(id uint64) *stack {
	return &stack{
		id:      id,
		frames:  make([]frame, 0),
		newCaps: make(map[int]int),
		chains:  make([]chain, 0),
	}
}

func newGeneration(id int) *generation {
	return &generation{
		id:                id,
		stacks:            make(map[uint64]*stack),
		stringIdToHash:    make(map[int]stringHash),
		tailPtrToChainPos: make(map[uintptr]chainPtrInfo),
	}
}

const parsingErrPrefix string = "failed to parse extra event"

func (gen *generation) readGrowslice(ev *raw.Event) error {
	if ev.Ev != tracev2.EvGrowslice {
		return fmt.Errorf("%s: invalid event: got %d, want %d", parsingErrPrefix, ev.Ev, tracev2.Growslice)
	}
	// Args: dt (skip), oldPtr, newPtr, oldCap, newCap, stackId
	spec := ev.Version.Specs()[ev.Ev]
	argNum := len(spec.Args)
	if len(ev.Args) != argNum {
		return fmt.Errorf("%s: invalid arg number: got %d, want %d", parsingErrPrefix, len(ev.Args), argNum)
	}
	oldPtr := uintptr(ev.Args[1])
	newPtr := uintptr(ev.Args[2])
	oldCap := int(ev.Args[3])
	newCap := int(ev.Args[4])
	stackId := ev.Args[5]
	if _, ok := gen.stacks[stackId]; !ok {
		gen.stacks[stackId] = newStack(stackId)
	}
	stack := gen.stacks[stackId]
	stack.num++
	chainPos, ok := gen.tailPtrToChainPos[oldPtr]
	if !ok || chainPos.lastCap != oldCap {
		// No such chain in the current generation, create new one.
		// Current stack is a root stack.
		idx := len(stack.chains)
		stack.chains = append(stack.chains, chain{
			ptrs:     append([]uintptr{}, oldPtr, newPtr),
			caps:     append([]int{}, newCap),
			startCap: oldCap,
			endCap:   newCap,
			stackSeq: append([]uint64{}, stackId),
		})
		gen.tailPtrToChainPos[newPtr] = chainPtrInfo{
			rootStack: stack,
			chainIdx:  idx,
			lastCap:   newCap,
		}
		stack.newCaps[newCap]++
		return nil
	}
	rootStack := chainPos.rootStack
	chain := &rootStack.chains[chainPos.chainIdx]
	if len(chain.ptrs) < 2 {
		return fmt.Errorf("%s: too few pointers in a chain: got %d, want >= 2", parsingErrPrefix, len(chain.ptrs))
	}
	if len(chain.stackSeq) == 0 {
		return fmt.Errorf("%s: no stack sequence for a non-empty chain", parsingErrPrefix)
	}
	chain.ptrs = append(chain.ptrs, newPtr)
	chain.caps = append(chain.caps, newCap)
	chain.endCap = newCap
	rootStack.newCaps[newCap]++
	if stack != rootStack {
		lastStackId := chain.stackSeq[len(chain.stackSeq)-1]
		if lastStackId != stackId {
			chain.stackSeq = append(chain.stackSeq, stackId)
		}
		stack.intermediate = true
	}
	delete(gen.tailPtrToChainPos, oldPtr)
	gen.tailPtrToChainPos[newPtr] = chainPtrInfo{
		rootStack: rootStack,
		chainIdx:  chainPos.chainIdx,
		lastCap:   newCap,
	}
	return nil
}

const maxStackDepth = 2

func (gen *generation) readStack(ev *raw.Event) error {
	if ev.Ev != tracev2.EvStack {
		return fmt.Errorf("%s: invalid event: got %d, want %d", parsingErrPrefix, ev.Ev, tracev2.EvStack)
	}
	spec := ev.Version.Specs()[ev.Ev]
	if !spec.IsStack {
		return fmt.Errorf("%s: invalid event type, stack expected", parsingErrPrefix)
	}
	id := ev.Args[0]
	stack, ok := gen.stacks[id]
	if !ok {
		// This stack hasn't appeared in Growslice events, skip it.
		return nil
	}
	frames := ev.Args[len(spec.Args):]
	depth := 0
	for i := 0; i < len(frames) && depth < maxStackDepth; i += 4 {
		funcId := frames[i+1]
		fileId := frames[i+2]
		line := strconv.Itoa(int(frames[i+3]))
		stack.frames = append(stack.frames, frame{funcId: int(funcId), fileId: int(fileId), line: line})
		depth++
	}
	return nil
}

func (gen *generation) readString(ev *raw.Event, gs *globalStat) error {
	if ev.Ev != tracev2.EvString {
		return fmt.Errorf("%s: invalid event: got %d, want %d", parsingErrPrefix, ev.Ev, tracev2.EvString)
	}
	if ev.Data == nil {
		return fmt.Errorf("%s: data == nil for EvString", parsingErrPrefix)
	}
	id := int(ev.Args[0])
	datastr := string(ev.Data)
	hash, err := hashString64(datastr)
	if err != nil {
		return fmt.Errorf("%s: %v", parsingErrPrefix, err)
	}
	gen.stringIdToHash[id] = hash
	if str, ok := gs.hashToString[hash]; ok && str != datastr {
		return fmt.Errorf("%s: string mismatch for the same id=%d: got %s, want %s", parsingErrPrefix, id, str, datastr)
	} else if !ok {
		gs.hashToString[hash] = datastr
	}
	return nil
}
