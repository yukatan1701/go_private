// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// WARNING: Please avoid updating this file. If this file needs to be updated,
// then a new slice.prof file should be generated:
//
//  $ cd $GOROOT/src/cmd/compile/internal/test/testdata/pgo/growslice/
//  $ go mod init example.com/pgo/growslice
//  $ GODEBUG=tracegrowslice=1 go test -bench=. -benchtime=10000x -trace=trace.out
//  $ go tool trace -profextra=slice trace.out > slice.prof

package growslice

func doAppend(a *[]int, n int) {
	for i := range n {
		*a = append(*a, i)
	}
}

//go:noinline
func A(i int) {
	var a []int
	if i%2 == 0 {
		doAppend(&a, 64)
	} else {
		doAppend(&a, 4)
	}
}
