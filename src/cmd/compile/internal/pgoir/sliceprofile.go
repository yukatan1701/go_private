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
	fname := ir.LinkFuncName(fn)
	var visit func(n ir.Node)
	visit = func(n ir.Node) {
		if n.Op() != ir.OAPPEND {
			return
		}
		parentInlIdx := base.Ctxt.PosTable.Pos(n.Pos()).Base().InliningIndex()
		calleeName := fname
		callerName := fname
		if parentInlIdx >= 0 { // parent caller inlined
			calleeName = base.Ctxt.InlTree.InlinedFunction(parentInlIdx).Name
			parentInlIdx2 := base.Ctxt.InlTree.Parent(parentInlIdx)
			if parentInlIdx2 >= 0 {
				callerName = base.Ctxt.InlTree.InlinedFunction(parentInlIdx2).Name
			}
		}
		if calleeName == callerName {
			cnt, ok := prof.Counters[calleeName]
			if !ok {
				return
			}
			line := int(base.Ctxt.InnermostPos(n.Pos()).RelLine())
			if stat, ok := cnt[line]; ok {
				if base.Debug.SliceProfDebug > 0 {
					fmt.Printf("%v: %v: found statistics for append: start_size=%d, end_size=%d\n",
						ir.Line(n), fname, stat.StartSize, stat.EndSize)
				}
				exp := n.(*ir.CallExpr)
				exp.AppendProfile = &ir.AppendStat{StartSize: stat.StartSize, EndSize: stat.EndSize}
			}
		} else {
			callerLine := base.Ctxt.InlTree.CallPos(parentInlIdx).Line()
			calleeLine := n.Pos().Line()
			callerc := GrowsliceContext{Func: callerName, Line: int(callerLine)}
			calleec := GrowsliceContext{Func: calleeName, Line: int(calleeLine)}
			if calleeCtxt, ok := prof.CallerSensitiveCounters[callerc]; ok {
				if stat, ok := calleeCtxt[calleec]; ok {
					if base.Debug.SliceProfDebug > 0 {
						fmt.Printf("%v: %v: found caller-sensitive statistics for append: start_size=%d, end_size=%d\n",
							ir.Line(n), fname, stat.StartSize, stat.EndSize)
					}
					exp := n.(*ir.CallExpr)
					exp.AppendProfile = &ir.AppendStat{StartSize: stat.StartSize, EndSize: stat.EndSize}
				}
			}
		}

	}
	ir.Visit(fn, visit)
}
