// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package pgo contains the compiler-agnostic portions of PGO profile handling.
// Notably, parsing pprof profiles and serializing/deserializing from a custom
// intermediate representation.
package pgo

import (
	"errors"
	"fmt"
	"internal/profile"
	"io"
	"sort"
)

// FromPProf parses Profile from a pprof profile.
func FromPProf(r io.Reader, csinl bool) (*Profile, error) {
	p, err := profile.Parse(r)
	if errors.Is(err, profile.ErrNoData) {
		// Treat a completely empty file the same as a profile with no
		// samples: nothing to do.
		return emptyProfile(), nil
	} else if err != nil {
		return nil, fmt.Errorf("error parsing profile: %w", err)
	}

	if len(p.Sample) == 0 {
		// We accept empty profiles, but there is nothing to do.
		return emptyProfile(), nil
	}

	valueIndex := -1
	for i, s := range p.SampleType {
		// Samples count is the raw data collected, and CPU nanoseconds is just
		// a scaled version of it, so either one we can find is fine.
		if (s.Type == "samples" && s.Unit == "count") ||
			(s.Type == "cpu" && s.Unit == "nanoseconds") {
			valueIndex = i
			break
		}
	}

	if valueIndex == -1 {
		return nil, fmt.Errorf(`profile does not contain a sample index with value/type "samples/count" or cpu/nanoseconds"`)
	}

	g := profile.NewGraph(p, &profile.Options{
		SampleValue: func(v []int64) int64 { return v[valueIndex] },
	})

	namedEdgeMap, totalWeight, err := createNamedEdgeMap(g)
	if err != nil {
		return nil, err
	}

	if totalWeight == 0 {
		return emptyProfile(), nil // accept but ignore profile with no samples.
	}

	var csiSampleInfo *CSIInfo
	if csinl {
		csiSampleInfo = createCSISampleInfo(p)
	}

	return &Profile{
		TotalWeight:   totalWeight,
		NamedEdgeMap:  namedEdgeMap,
		CSISampleInfo: csiSampleInfo,
	}, nil
}

// createNamedEdgeMap builds a map of callsite-callee edge weights from the
// profile-graph.
//
// Caller should ignore the profile if totalWeight == 0.
func createNamedEdgeMap(g *profile.Graph) (edgeMap NamedEdgeMap, totalWeight int64, err error) {
	seenStartLine := false

	// Process graph and build various node and edge maps which will
	// be consumed by AST walk.
	weight := make(map[NamedCallEdge]int64)
	for _, n := range g.Nodes {
		seenStartLine = seenStartLine || n.Info.StartLine != 0

		canonicalName := n.Info.Name
		// Create the key to the nodeMapKey.
		namedEdge := NamedCallEdge{
			CallerName:     canonicalName,
			CallSiteOffset: n.Info.Lineno - n.Info.StartLine,
		}

		for _, e := range n.Out {
			totalWeight += e.WeightValue()
			namedEdge.CalleeName = e.Dest.Info.Name
			// Create new entry or increment existing entry.
			weight[namedEdge] += e.WeightValue()
		}
	}

	if !seenStartLine {
		// TODO(prattmic): If Function.start_line is missing we could
		// fall back to using absolute line numbers, which is better
		// than nothing.
		return NamedEdgeMap{}, 0, fmt.Errorf("profile missing Function.start_line data (Go version of profiled application too old? Go 1.20+ automatically adds this to profiles)")
	}
	return postProcessNamedEdgeMap(weight, totalWeight)
}

func sortByWeight(edges []NamedCallEdge, weight map[NamedCallEdge]int64) {
	sort.Slice(edges, func(i, j int) bool {
		ei, ej := edges[i], edges[j]
		if wi, wj := weight[ei], weight[ej]; wi != wj {
			return wi > wj // want larger weight first
		}
		// same weight, order by name/line number
		if ei.CallerName != ej.CallerName {
			return ei.CallerName < ej.CallerName
		}
		if ei.CalleeName != ej.CalleeName {
			return ei.CalleeName < ej.CalleeName
		}
		return ei.CallSiteOffset < ej.CallSiteOffset
	})
}

func postProcessNamedEdgeMap(weight map[NamedCallEdge]int64, weightVal int64) (edgeMap NamedEdgeMap, totalWeight int64, err error) {
	if weightVal == 0 {
		return NamedEdgeMap{}, 0, nil // accept but ignore profile with no samples.
	}
	byWeight := make([]NamedCallEdge, 0, len(weight))
	for namedEdge := range weight {
		byWeight = append(byWeight, namedEdge)
	}
	sortByWeight(byWeight, weight)

	edgeMap = NamedEdgeMap{
		Weight:   weight,
		ByWeight: byWeight,
	}

	totalWeight = weightVal

	return edgeMap, totalWeight, nil
}

func createCSISampleInfo(p *profile.Profile) *CSIInfo {
	csiInfo := CSIInfo{
		Graph:         make(map[string]*CSINode),
		SampleToCount: make(map[int]int),
	}
	for j, sample := range p.Sample {
		if len(sample.Location) == 0 {
			continue
		}
		stack := make([]CSICallSite, 0)
		for li := 0; li < len(sample.Location); li++ {
			loc := sample.Location[li]
			// Multiple line indicates this location has inlined functions,
			// where the last entry represents the caller into which the
			// preceding entries were inlined.
			//
			// E.g., if memcpy() is inlined into printf:
			//    line[0].function_name == "memcpy"
			//    line[1].function_name == "printf"
			for i := 0; i < len(loc.Line); i++ {
				line := loc.Line[i]
				funcName := line.Function.SystemName
				lineNum := line.Line
				pos := CSICallSite{FuncName: funcName, Line: int(lineNum)}
				stack = append(stack, pos)
			}
		}
		sampleCount := sample.Value[0]
		if len(stack) <= 1 {
			continue
		}
		csiInfo.SampleToCount[j] = int(sampleCount)

		for i := 0; i < len(stack)-1; i++ {
			frame := stack[i]
			// Add callee node if not exist
			if _, ok := csiInfo.Graph[frame.FuncName]; !ok {
				node := &CSINode{
					FuncName:    frame.FuncName,
					TargetNodes: make(map[*CSINode][]CSIEdge),
					TopLines:    make(map[int]int),
				}
				csiInfo.Graph[frame.FuncName] = node
			}
			callerFrame := stack[i+1]
			// Add caller node if not exist
			if _, ok := csiInfo.Graph[callerFrame.FuncName]; !ok {
				node := &CSINode{
					FuncName:    callerFrame.FuncName,
					TargetNodes: make(map[*CSINode][]CSIEdge),
					TopLines:    make(map[int]int),
				}
				csiInfo.Graph[callerFrame.FuncName] = node
			}
			node := csiInfo.Graph[frame.FuncName]
			callerNode := csiInfo.Graph[callerFrame.FuncName]
			// Add an edge from callee to caller
			if _, ok := (*node).TargetNodes[callerNode]; !ok {
				(*node).TargetNodes[callerNode] = make([]CSIEdge, 0)
			}
			edges := (*node).TargetNodes[callerNode]
			from := frame.Line
			if i == 0 {
				if count, ok := (*node).TopLines[from]; ok {
					(*node).TopLines[from] = count + 1
				} else {
					(*node).TopLines[from] = 1
				}
				from = -1
			}
			to := callerFrame.Line
			// If this edge exists, update sample indices
			foundEdge := false
			for _, edge := range edges {
				if from == edge.From && to == edge.To {
					edge.Samples[j] = true
					foundEdge = true
				}
			}
			if !foundEdge {
				edges = append(edges, CSIEdge{
					From: from,
					To:   to,
					Samples: CSISampleSet{
						j: true,
					},
				})
			}
			(*node).TargetNodes[callerNode] = edges
		}
	}
	return &csiInfo
}
