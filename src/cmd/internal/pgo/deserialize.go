// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pgo

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// IsSerialized returns true if r is a serialized Profile.
//
// IsSerialized only peeks at r, so seeking back after calling is not
// necessary.
func IsSerialized(r *bufio.Reader) (bool, error) {
	hdr, err := r.Peek(len(serializationHeader))
	if err == io.EOF {
		// Empty file.
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("error reading profile header: %w", err)
	}

	return string(hdr) == serializationHeader, nil
}

// FromSerialized parses a profile from serialization output of Profile.WriteTo.
func FromSerialized(r io.Reader) (*Profile, error) {
	d := emptyProfile()

	scanner := bufio.NewScanner(r)
	scanner.Split(bufio.ScanLines)

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
		}
		return nil, fmt.Errorf("preprocessed profile missing header")
	}
	if gotHdr := scanner.Text() + "\n"; gotHdr != serializationHeader {
		return nil, fmt.Errorf("preprocessed profile malformed header; got %q want %q", gotHdr, serializationHeader)
	}

	for scanner.Scan() {
		readStr := scanner.Text()

		callerName := readStr

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
			}
			return nil, fmt.Errorf("preprocessed profile entry missing callee")
		}
		calleeName := scanner.Text()

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
			}
			return nil, fmt.Errorf("preprocessed profile entry missing weight")
		}
		readStr = scanner.Text()

		split := strings.Split(readStr, " ")

		if len(split) != 2 {
			return nil, fmt.Errorf("preprocessed profile entry got %v want 2 fields", split)
		}

		co, err := strconv.Atoi(split[0])
		if err != nil {
			return nil, fmt.Errorf("preprocessed profile error processing call line: %w", err)
		}

		edge := NamedCallEdge{
			CallerName:     callerName,
			CalleeName:     calleeName,
			CallSiteOffset: co,
		}

		weight, err := strconv.ParseInt(split[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("preprocessed profile error processing call weight: %w", err)
		}

		if _, ok := d.NamedEdgeMap.Weight[edge]; ok {
			return nil, fmt.Errorf("preprocessed profile contains duplicate edge %+v", edge)
		}

		d.NamedEdgeMap.ByWeight = append(d.NamedEdgeMap.ByWeight, edge) // N.B. serialization is ordered.
		d.NamedEdgeMap.Weight[edge] += weight
		d.TotalWeight += weight
	}

	return d, nil

}

func FromSerializedCsinl(r io.Reader) (*CSIInfo, error) {
	d := CSIInfo{
		Graph:         make(map[string]*CSINode),
		SampleToCount: make(map[int]int),
	}

	scanner := bufio.NewScanner(r)
	scanner.Split(bufio.ScanLines)

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
		}
		return nil, fmt.Errorf("preprocessed profile missing header")
	}
	if gotHdr := scanner.Text() + "\n"; gotHdr != serializationHeader {
		return nil, fmt.Errorf("preprocessed profile malformed header; got %q want %q", gotHdr, serializationHeader)
	}

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
		}
		return nil, fmt.Errorf("preprocessed profile entry missing function count")
	}
	readStr := scanner.Text()
	funcCount, _ := strconv.Atoi(readStr)
	idxToFunc := make([]string, funcCount)
	idxToNode := make([]*CSINode, funcCount)

	// Read functions and its indices
	for i := 0; i < funcCount; i++ {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
			}
			return nil, fmt.Errorf("preprocessed profile entry missing function index and name")
		}
		readStr := scanner.Text()
		split := strings.SplitN(readStr, " ", 2)
		if len(split) != 2 {
			return nil, fmt.Errorf("preprocessed profile entry got %v want 2 fields", split)
		}
		idx, err := strconv.Atoi(split[0])
		if err != nil {
			return nil, fmt.Errorf("preprocessed profile error processing function index: %w", err)
		}
		fname := split[1]
		idxToFunc[idx] = fname
		newNode := &CSINode{
			FuncName:    fname,
			TargetNodes: make(map[*CSINode][]CSIEdge),
			TopLines:    make(map[int]int),
		}
		idxToNode[idx] = newNode
		d.Graph[fname] = newNode
	}

	// Generate graph nodes
	for i := 0; i < int(funcCount); i++ {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
			}
			return nil, fmt.Errorf("preprocessed profile entry missing function index, target node count and top lines count")
		}
		readStr = scanner.Text()
		split := strings.Split(readStr, " ")
		if len(split) != 3 {
			return nil, fmt.Errorf("preprocessed profile entry got %v want 2 fields", split)
		}
		idx, err := strconv.Atoi(split[0])
		if err != nil {
			return nil, fmt.Errorf("preprocessed profile error processing function index: %w", err)
		}
		targetNodeCount, err := strconv.Atoi(split[1])
		if err != nil {
			return nil, fmt.Errorf("preprocessed profile error processing target node count: %w", err)
		}
		topLinesCount, err := strconv.Atoi(split[2])
		if err != nil {
			return nil, fmt.Errorf("preprocessed profile error processing top lines count: %w", err)
		}
		node := idxToNode[idx]
		for j := 0; j < targetNodeCount; j++ {
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
				}
				return nil, fmt.Errorf("preprocessed profile entry missing target index and edge list length")
			}
			readStr = scanner.Text()
			split = strings.Split(readStr, " ")
			if len(split) != 2 {
				return nil, fmt.Errorf("preprocessed profile entry got %v want 2 fields", split)
			}
			targetIdx, err := strconv.Atoi(split[0])
			if err != nil {
				return nil, fmt.Errorf("preprocessed profile error processing target index: %w", err)
			}
			edgeListLen, err := strconv.Atoi(split[1])
			if err != nil {
				return nil, fmt.Errorf("preprocessed profile error processing edge list length: %w", err)
			}
			targetNode := idxToNode[targetIdx]
			node.TargetNodes[targetNode] = make([]CSIEdge, edgeListLen)
			for k := 0; k < edgeListLen; k++ {
				if !scanner.Scan() {
					if err := scanner.Err(); err != nil {
						return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
					}
					return nil, fmt.Errorf("preprocessed profile entry missing 'from' and 'to' fields value and sample count")
				}
				readStr = scanner.Text()
				split := strings.Split(readStr, " ")
				if len(split) != 3 {
					return nil, fmt.Errorf("preprocessed profile entry got %v want 2 fields", split)
				}
				from, err := strconv.Atoi(split[0])
				if err != nil {
					return nil, fmt.Errorf("preprocessed profile error processing 'from' field value: %w", err)
				}
				to, err := strconv.Atoi(split[1])
				if err != nil {
					return nil, fmt.Errorf("preprocessed profile error processing 'to' field value: %w", err)
				}
				sampleCount, err := strconv.Atoi(split[2])
				if err != nil {
					return nil, fmt.Errorf("preprocessed profile error processing sample count: %w", err)
				}
				edge := &node.TargetNodes[targetNode][k]
				edge.From = from
				edge.To = to
				edge.Samples = make(CSISampleSet)
				for m := 0; m < sampleCount; m++ {
					if !scanner.Scan() {
						if err := scanner.Err(); err != nil {
							return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
						}
						return nil, fmt.Errorf("preprocessed profile entry missing sample index")
					}
					sample := scanner.Text()
					sampleIdx, err := strconv.Atoi(sample)
					if err != nil {
						return nil, fmt.Errorf("preprocessed profile error processing sample index: %w", err)
					}
					edge.Samples[sampleIdx] = true
				}
			}
		}
		for k := 0; k < topLinesCount; k++ {
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
				}
				return nil, fmt.Errorf("preprocessed profile entry missing top line value and top lines count")
			}
			readStr = scanner.Text()
			split := strings.Split(strings.TrimSpace(readStr), " ")
			if len(split) != 2 {
				return nil, fmt.Errorf("preprocessed profile entry got %v want 2 fields", split)
			}
			line, err := strconv.Atoi(split[0])
			if err != nil {
				return nil, fmt.Errorf("preprocessed profile error processing top line value: %w", err)
			}
			count, err := strconv.Atoi(split[1])
			if err != nil {
				return nil, fmt.Errorf("preprocessed profile error processing top lines count: %w", err)
			}
			node.TopLines[line] = count
		}
	}

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
		}
		return nil, fmt.Errorf("preprocessed profile entry missing sample list count")
	}
	readStr = scanner.Text()
	sampleListCount, err := strconv.Atoi(readStr)
	if err != nil {
		return nil, fmt.Errorf("preprocessed profile error processing sample list count: %w", err)
	}
	d.SampleToCount = make(map[int]int)
	for i := 0; i < sampleListCount; i++ {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("error reading preprocessed profile: %w", err)
			}
			return nil, fmt.Errorf("preprocessed profile entry missing sample index and sample count")
		}
		readStr = scanner.Text()
		split := strings.Split(readStr, " ")
		idx, err := strconv.Atoi(split[0])
		if err != nil {
			return nil, fmt.Errorf("preprocessed profile error processing sample index: %w", err)
		}
		count, err := strconv.Atoi(split[1])
		if err != nil {
			return nil, fmt.Errorf("preprocessed profile error processing sample count: %w", err)
		}
		d.SampleToCount[idx] = count
	}

	return &d, nil
}
