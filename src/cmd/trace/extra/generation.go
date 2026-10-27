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
	ptrs      []uintptr
	caps      []int
	startSize int
	endSize   int
	stackSeq  []uint64 // list of stackId
}

type chainPtrInfo struct {
	rootStack *stack
	chainIdx  int
}

type stack struct {
	id                uint64
	frames            []frame
	newcaps           map[int]int //newcap -> number
	num               int         // how many times stack appeared in a trace
	hash              stackHash
	chains            []chain
	lastPtrToChainIdx map[uintptr]int
	intermediate      bool
}

type generation struct {
	id             int
	stacks         map[uint64]*stack
	stringIdToHash map[int]stringHash
	hashToStack    map[uint64]*stack
	chainInfo      map[uintptr]chainPtrInfo
}

func newStack(id uint64) *stack {
	return &stack{
		id:      id,
		frames:  make([]frame, 0),
		newcaps: make(map[int]int),
		chains:  make([]chain, 0),
	}
}

func newGeneration(id int) *generation {
	return &generation{
		id:             id,
		stacks:         make(map[uint64]*stack),
		stringIdToHash: make(map[int]stringHash),
		hashToStack:    make(map[uint64]*stack),
		chainInfo:      make(map[uintptr]chainPtrInfo),
	}
}

const errPrefix string = "failed to parse extra event"

func (gen *generation) readGrowslice(ev *raw.Event) error {
	if ev.Ev != tracev2.EvGrowslice {
		return fmt.Errorf("%s: invalid event: got %d, want %d", errPrefix, ev.Ev, tracev2.Growslice)
	}
	// Args: dt (skip), oldPtr, newPtr, oldCap, newCap, stackId
	spec := ev.Version.Specs()[ev.Ev]
	argNum := len(spec.Args)
	if len(ev.Args) != argNum {
		return fmt.Errorf("%s: invalid arg number: got %d, want %d", errPrefix, len(ev.Args), argNum)
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
	ptrInfo, ok := gen.chainInfo[oldPtr]
	if !ok {
		// No such chain in the current generation, create new one.
		// Current stack is a root stack.
		idx := len(stack.chains)
		stack.chains = append(stack.chains, chain{
			ptrs:      append([]uintptr{}, oldPtr, newPtr),
			caps:      append([]int{}, newCap),
			startSize: oldCap,
			endSize:   newCap,
			stackSeq:  append([]uint64{}, stackId),
		})
		gen.chainInfo[newPtr] = chainPtrInfo{
			rootStack: stack,
			chainIdx:  idx,
		}
		stack.newcaps[newCap]++
		return nil
	}
	rootStack := ptrInfo.rootStack
	chain := &rootStack.chains[ptrInfo.chainIdx]
	if len(chain.ptrs) < 2 {
		return fmt.Errorf("%s: too few pointers in a chain: got %d, want >= 2", errPrefix, len(chain.ptrs))
	}
	if len(chain.stackSeq) == 0 {
		return fmt.Errorf("%s: no stack sequence for a non-empty chain", errPrefix)
	}
	chain.ptrs = append(chain.ptrs, newPtr)
	chain.caps = append(chain.caps, newCap)
	chain.endSize = newCap
	rootStack.newcaps[newCap]++
	if stack != rootStack {
		lastStackId := chain.stackSeq[len(chain.stackSeq)-1]
		if lastStackId != stackId {
			chain.stackSeq = append(chain.stackSeq, stackId)
		}
		stack.intermediate = true
	}
	delete(gen.chainInfo, oldPtr)
	gen.chainInfo[newPtr] = chainPtrInfo{
		rootStack: rootStack,
		chainIdx:  ptrInfo.chainIdx,
	}
	return nil
}

func (gen *generation) readStack(ev *raw.Event) error {
	if ev.Ev != tracev2.EvStack {
		return fmt.Errorf("%s: invalid event: got %d, want %d", errPrefix, ev.Ev, tracev2.EvStack)
	}
	spec := ev.Version.Specs()[ev.Ev]
	if !spec.IsStack {
		return fmt.Errorf("%s: invalid event type, stack expected", errPrefix)
	}
	id := ev.Args[0]
	stack, ok := gen.stacks[id]
	if !ok {
		return nil
	}
	frames := ev.Args[len(spec.Args):]
	for i := 0; i < len(frames); i += 4 {
		funcId := frames[i+1]
		fileId := frames[i+2]
		line := strconv.Itoa(int(frames[i+3]))
		stack.frames = append(stack.frames, frame{funcId: int(funcId), fileId: int(fileId), line: line})
	}
	return nil
}

func (gen *generation) readString(ev *raw.Event, gs *globalStat) error {
	if ev.Ev != tracev2.EvString {
		return fmt.Errorf("%s: invalid event: got %d, want %d", errPrefix, ev.Ev, tracev2.EvString)
	}
	if ev.Data == nil {
		return fmt.Errorf("%s: data == nil for EvString", errPrefix)
	}
	id := int(ev.Args[0])
	datastr := string(ev.Data)
	hash, err := hashString64(datastr)
	if err != nil {
		return fmt.Errorf("%s: %v", errPrefix, err)
	}
	gen.stringIdToHash[id] = hash
	if str, ok := gs.hashToString[hash]; ok && str != datastr {
		return fmt.Errorf("%s: string mismatch for the same id=%d: got %s, want %s", errPrefix, id, str, datastr)
	} else if !ok {
		gs.hashToString[hash] = datastr
	}
	return nil
}
