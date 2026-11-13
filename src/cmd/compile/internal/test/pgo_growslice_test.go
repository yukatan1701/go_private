// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package test

import (
	"bufio"
	"bytes"
	"fmt"
	"internal/testenv"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

const sliceProfFile = "slice.prof"

type growslice struct {
	pos      string
	startCap string
	predCap  string
}

func testPGOGrowslice(t *testing.T, dir string, want []growslice, sliceProfFile string) {
	testenv.MustHaveGoRun(t)
	t.Parallel()

	const pkg = "example.com/pgo/growslice"

	// Add a go.mod so we have a consistent symbol names in this temp dir.
	goMod := fmt.Sprintf(`module %s
go 1.28
`, pkg)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatalf("error writing go.mod: %v", err)
	}

	// Build the test with the profile.
	pprof := filepath.Join(dir, sliceProfFile)
	gcflag := fmt.Sprintf("-gcflags=-sliceprofile=%s -d=pgoslicedebug=1", pprof)
	exe := filepath.Join(dir, "test.exe")
	cmd := testenv.CleanCmdEnv(testenv.Command(t, testenv.GoToolPath(t), "test", "-c", "-a", "-o", exe, gcflag, "."))
	cmd.Dir = dir
	t.Log(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v, output:\n%s", err, out)
	}

	gotStat := make(map[growslice]struct{})
	gotSpecial := make(map[growslice]struct{})

	foundStat := regexp.MustCompile(`(.*): found statistics for append \(depth=.*, maxDepth=.*\): start_cap=(.*), predicted_cap=(.*)`)
	specialLine := regexp.MustCompile(`(.*): generate growsliceSpecial \(stage=ssa\) \(startCap=(.*), predictedCap=(.*)\)`)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		t.Logf("child: %s", line)

		m := foundStat.FindStringSubmatch(line)
		if m != nil {
			g := growslice{
				pos:      m[1],
				startCap: m[2],
				predCap:  m[3],
			}
			gotStat[g] = struct{}{}
			continue
		}
		m = specialLine.FindStringSubmatch(line)
		if m != nil {
			g := growslice{
				pos:      m[1],
				startCap: m[2],
				predCap:  m[3],
			}
			gotSpecial[g] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("error reading go test output: %v", err)
	}
	if len(gotStat) != len(want) {
		t.Errorf("mismatched growslice stat count; got %v want %v", gotStat, want)
	}
	if len(gotStat) != len(gotSpecial) {
		t.Errorf("mismatched growslice stat and generated calls to growsliceSpecial; got %v want %v", gotStat, gotSpecial)
	}
	for _, w := range want {
		if _, ok := gotStat[w]; !ok {
			t.Errorf("growslice stat %v missing; got %v", w, gotStat)
		}
		if _, ok := gotSpecial[w]; !ok {
			t.Errorf("growsliceSpecial call %v missing; got %v", w, gotSpecial)
		}
	}
}

func growsliceSrcDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("error getting wd: %v", err)
	}
	return filepath.Join(wd, "testdata/pgo/growslice")
}

// setupPGOGrowslice copies the test module and the given slice profile into a
// scratch directory so the test can add a go.mod.
func setupPGOGrowslice(t *testing.T, profile string) string {
	t.Helper()
	srcDir := growsliceSrcDir(t)
	dir := t.TempDir()
	for _, file := range []string{"growslice_predicted.go", "growslice_predicted_test.go", profile} {
		if err := copyFile(filepath.Join(dir, file), filepath.Join(srcDir, file)); err != nil {
			t.Fatalf("error copying %s: %v", file, err)
		}
	}
	return dir
}

func TestPGOGrowslice(t *testing.T) {
	want := []growslice{
		{
			pos:      "./growslice_predicted.go:17:14: doAppend",
			startCap: "0",
			predCap:  "4",
		},
		{
			pos:      "./growslice_predicted.go:25:11: A",
			startCap: "0",
			predCap:  "64",
		},
		{
			pos:      "./growslice_predicted.go:27:11: A",
			startCap: "0",
			predCap:  "4",
		},
	}

	testPGOGrowslice(t, setupPGOGrowslice(t, sliceProfFile), want, sliceProfFile)
}

// TestPGOGrowsliceDepth0Empty checks that statistics from a deeper depth are
// used when the profile has no depth-0 entry for the callsite. The generator
// may omit unprofitable merged stacks while still recording profitable
// per-callsite stacks at a deeper depth.
func TestPGOGrowsliceDepth0Empty(t *testing.T) {
	const profile = "slice_gap0.prof"
	want := []growslice{
		{
			pos:      "./growslice_predicted.go:25:11: A",
			startCap: "0",
			predCap:  "64",
		},
		{
			pos:      "./growslice_predicted.go:27:11: A",
			startCap: "0",
			predCap:  "4",
		},
	}

	testPGOGrowslice(t, setupPGOGrowslice(t, profile), want, profile)
}

// TestPGOGrowsliceDepthFallback checks that a missing deeper-depth entry, or
// an entry whose inlining chain does not match, falls back to the shallower
// statistics instead of failing the compilation.
func TestPGOGrowsliceDepthFallback(t *testing.T) {
	// doAppend is either compiled standalone (depth 0) or inlined into A
	// (depth 1). With only depth-0 statistics available, all three appends
	// use the depth-0 prediction.
	want := []growslice{
		{
			pos:      "./growslice_predicted.go:17:14: doAppend",
			startCap: "0",
			predCap:  "4",
		},
		{
			pos:      "./growslice_predicted.go:25:11: A",
			startCap: "0",
			predCap:  "4",
		},
		{
			pos:      "./growslice_predicted.go:27:11: A",
			startCap: "0",
			predCap:  "4",
		},
	}

	for _, profile := range []string{"slice_gap1.prof", "slice_gap2.prof"} {
		t.Run(profile, func(t *testing.T) {
			testPGOGrowslice(t, setupPGOGrowslice(t, profile), want, profile)
		})
	}
}
