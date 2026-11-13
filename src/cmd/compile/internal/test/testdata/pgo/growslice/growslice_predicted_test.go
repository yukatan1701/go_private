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

import "testing"

func BenchmarkAppend(b *testing.B) {
	for i := range b.N {
		A(i)
	}
}
