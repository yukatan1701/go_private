// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pgo

import (
	"bufio"
	"fmt"
	"io"
)

// Serialization of a Profile allows go tool preprofile to construct the edge
// map only once (rather than once per compile process). The compiler processes
// then parse the pre-processed data directly from the serialized format.
//
// The format of the serialized output is as follows.
//
//      GO PREPROFILE V1
//      caller_name
//      callee_name
//      "call site offset" "call edge weight"
//      ...
//      caller_name
//      callee_name
//      "call site offset" "call edge weight"
//
// Entries are sorted by "call edge weight", from highest to lowest.

const serializationHeader = "GO PREPROFILE V1\n"

// WriteTo writes a serialized representation of Profile to w.
//
// FromSerialized can parse the format back to Profile.
//
// WriteTo implements io.WriterTo.Write.
func (d *Profile) WriteTo(w io.Writer) (int64, error) {
	bw := bufio.NewWriter(w)

	var written int64

	// Header
	n, err := bw.WriteString(serializationHeader)
	written += int64(n)
	if err != nil {
		return written, err
	}

	for _, edge := range d.NamedEdgeMap.ByWeight {
		weight := d.NamedEdgeMap.Weight[edge]

		n, err = fmt.Fprintln(bw, edge.CallerName)
		written += int64(n)
		if err != nil {
			return written, err
		}

		n, err = fmt.Fprintln(bw, edge.CalleeName)
		written += int64(n)
		if err != nil {
			return written, err
		}

		n, err = fmt.Fprintf(bw, "%d %d\n", edge.CallSiteOffset, weight)
		written += int64(n)
		if err != nil {
			return written, err
		}
	}

	if err := bw.Flush(); err != nil {
		return written, err
	}

	// No need to serialize TotalWeight, it can be trivially recomputed
	// during parsing.

	return written, nil
}

// WriteCsinlTo writes a serialized representation of context-sensitive inline profile to w.
//
// FromSerializedCsinl can parse the format back to Profile.
//
// WriteCsinlTo implements io.WriterTo.Write.
func (d *Profile) WriteCsinlTo(w io.Writer) (int64, error) {
	bw := bufio.NewWriter(w)

	var written int64

	n, err := bw.WriteString(serializationHeader)
	written += int64(n)
	if err != nil {
		return written, err
	}

	if d.CSISampleInfo != nil {
		graph := &(*d.CSISampleInfo).Graph
		n, err = fmt.Fprintf(bw, "%d\n", len(*graph))
		written += int64(n)
		if err != nil {
			return written, err
		}

		funcToIdx := make(map[string]int)
		i := 0
		for fname := range *graph {
			funcToIdx[fname] = i
			n, err = fmt.Fprintf(bw, "%d %s\n", i, fname)
			written += int64(n)
			if err != nil {
				return written, err
			}
			i++
		}
		for fname, idx := range funcToIdx {
			node := (*graph)[fname]
			edgeNum := len(node.TargetNodes)
			n, err = fmt.Fprintf(bw, "%d %d %d\n", idx, edgeNum, len(node.TopLines))
			written += int64(n)
			if err != nil {
				return written, err
			}
			for targetNode, edges := range node.TargetNodes {
				funcIdx := funcToIdx[targetNode.FuncName]
				n, err = fmt.Fprintf(bw, "%d %d\n", funcIdx, len(edges))
				written += int64(n)
				if err != nil {
					return written, err
				}
				for _, edge := range edges {
					n, err = fmt.Fprintf(bw, "%d %d %d\n", edge.From, edge.To, len(edge.Samples))
					written += int64(n)
					if err != nil {
						return written, err
					}
					for sample := range edge.Samples {
						n, err = fmt.Fprintf(bw, "%d\n", sample)
						written += int64(n)
						if err != nil {
							return written, err
						}
					}
				}
			}
			for lineIdx, sampleCount := range node.TopLines {
				n, err = fmt.Fprintf(bw, "%d %d\n", lineIdx, sampleCount)
				written += int64(n)
				if err != nil {
					return written, err
				}
			}
		}

		sampleNum := len((*d.CSISampleInfo).SampleToCount)
		n, err = fmt.Fprintf(bw, "%d\n", sampleNum)
		written += int64(n)
		if err != nil {
			return written, err
		}

		for idx, count := range (*d.CSISampleInfo).SampleToCount {
			n, err = fmt.Fprintf(bw, "%d %d\n", idx, count)
			written += int64(n)
			if err != nil {
				return written, err
			}
		}
	}

	if err := bw.Flush(); err != nil {
		return written, err
	}

	return written, nil
}
