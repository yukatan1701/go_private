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
	fileIdx int32
	line    uint
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
			name = base.Ctxt.InlTree.InlinedFunction(parent).Name
		}
		line := int(base.Ctxt.InnermostPos(n.Pos()).RelLine())
		cs := GrowsliceCallsite{Func: name, Line: line}
		if _, ok := prof.DepthTrees[0][cs]; !ok {
			return
		}
		inlChain := []GrowsliceCallsite{}
		parentLine := n.Pos().Line()
		for inlIndex := parent; inlIndex >= 0; inlIndex = base.Ctxt.InlTree.Parent(inlIndex) {
			parentName := base.Ctxt.InlTree.InlinedFunction(inlIndex).Name
			inlChain = append(inlChain, GrowsliceCallsite{Func: parentName, Line: int(parentLine)})
			parentLine = base.Ctxt.InlTree.CallPos(inlIndex).Line()
		}
		inlChain = append(inlChain, GrowsliceCallsite{Func: fname, Line: int(parentLine)})
		depth := min(len(inlChain), len(prof.DepthTrees))
		node := prof.DepthTrees[depth-1][cs]
		for i := 1; i < len(inlChain); i++ {
			callsite := inlChain[i]
			if caller, ok := node.Callers[callsite]; !ok {
				// No such inlining chain in the profile.
				return
			} else {
				node = caller
			}
		}
		stat := node.Stat
		if base.Debug.SliceProfDebug > 0 {
			fmt.Printf("%v: %v: found statistics for append (depth=%d, maxDepth=%d): start_cap=%d, predicted_cap=%d\n",
				ir.Line(n), ir.FuncName(fn), depth, len(prof.DepthTrees), stat.StartCap, stat.PredictedCap)
		}
		exp := n.(*ir.CallExpr)
		pos := callPos{fileIdx: exp.Pos().FileIndex(), line: exp.Pos().Line()}
		if call, ok := posToCall[pos]; ok {
			// We already have a call to growslice on this line.
			// Slice profile does not consider columns and we cannot distinguish
			// appends for different slices. So, skip such calls conservatively.
			call.AppendInfo = nil
			if base.Debug.SliceProfDebug > 0 {
				fmt.Printf("%v: %v: found several appends on the same position, skip them all.\n",
					ir.Line(n), ir.FuncName(fn))
			}
		} else {
			exp.AppendInfo = &ir.AppendStat{StartCap: stat.StartCap, PredictedCap: stat.PredictedCap}
			posToCall[pos] = exp
		}
	}

	ir.Visit(fn, visit)
}
