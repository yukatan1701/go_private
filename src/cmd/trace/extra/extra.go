package extra

import (
	"internal/trace/raw"
	"internal/trace/tracev2"
	"io"
	"os"
)

func ProcessExtraTrace(trc io.Reader) error {
	rr, err := raw.NewReader(trc)
	if err != nil {
		return err
	}
	gs := globalStat{
		hashToString: make(map[stringHash]string),
		globalStacks: make(map[stackHash]*globalStack),
		globalFrames: make(map[frameHash]*globalFrame),
	}
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
			if debug {
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
		if debug {
			gs.printGenerationStat(gen)
		}
	}
	gs.filterStacks()
	err = gs.findBestSolutions()
	if err != nil {
		return err
	}
	if debug {
		gs.printGlobalStat()
	}
	gs.bestForTopFrames()
	gs.bestForTopFramesWithCaller()
	err = gs.emitProfile(os.Stdout, true)
	return err
}
