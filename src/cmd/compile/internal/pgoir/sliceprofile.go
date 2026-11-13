package pgoir

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"fmt"
)

func LoadAppendStatistics(funcs []*ir.Func, prof *SliceProfile) {
	if prof == nil {
		return
	}
	ir.VisitFuncsBottomUp(funcs, func(funcs []*ir.Func, recursive bool) {
		for _, fn := range funcs {
			loadAppendStatistics(fn, prof)
		}
	})
}

type callPos struct {
	filename string
	line     uint
}

func loadAppendStatistics(fn *ir.Func, prof *SliceProfile) {
	if prof == nil || len(prof.DepthTrees) == 0 {
		return
	}
	fname := ir.LinkFuncName(fn)
	posToCall := make(map[callPos]*ir.CallExpr)
	var visit func(n ir.Node)
	visit = func(n ir.Node) {
		if n.Op() != ir.OAPPEND {
			return
		}
		name := fname
		parent := base.Ctxt.PosTable.Pos(n.Pos()).Base().InliningIndex()
		if parent >= 0 {
			// If a function containing append has been inlined,
			// find its actual name instead of the outermost caller name.
			name = base.Ctxt.InlTree.InlinedFunction(parent).Name
		}
		ipos := base.Ctxt.InnermostPos(n.Pos())
		line := int(ipos.RelLine())
		cs := GrowsliceCallsite{Func: name, Line: line}
		inlChain := []GrowsliceCallsite{}
		parentLine := n.Pos().Line()
		for inlIndex := parent; inlIndex >= 0; inlIndex = base.Ctxt.InlTree.Parent(inlIndex) {
			parentName := base.Ctxt.InlTree.InlinedFunction(inlIndex).Name
			inlChain = append(inlChain, GrowsliceCallsite{Func: parentName, Line: int(parentLine)})
			parentLine = base.Ctxt.InlTree.CallPos(inlIndex).Line()
		}
		inlChain = append(inlChain, GrowsliceCallsite{Func: fname, Line: int(parentLine)})

		// A profile does not have to contain an entry at every depth for every
		// callsite: the generator omits unprofitable stacks. Try the most
		// specific depth first, then fall back to shallower depths, which
		// carry less precise but still valid statistics for the same callsite.
		// A missing entry is not an error; it just means there is nothing to
		// specialize here.
		depth := min(len(inlChain), len(prof.DepthTrees)) - 1
		stat, statDepth, ok := findAppendStat(prof, cs, inlChain, depth)
		if !ok {
			return
		}
		if base.Debug.PGOSliceDebug > 0 {
			fmt.Printf("%v: %v: found statistics for append (depth=%d, maxDepth=%d): start_cap=%d, predicted_cap=%d\n",
				ir.Line(n), ir.FuncName(fn), statDepth, len(prof.DepthTrees)-1, stat.StartCap, stat.PredictedCap)
		}
		pos := callPos{filename: ipos.AbsFilename(), line: ipos.RelLine()}
		if call, ok := posToCall[pos]; ok && ipos.RelCol() != base.Ctxt.InnermostPos(call.Pos()).RelCol() {
			// We already have a call to growslice on this line.
			// Slice profile does not consider columns and we cannot distinguish
			// appends for different slices. So, skip such calls conservatively.
			call.AppendInfo = nil
			if base.Debug.PGOSliceDebug > 0 {
				fmt.Printf("%v: %v: found several appends on the same line, skip them all.\n",
					ir.Line(n), ir.FuncName(fn))
			}
		} else {
			exp := n.(*ir.CallExpr)
			exp.AppendInfo = &ir.AppendStat{StartCap: stat.StartCap, PredictedCap: stat.PredictedCap}
			posToCall[pos] = exp
		}
	}

	ir.Visit(fn, visit)
}

// findAppendStat returns the statistics recorded for the append callsite cs
// in prof. It searches the most specific inlining depth first and falls back
// to shallower depths if the profile has no complete chain for cs at that
// depth. inlChain is ordered from the function containing the append
// (inlChain[0]) outwards through its callers, so a depth-d entry for cs is
// reached by following inlChain[1]..inlChain[d].
func findAppendStat(prof *SliceProfile, cs GrowsliceCallsite, inlChain []GrowsliceCallsite, depth int) (GrowsliceStat, int, bool) {
	for d := depth; d >= 0; d-- {
		node, ok := prof.DepthTrees[d][cs]
		if !ok || node == nil {
			continue
		}
		complete := true
		for i := 1; i <= d; i++ {
			caller, ok := node.Callers[inlChain[i]]
			if !ok {
				complete = false
				break
			}
			node = caller
		}
		if complete {
			return node.Stat, d, true
		}
	}
	return GrowsliceStat{}, 0, false
}
