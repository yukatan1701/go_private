package pgoir

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"fmt"
)

func LoadAppendCounters(funcs []*ir.Func, prof *SliceProfile) {
	if prof == nil {
		return
	}
	ir.VisitFuncsBottomUp(funcs, func(funcs []*ir.Func, recursive bool) {
		for _, fn := range funcs {
			loadAppendCounters(fn, prof)
		}
	})
}

func loadAppendCounters(fn *ir.Func, prof *SliceProfile) {
	if prof == nil || len(prof.DepthTrees) == 0 {
		return
	}
	// TODO: check several appends on the same line
	fname := ir.LinkFuncName(fn)
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
		/*if base.Debug.SliceProfDebug > 0 {
			fmt.Printf("\nParent: %v, %s\n", parent, name)
		}*/
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
			fmt.Printf("%v: %v: found statistics for append (depth=%d, maxDepth=%d): start_size=%d, end_size=%d\n",
				ir.Line(n), fname, depth, len(prof.DepthTrees), stat.StartCap, stat.EndCap)
		}
		exp := n.(*ir.CallExpr)
		exp.AppendProfile = &ir.AppendStat{StartSize: stat.StartCap, EndSize: stat.EndCap}
	}
	ir.Visit(fn, visit)
}
