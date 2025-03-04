## [Proposal] cmd/compile: use context-sensitivity to prevent inlining of cold callsites in PGO

### Abstract

This proposal aims to support context-sensitive inline in the Go Compiler. Context-sensitive inline extends the current inlining approach. It considers both an inlining path and profile information when deciding whether a function is worth being inlined in a particular callsite. It helps reduce code size without any significant performance changes. This proposal is based on Wenlei He, Hongtao Yu, Lei Wang, Taewook Oh, "Revamping Sampling-Based PGO with Context-Sensitivity and Pseudo-instrumentation" [paper](https://ieeexplore.ieee.org/stamp/stamp.jsp?tp=&arnumber=10444807). Section III.B of the paper ("Context-sensitive Sampling-based PGO") contains details.

### Problem

This example demonstrates in which cases this optimization can be useful:
```
// context_sensitive_inline_test.go
 
package context_sensitive_inline
 
import "testing"
 
type Opcode int
 
const (
    OpAdd Opcode = iota
    OpSub
)
 
func AddVectorHead(V1, V2 []int) int {
    return scalarOp(V1[0], V2[0], OpAdd)
}
 
func SubVectorHead(V1, V2 []int) int {
    return scalarOp(V1[0], V2[0], OpSub)
}
 
func scalarOp(E1, E2 int, Op Opcode) int {
    switch Op {
    case OpAdd:
        return scalarAdd(E1, E2)
    case OpSub:
        return scalarSub(E1, E2)
    default:
        return 0
    }
}
 
func scalarAdd(E1, E2 int) int {  // Call will be inlined in the SubVectorHead, but should not
    return E1 + E2
}
 
func scalarSub(E1, E2 int) int {  // Call will be inlined in the AddVectorHead, but should not
    return E1 - E2
}
 
func BenchmarkFoo(b *testing.B) {
    V1 := []int{1}
    V2 := []int{1}
    for i := 0; i < b.N; i++ {
        for i := 0; i < 60000; i++ {
            V1[0] = AddVectorHead(V1, V2)
        }
        for i := 0; i < 30000; i++ {
            V1[0] = SubVectorHead(V1, V2)
        }
    }
    println(V1[0])
}
```
As can be seen, `scalarSub` is never called when invoking `scalarOp` from `AddVectorHead`. The Go Compiler's inliner can't handle such cases and inlines both `scalarAdd` and `scalarSub` in `scalarOp` by default. In this simple example, unreachable calls will be eliminated later, after constant propagation and dead code elimination. However, this approach does not work in more complex cases where specific variable values are unknown. In such situations, we can use profile information to prevent the inlining of cold call sites, which helps reduce code size.

A callsite may be cold on one execution path and hot on another. In the example above, a call to `scalarAdd` is hot when `scalarOp` is invoked from `AddVectorHead` while a call to `scalarSub` is cold. After inlining `scalarOp` in `AddVectorHead`, the nodes of the inlined function body are traversed. Since we know the inlining path `AddVectorHead -> scalarOp`, we can check whether the path `AddVectorHead -> scalarOp -> scalarSub` is cold before inlining `scalarSub`.

### Proposed changes

One possible solution is to use profile information. A collected profile contains a list of samples representing call stacks along with additional data. If there are no samples that match the inlining path (i.e. if this path didn't appear in the samples), the inlining of `scalarSub` can be prevented in this context. In this particular example, it may be best to inline `scalarSub` everywhere as it would need only a single `sub` instruction instead of a function call. However, preventing inlining can be beneficial when applied to relatively large functions. Context-sensitivity involves considering the path through a call graph that corresponds to the call stack leading to the current call site.

Collect a profile for the example:
```
$ go test -bench=. context_sensitive_inline_test.go -cpuprofile=default.pprof
```
Compile with PGO enabled and look at inlining decisions:
```
$ go test -pgo=default.pprof -gcflags='-m' context_sensitive_inline_test.go
 
# command-line-arguments [command-line-arguments.test]
./context_sensitive_inline_test.go:33:6: can inline scalarAdd
./context_sensitive_inline_test.go:37:6: can inline scalarSub
./context_sensitive_inline_test.go:22:6: can inline scalarOp
./context_sensitive_inline_test.go:25:25: inlining call to scalarAdd
./context_sensitive_inline_test.go:27:25: inlining call to scalarSub
./context_sensitive_inline_test.go:14:6: can inline AddVectorHead
./context_sensitive_inline_test.go:15:20: inlining call to scalarOp
./context_sensitive_inline_test.go:15:20: inlining call to scalarAdd
./context_sensitive_inline_test.go:15:20: inlining call to scalarSub // unwanted inlining
./context_sensitive_inline_test.go:18:6: can inline SubVectorHead
./context_sensitive_inline_test.go:19:20: inlining call to scalarOp
./context_sensitive_inline_test.go:19:20: inlining call to scalarAdd // unwanted inlining
./context_sensitive_inline_test.go:19:20: inlining call to scalarSub
./context_sensitive_inline_test.go:41:6: can inline BenchmarkFoo
./context_sensitive_inline_test.go:46:34: inlining call to AddVectorHead
./context_sensitive_inline_test.go:49:34: inlining call to SubVectorHead
./context_sensitive_inline_test.go:46:34: inlining call to scalarOp
./context_sensitive_inline_test.go:49:34: inlining call to scalarOp
./context_sensitive_inline_test.go:46:34: inlining call to scalarAdd
./context_sensitive_inline_test.go:46:34: inlining call to scalarSub // unwanted inlining
./context_sensitive_inline_test.go:49:34: inlining call to scalarAdd // unwanted inlining
./context_sensitive_inline_test.go:49:34: inlining call to scalarSub
...
```
As we can see on the last four lines, both `scalarAdd` and `scalarSub` were inlined into `AddVectorHead` and `SubVectorHead`.

Context-Sensitive Inline prevents cold callsites from being inlined:
```
$ go test -pgo=default.pprof -pgocsinl -gcflags='-m' context_sensitive_inline_test.go
 
# command-line-arguments [command-line-arguments.test]
./context_sensitive_inline_test.go:33:6: can inline scalarAdd
./context_sensitive_inline_test.go:37:6: can inline scalarSub
./context_sensitive_inline_test.go:22:6: can inline scalarOp
./context_sensitive_inline_test.go:25:25: inlining call to scalarAdd
./context_sensitive_inline_test.go:27:25: inlining call to scalarSub
./context_sensitive_inline_test.go:14:6: can inline AddVectorHead
./context_sensitive_inline_test.go:15:20: inlining call to scalarOp
./context_sensitive_inline_test.go:15:20: inlining call to scalarAdd
./context_sensitive_inline_test.go:18:6: can inline SubVectorHead
./context_sensitive_inline_test.go:19:20: inlining call to scalarOp
./context_sensitive_inline_test.go:19:20: inlining call to scalarSub
./context_sensitive_inline_test.go:41:6: can inline BenchmarkFoo
./context_sensitive_inline_test.go:46:34: inlining call to AddVectorHead
./context_sensitive_inline_test.go:49:34: inlining call to SubVectorHead
./context_sensitive_inline_test.go:46:34: inlining call to scalarOp
./context_sensitive_inline_test.go:49:34: inlining call to scalarOp
./context_sensitive_inline_test.go:46:34: inlining call to scalarAdd
./context_sensitive_inline_test.go:49:34: inlining call to scalarSub
...
```

### Implementation details

For this example, there are only 7 samples in the profile:
```
Sample 0 [count: 24]: [ [scalarAdd:34] [scalarOp:25] [AddVectorHead:15] [BenchmarkFoo:46] [testing.(*B).runN:193] [testing.(*B).launch:316] ]
Sample 1 [count: 10]: [ [BenchmarkFoo:48] [testing.(*B).runN:193] [testing.(*B).launch:316] ]
Sample 2 [count: 15]: [ [BenchmarkFoo:45] [testing.(*B).runN:193] [testing.(*B).launch:316] ]
Sample 3 [count: 24]: [ [BenchmarkFoo:49] [testing.(*B).runN:193] [testing.(*B).launch:316] ]
Sample 4 [count: 32]: [ [BenchmarkFoo:46] [testing.(*B).runN:193] [testing.(*B).launch:316] ]
Sample 5 [count: 16]: [ [scalarSub:38] [scalarOp:27] [SubVectorHead:19] [BenchmarkFoo:49] [testing.(*B).runN:193] [testing.(*B).launch:316] ]
Sample 6 [count: 1]:  [ [scalarOp:27] [SubVectorHead:19] [BenchmarkFoo:49] [testing.(*B).runN:193] [testing.(*B).launch:316] ]
```
Each sample represents a call stack and its occurrence count in the profile. Stack frames are sorted from leaf to root.
1. Extract all samples from the profile and store them in a convenient data structure that enables the compiler to quickly check whether an inlining path is present in any sample. A special context-sensitive inline graph was created for this purpose. Each node represents a single function. The nodes of the graph are connected by edges, depending on whether the stack frames corresponding to the functions are adjacent in samples. For example, nodes scalarOp and subVectorHead are connected by an edge with the tag <27, 19> (samples 5, 6), which represents the stack frame line pair. CS-graph looks like the following:
```
            +---------------------+
            | testing.(*B).launch |
            +---------------------+
                       |
                       | <316, 193>
                       V
            +---------------------+
            |  testing.(*B).runN  |
            +---------------------+
                       |
                       | <193, 48>  <193, 45>
                       | <193, 49>  <193, 46>
                       V
                +--------------+
         -------| BenchmarkFoo |-------
<46, 15> |      +--------------+      | <49, 19>
         |                            |
         V                            V
+---------------+              +---------------+
| AddVectorHead |              | SubVectorHead |
+---------------+              +---------------+
         |                            |
<15, 25> |                            | <19, 27>
         |        +----------+        |
         -------->|          |<--------
                  | scalarOp |
         ---------|          |---------
         |        +----------+        |
<25, 34> |                            | <27, 38>
         V                            V
   +-----------+                +-----------+
   | scalarAdd |                | scalarSub |
   +-----------+                +-----------+
```
2. The Go compiler performs inlining level by level, meaning it inlines inlinable callees in the root function, then traverses the inlined nodes and repeats the process for new inlinable callees. This approach allows for extracting the inlining path at each level and attempting to find it in the profile using the graph mentioned above. For example, the path `AddVectorHead:15 -> scalarOp:25 -> scalarSub` doesn't appear in the profile, so inlining of scalarSub may be prevented in this case. However, `scalarAdd` is worth being inlined after finding the `AddVectorHead:15 -> scalarOp:25 -> scalarAdd`  path in the graph.
3. CS-inline threshold can be adjusted to prevents inlining of callees whose cost is above a specific value. For example, to apply the optimization only for functions with cost >= 40 while the current default inline threshold is 80.

### Results
Benchmarking with Sweet (threshold = 40) shows that this approach can reduce code size by up to 5.11% without any significant performance changes. Some internal tests show a code size reduction of 10% (threshold = 30).
<details>
<summary>ARMv8 Kunpeng920</summary>

```
                          │  base.stat   │            csinline.stat            │
                          │    sec/op    │    sec/op     vs base               │
BiogoIgor-4                  25.05 ±  2%    24.16 ±  2%  -3.53% (p=0.000 n=10)
BiogoKrishna-4               24.54 ±  0%    24.54 ±  0%       ~ (p=0.971 n=10)
BleveIndexBatch100-4         11.03 ±  2%    11.04 ±  1%       ~ (p=1.000 n=10)
CockroachDBkv0/nodes=1-2    1.095m ± 29%   1.119m ± 30%       ~ (p=0.739 n=10)
CockroachDBkv50/nodes=1-2   836.2µ ± 19%   730.1µ ±  8%       ~ (p=0.105 n=10)
CockroachDBkv95/nodes=1-2   399.2µ ± 13%   361.8µ ± 17%       ~ (p=0.684 n=10)
CockroachDBkv0/nodes=3-2    1.062m ± 23%   1.176m ± 19%       ~ (p=0.481 n=10)
CockroachDBkv50/nodes=3-2   801.6µ ± 21%   800.8µ ± 20%       ~ (p=1.000 n=10)
CockroachDBkv95/nodes=3-2   389.2µ ± 14%   391.3µ ±  9%       ~ (p=0.739 n=10)
EtcdPut-4                   52.31m ±  4%   52.38m ±  3%       ~ (p=0.684 n=10)
EtcdSTM-4                   286.1m ±  5%   281.3m ±  3%       ~ (p=0.123 n=10)
GoBuildKubelet-4             166.2 ±  4%    167.1 ±  3%       ~ (p=0.684 n=10)
GoBuildKubeletLink-4         15.86 ± 10%    15.68 ±  9%       ~ (p=0.912 n=10)
GoBuildIstioctl-4            127.9 ±  3%    128.7 ±  3%       ~ (p=0.280 n=10)
GoBuildIstioctlLink-4        9.482 ± 16%    9.470 ± 15%       ~ (p=0.912 n=10)
GoBuildFrontend-4            47.68 ±  0%    47.91 ±  0%  +0.47% (p=0.003 n=10)
GoBuildFrontendLink-4        2.199 ±  1%    2.214 ±  0%  +0.68% (p=0.023 n=10)
GopherLuaKNucleotide-4       33.70 ±  0%    34.05 ±  0%  +1.05% (p=0.000 n=10)
MarkdownRenderXHTML-4       281.0m ±  0%   280.2m ±  0%  -0.30% (p=0.002 n=10)
geomean                     407.4m         404.5m        -0.72%

                          │     base.stat     │              csinline.stat               │
                          │ average-RSS-bytes │ average-RSS-bytes  vs base               │
BiogoIgor-4                      63.94Mi ± 1%        65.09Mi ± 1%  +1.81% (p=0.000 n=10)
BiogoKrishna-4                   3.732Gi ± 0%        3.732Gi ± 0%       ~ (p=0.912 n=10)
BleveIndexBatch100-4             188.0Mi ± 1%        187.6Mi ± 0%       ~ (p=0.631 n=10)
CockroachDBkv0/nodes=1-2         4.144Gi ± 4%        4.136Gi ± 5%       ~ (p=0.631 n=10)
CockroachDBkv50/nodes=1-2        4.025Gi ± 4%        3.971Gi ± 3%       ~ (p=0.436 n=10)
CockroachDBkv95/nodes=1-2        3.356Gi ± 4%        3.347Gi ± 4%       ~ (p=0.529 n=10)
CockroachDBkv0/nodes=3-2         4.151Gi ± 6%        4.076Gi ± 7%       ~ (p=0.971 n=10)
CockroachDBkv50/nodes=3-2        3.920Gi ± 4%        3.933Gi ± 9%       ~ (p=0.631 n=10)
CockroachDBkv95/nodes=3-2        3.435Gi ± 4%        3.299Gi ± 3%  -3.96% (p=0.009 n=10)
EtcdPut-4                        101.6Mi ± 1%        101.2Mi ± 2%       ~ (p=0.631 n=10)
EtcdSTM-4                        91.47Mi ± 1%        93.22Mi ± 1%  +1.91% (p=0.000 n=10)
GopherLuaKNucleotide-4           34.28Mi ± 1%        34.14Mi ± 1%       ~ (p=0.089 n=10)
MarkdownRenderXHTML-4            18.66Mi ± 1%        18.83Mi ± 2%       ~ (p=0.225 n=10)
geomean                          587.1Mi             585.5Mi       -0.29%

                          │   base.stat    │             csinline.stat             │
                          │ peak-RSS-bytes │ peak-RSS-bytes  vs base               │
BiogoIgor-4                  86.11Mi ±  3%    88.57Mi ±  2%  +2.86% (p=0.000 n=10)
BiogoKrishna-4               4.159Gi ±  0%    4.159Gi ±  0%       ~ (p=0.079 n=10)
BleveIndexBatch100-4         268.3Mi ±  2%    268.0Mi ±  1%       ~ (p=0.481 n=10)
CockroachDBkv0/nodes=1-2     7.710Gi ± 16%    7.577Gi ± 12%       ~ (p=0.481 n=10)
CockroachDBkv50/nodes=1-2    7.213Gi ± 12%    7.385Gi ± 10%       ~ (p=0.853 n=10)
CockroachDBkv95/nodes=1-2    5.282Gi ±  8%    5.469Gi ±  5%       ~ (p=0.247 n=10)
CockroachDBkv0/nodes=3-2     7.104Gi ± 21%    7.686Gi ± 22%       ~ (p=0.481 n=10)
CockroachDBkv50/nodes=3-2    7.152Gi ±  8%    7.099Gi ± 10%       ~ (p=0.971 n=10)
CockroachDBkv95/nodes=3-2    5.620Gi ±  8%    5.087Gi ± 12%  -9.50% (p=0.007 n=10)
EtcdPut-4                    142.2Mi ±  3%    140.5Mi ±  4%       ~ (p=0.247 n=10)
EtcdSTM-4                    117.4Mi ±  2%    119.0Mi ±  2%       ~ (p=0.105 n=10)
GopherLuaKNucleotide-4       36.11Mi ±  2%    36.20Mi ±  1%       ~ (p=0.853 n=10)
MarkdownRenderXHTML-4        19.59Mi ±  2%    20.00Mi ±  2%  +2.13% (p=0.015 n=10)
geomean                      845.2Mi          849.4Mi        +0.50%

                          │   base.stat   │            csinline.stat             │
                          │ peak-VM-bytes │ peak-VM-bytes  vs base               │
BiogoIgor-4                 1.237Gi ±  0%   1.237Gi ±  0%  -0.01% (p=0.000 n=10)
BiogoKrishna-4              5.303Gi ±  0%   5.303Gi ±  0%       ~ (p=0.551 n=10)
BleveIndexBatch100-4        1.869Gi ±  3%   1.869Gi ±  0%  -0.02% (p=0.002 n=10)
CockroachDBkv0/nodes=1-2    9.684Gi ± 12%   9.610Gi ± 10%       ~ (p=0.631 n=10)
CockroachDBkv50/nodes=1-2   9.187Gi ± 11%   9.413Gi ±  9%       ~ (p=0.853 n=10)
CockroachDBkv95/nodes=1-2   6.832Gi ± 10%   6.983Gi ±  5%       ~ (p=0.436 n=10)
CockroachDBkv0/nodes=3-2    9.066Gi ± 16%   9.693Gi ± 16%       ~ (p=0.353 n=10)
CockroachDBkv50/nodes=3-2   9.090Gi ±  6%   9.089Gi ±  7%       ~ (p=0.739 n=10)
CockroachDBkv95/nodes=3-2   7.123Gi ±  6%   6.510Gi ± 10%  -8.61% (p=0.007 n=10)
EtcdPut-4                   11.32Gi ±  1%   11.25Gi ±  1%  -0.56% (p=0.003 n=10)
EtcdSTM-4                   11.25Gi ±  0%   11.25Gi ±  0%  -0.01% (p=0.000 n=10)
GopherLuaKNucleotide-4      1.174Gi ±  0%   1.174Gi ±  0%  -0.01% (p=0.000 n=10)
MarkdownRenderXHTML-4       1.174Gi ±  0%   1.174Gi ±  0%  -0.01% (p=0.032 n=10)
geomean                     4.825Gi         4.828Gi        +0.07%

                          │       base.stat       │                csinline.stat                 │
                          │ write-avg-latency-sec │ write-avg-latency-sec  vs base               │
CockroachDBkv0/nodes=1-2              11.45 ± 23%             11.50 ± 30%       ~ (p=0.912 n=10)
CockroachDBkv50/nodes=1-2             13.08 ± 22%             11.27 ± 14%       ~ (p=0.165 n=10)
CockroachDBkv95/nodes=1-2             4.971 ± 62%             4.810 ± 62%       ~ (p=0.796 n=10)
CockroachDBkv0/nodes=3-2              10.92 ± 22%             12.30 ± 22%       ~ (p=0.529 n=10)
CockroachDBkv50/nodes=3-2             12.44 ± 33%             12.43 ± 20%       ~ (p=0.684 n=10)
CockroachDBkv95/nodes=3-2             7.058 ± 36%             4.569 ± 95%       ~ (p=0.190 n=10)
geomean                               9.454                   8.706        -7.91%

                          │       base.stat        │                 csinline.stat                  │
                          │ write-p100-latency-sec │ write-p100-latency-sec  vs base                │
CockroachDBkv0/nodes=1-2               39.73 ± 19%              40.80 ± 11%        ~ (p=1.000 n=10)
CockroachDBkv50/nodes=1-2              42.95 ± 10%              42.95 ± 23%        ~ (p=1.000 n=10)
CockroachDBkv95/nodes=1-2              24.16 ± 20%              22.01 ± 46%        ~ (p=0.400 n=10)
CockroachDBkv0/nodes=3-2               39.73 ± 19%              45.10 ± 19%  +13.51% (p=0.035 n=10)
CockroachDBkv50/nodes=3-2              40.80 ± 32%              39.73 ± 14%        ~ (p=0.227 n=10)
CockroachDBkv95/nodes=3-2              29.53 ± 24%              25.77 ± 25%        ~ (p=0.238 n=10)
geomean                                35.42                    34.82         -1.69%

                          │       base.stat       │                csinline.stat                 │
                          │ write-p50-latency-sec │ write-p50-latency-sec  vs base               │
CockroachDBkv0/nodes=1-2              8.053 ±  7%            8.590 ±  31%       ~ (p=0.156 n=10)
CockroachDBkv50/nodes=1-2             10.20 ± 37%            10.20 ±  21%       ~ (p=0.779 n=10)
CockroachDBkv95/nodes=1-2             3.959 ± 69%            3.758 ± 100%       ~ (p=0.956 n=10)
CockroachDBkv0/nodes=3-2              8.322 ± 16%            8.456 ±  14%       ~ (p=0.563 n=10)
CockroachDBkv50/nodes=3-2             11.01 ± 44%            11.27 ±  19%       ~ (p=0.486 n=10)
CockroachDBkv95/nodes=3-2             6.174 ± 41%            3.557 ± 119%       ~ (p=0.109 n=10)
geomean                               7.541                  6.939         -7.98%

                          │       base.stat       │                 csinline.stat                 │
                          │ write-p95-latency-sec │ write-p95-latency-sec  vs base                │
CockroachDBkv0/nodes=1-2              29.53 ± 24%             30.60 ± 12%        ~ (p=0.694 n=10)
CockroachDBkv50/nodes=1-2             28.99 ± 15%             25.23 ± 23%  -12.96% (p=0.037 n=10)
CockroachDBkv95/nodes=1-2             13.15 ± 39%             12.62 ± 28%        ~ (p=0.897 n=10)
CockroachDBkv0/nodes=3-2              28.99 ± 46%             31.14 ± 31%        ~ (p=0.279 n=10)
CockroachDBkv50/nodes=3-2             27.92 ± 19%             28.99 ± 19%        ~ (p=0.809 n=10)
CockroachDBkv95/nodes=3-2             17.72 ± 33%             14.76 ± 45%        ~ (p=0.224 n=10)
geomean                               23.34                   22.50         -3.57%

                          │       base.stat       │                csinline.stat                 │
                          │ write-p99-latency-sec │ write-p99-latency-sec  vs base               │
CockroachDBkv0/nodes=1-2              33.82 ± 21%             34.90 ± 11%       ~ (p=0.752 n=10)
CockroachDBkv50/nodes=1-2             34.90 ± 14%             33.82 ± 14%       ~ (p=0.446 n=10)
CockroachDBkv95/nodes=1-2             20.40 ± 26%             18.25 ± 47%       ~ (p=0.269 n=10)
CockroachDBkv0/nodes=3-2              36.51 ± 35%             37.58 ± 31%       ~ (p=0.146 n=10)
CockroachDBkv50/nodes=3-2             33.82 ± 17%             33.82 ± 14%       ~ (p=0.783 n=10)
CockroachDBkv95/nodes=3-2             20.94 ± 28%             20.94 ± 28%       ~ (p=0.986 n=10)
geomean                               29.22                   28.82        -1.36%

                          │   base.stat   │            csinline.stat             │
                          │ write-ops/sec │ write-ops/sec  vs base               │
CockroachDBkv0/nodes=1-2      912.5 ± 22%     893.0 ± 23%       ~ (p=0.739 n=10)
CockroachDBkv50/nodes=1-2     547.0 ± 24%     625.0 ±  8%       ~ (p=0.149 n=10)
CockroachDBkv95/nodes=1-2     123.5 ± 16%     132.5 ± 19%       ~ (p=0.403 n=10)
CockroachDBkv0/nodes=3-2      941.0 ± 22%     849.5 ± 23%       ~ (p=0.481 n=10)
CockroachDBkv50/nodes=3-2     574.5 ± 27%     566.0 ± 18%       ~ (p=0.781 n=10)
CockroachDBkv95/nodes=3-2     119.0 ± 27%     127.5 ± 20%       ~ (p=0.541 n=10)
geomean                       397.8           406.8        +2.26%

                          │  base.stat   │            csinline.stat            │
                          │  write-ops   │  write-ops    vs base               │
CockroachDBkv0/nodes=1-2    54.80k ± 22%   53.62k ± 23%       ~ (p=0.739 n=10)
CockroachDBkv50/nodes=1-2   32.86k ± 25%   37.59k ±  8%       ~ (p=0.143 n=10)
CockroachDBkv95/nodes=1-2   7.431k ± 15%   7.970k ± 19%       ~ (p=0.393 n=10)
CockroachDBkv0/nodes=3-2    56.54k ± 22%   51.00k ± 23%       ~ (p=0.481 n=10)
CockroachDBkv50/nodes=3-2   34.53k ± 27%   34.02k ± 18%       ~ (p=0.796 n=10)
CockroachDBkv95/nodes=3-2   7.178k ± 27%   7.662k ± 20%       ~ (p=0.579 n=10)
geomean                     23.92k         24.45k        +2.19%

                          │      base.stat       │                csinline.stat                │
                          │ read-avg-latency-sec │ read-avg-latency-sec  vs base               │
CockroachDBkv50/nodes=1-2            4.502 ± 18%            3.854 ± 12%       ~ (p=0.165 n=10)
CockroachDBkv95/nodes=1-2            4.011 ±  9%            3.710 ±  6%       ~ (p=0.218 n=10)
CockroachDBkv50/nodes=3-2            4.398 ±  8%            4.088 ± 27%       ~ (p=0.393 n=10)
CockroachDBkv95/nodes=3-2            3.933 ±  7%            4.004 ± 10%       ~ (p=0.912 n=10)
geomean                              4.204                  3.911        -6.97%

                          │       base.stat       │                csinline.stat                 │
                          │ read-p100-latency-sec │ read-p100-latency-sec  vs base               │
CockroachDBkv50/nodes=1-2             40.80 ± 11%             38.65 ± 14%       ~ (p=0.123 n=10)
CockroachDBkv95/nodes=1-2             24.70 ± 17%             21.47 ± 40%       ~ (p=0.158 n=10)
CockroachDBkv50/nodes=3-2             38.65 ± 11%             36.51 ± 18%       ~ (p=0.419 n=10)
CockroachDBkv95/nodes=3-2             28.45 ± 25%             25.23 ± 15%       ~ (p=0.146 n=10)
geomean                               32.45                   29.57        -8.86%

                          │      base.stat       │                csinline.stat                │
                          │ read-p50-latency-sec │ read-p50-latency-sec  vs base               │
CockroachDBkv50/nodes=1-2           1.409 ±  33%            1.275 ± 42%       ~ (p=0.195 n=10)
CockroachDBkv95/nodes=1-2           3.087 ±   9%            3.154 ± 11%       ~ (p=0.811 n=10)
CockroachDBkv50/nodes=3-2           1.409 ± 100%            1.309 ± 38%       ~ (p=0.490 n=10)
CockroachDBkv95/nodes=3-2           3.020 ±  16%            3.221 ±  8%       ~ (p=0.139 n=10)
geomean                             2.074                   2.029        -2.18%

                          │      base.stat       │                csinline.stat                 │
                          │ read-p95-latency-sec │ read-p95-latency-sec  vs base                │
CockroachDBkv50/nodes=1-2            19.86 ± 19%            16.91 ± 21%  -14.86% (p=0.042 n=10)
CockroachDBkv95/nodes=1-2            9.664 ± 44%            9.932 ± 30%        ~ (p=0.724 n=10)
CockroachDBkv50/nodes=3-2            19.33 ± 17%            18.79 ± 20%        ~ (p=0.753 n=10)
CockroachDBkv95/nodes=3-2            12.88 ± 50%            12.88 ± 37%        ~ (p=0.342 n=10)
geomean                              14.79                  14.20         -3.96%

                          │      base.stat       │                csinline.stat                │
                          │ read-p99-latency-sec │ read-p99-latency-sec  vs base               │
CockroachDBkv50/nodes=1-2            23.09 ± 21%            22.01 ± 12%       ~ (p=0.158 n=10)
CockroachDBkv95/nodes=1-2            18.79 ± 14%            17.18 ± 13%       ~ (p=0.086 n=10)
CockroachDBkv50/nodes=3-2            23.09 ± 12%            22.55 ± 24%       ~ (p=0.995 n=10)
CockroachDBkv95/nodes=3-2            19.33 ± 17%            19.86 ± 14%       ~ (p=0.513 n=10)
geomean                              20.97                  20.29        -3.28%

                          │  base.stat   │            csinline.stat            │
                          │ read-ops/sec │ read-ops/sec  vs base               │
CockroachDBkv50/nodes=1-2    644.0 ± 24%    743.0 ±  8%       ~ (p=0.118 n=10)
CockroachDBkv95/nodes=1-2   2.385k ± 15%   2.631k ± 15%       ~ (p=0.684 n=10)
CockroachDBkv50/nodes=3-2    672.5 ± 26%    684.0 ± 15%       ~ (p=0.971 n=10)
CockroachDBkv95/nodes=3-2   2.452k ± 12%   2.434k ±  8%       ~ (p=0.698 n=10)
geomean                     1.262k         1.343k        +6.47%

                          │  base.stat   │            csinline.stat            │
                          │   read-ops   │   read-ops    vs base               │
CockroachDBkv50/nodes=1-2   38.70k ± 24%   44.64k ±  8%       ~ (p=0.123 n=10)
CockroachDBkv95/nodes=1-2   143.2k ± 15%   158.0k ± 15%       ~ (p=0.684 n=10)
CockroachDBkv50/nodes=3-2   40.41k ± 26%   41.09k ± 15%       ~ (p=0.971 n=10)
CockroachDBkv95/nodes=3-2   147.3k ± 12%   146.1k ±  8%       ~ (p=0.739 n=10)
geomean                     75.79k         80.67k        +6.44%

          │    base.stat    │             csinline.stat              │
          │ p50-latency-sec │ p50-latency-sec  vs base               │
EtcdPut-4       49.23m ± 4%       49.14m ± 4%       ~ (p=0.971 n=10)
EtcdSTM-4       207.0m ± 4%       203.1m ± 2%  -1.89% (p=0.029 n=10)
geomean         100.9m            99.89m       -1.04%

          │    base.stat    │             csinline.stat              │
          │ p90-latency-sec │ p90-latency-sec  vs base               │
EtcdPut-4       78.33m ± 3%       78.50m ± 1%       ~ (p=0.853 n=10)
EtcdSTM-4       570.6m ± 6%       561.1m ± 3%       ~ (p=0.075 n=10)
geomean         211.4m            209.9m       -0.73%

          │    base.stat    │             csinline.stat              │
          │ p99-latency-sec │ p99-latency-sec  vs base               │
EtcdPut-4       107.7m ± 5%       107.4m ± 7%       ~ (p=0.579 n=10)
EtcdSTM-4        1.131 ± 5%        1.124 ± 4%       ~ (p=0.190 n=10)
geomean         349.1m            347.5m       -0.46%

          │  base.stat  │           csinline.stat            │
          │    ops/s    │    ops/s     vs base               │
EtcdPut-4   18.16k ± 4%   18.15k ± 3%       ~ (p=0.684 n=10)
EtcdSTM-4   3.421k ± 5%   3.470k ± 3%       ~ (p=0.138 n=10)
geomean     7.883k        7.936k       +0.67%


---------------------------------------------------------------------------------
Section .text size (bytes):
biogo-igor-bench: 1523956 -> 1482340 (-2.73%)
biogo-krishna-bench: 1500948 -> 1441332 (-3.97%)
bleve-index-bench: 3873476 -> 3723924 (-3.86%)
etcd: 9820420 -> 9336980 (-4.92%)
go-build-bench: 1386596 -> 1348756 (-2.73%)
gopher-lua-bench: 1634948 -> 1566260 (-4.20%)
markdown: 1489972 -> 1429876 (-4.03%)
tile38-bench: 2808404 -> 2680468 (-4.56%)
tile38-server: 10753876 -> 10204900 (-5.10%)
bazelisk: 2824340 -> 2739156 (-3.02%)
cockroach: 75333284 -> 73028660 (-3.06%)
cockroachdb-bench: 2931892 -> 2837076 (-3.23%)
cockroach-short: 75333284 -> 73028660 (-3.06%)
```
</details>

<details>
<summary>Intel(R) Xeon(R) CPU E5-2690 v3 @ 2.60GHz</summary>
  
```
                       │  base.stat  │           csinline.stat            │
                       │   sec/op    │   sec/op     vs base               │
BiogoIgor-4               21.95 ± 1%    22.07 ± 1%       ~ (p=0.123 n=10)
BiogoKrishna-4            22.62 ± 0%    22.58 ± 0%       ~ (p=0.165 n=10)
BleveIndexBatch100-4      6.189 ± 2%    6.177 ± 2%       ~ (p=0.853 n=10)
EtcdPut-4                131.3m ± 5%   129.9m ± 3%       ~ (p=0.436 n=10)
EtcdSTM-4                468.4m ± 2%   470.3m ± 1%       ~ (p=0.631 n=10)
GoBuildKubelet-4          153.8 ± 4%    154.3 ± 4%       ~ (p=0.436 n=10)
GoBuildKubeletLink-4      11.36 ± 2%    11.37 ± 2%       ~ (p=0.971 n=10)
GoBuildIstioctl-4         122.8 ± 5%    123.5 ± 5%       ~ (p=0.353 n=10)
GoBuildIstioctlLink-4     12.27 ± 2%    12.29 ± 2%       ~ (p=0.684 n=10)
GoBuildFrontend-4         43.04 ± 3%    43.39 ± 3%       ~ (p=0.280 n=10)
GoBuildFrontendLink-4     1.628 ± 3%    1.643 ± 2%       ~ (p=0.190 n=10)
GopherLuaKNucleotide-4    27.31 ± 1%    27.24 ± 0%       ~ (p=0.247 n=10)
MarkdownRenderXHTML-4    253.9m ± 0%   254.2m ± 0%       ~ (p=0.165 n=10)
Tile38QueryLoad-4        565.4µ ± 0%   562.5µ ± 0%  -0.52% (p=0.001 n=10)
geomean                   3.812         3.816       +0.13%

                       │     base.stat     │              csinline.stat               │
                       │ average-RSS-bytes │ average-RSS-bytes  vs base               │
BiogoIgor-4                  67.13Mi ±  1%        67.25Mi ± 2%       ~ (p=0.853 n=10)
BiogoKrishna-4               3.886Gi ±  0%        3.885Gi ± 0%  -0.03% (p=0.029 n=10)
BleveIndexBatch100-4         200.9Mi ±  1%        200.5Mi ± 1%       ~ (p=0.684 n=10)
EtcdPut-4                    111.3Mi ±  1%        109.9Mi ± 2%       ~ (p=0.105 n=10)
EtcdSTM-4                    102.9Mi ±  2%        101.0Mi ± 1%       ~ (p=0.105 n=10)
GopherLuaKNucleotide-4       34.77Mi ±  2%        34.69Mi ± 1%       ~ (p=0.912 n=10)
MarkdownRenderXHTML-4        19.11Mi ± 10%        20.82Mi ± 9%       ~ (p=0.165 n=10)
Tile38QueryLoad-4            5.735Gi ±  0%        5.739Gi ± 1%       ~ (p=0.684 n=10)
geomean                      198.4Mi              199.7Mi       +0.66%

                       │   base.stat    │             csinline.stat             │
                       │ peak-RSS-bytes │ peak-RSS-bytes  vs base               │
BiogoIgor-4                93.49Mi ± 2%     93.63Mi ± 3%       ~ (p=1.000 n=10)
BiogoKrishna-4             4.159Gi ± 0%     4.159Gi ± 0%       ~ (p=0.165 n=10)
BleveIndexBatch100-4       286.5Mi ± 2%     284.5Mi ± 1%       ~ (p=0.481 n=10)
EtcdPut-4                  147.6Mi ± 2%     146.5Mi ± 2%       ~ (p=0.280 n=10)
EtcdSTM-4                  128.4Mi ± 3%     128.0Mi ± 3%       ~ (p=0.631 n=10)
GopherLuaKNucleotide-4     37.50Mi ± 0%     37.31Mi ± 0%  -0.52% (p=0.004 n=10)
MarkdownRenderXHTML-4      21.35Mi ± 0%     21.27Mi ± 1%       ~ (p=0.089 n=10)
Tile38QueryLoad-4          5.837Gi ± 0%     5.858Gi ± 1%       ~ (p=0.280 n=10)
geomean                    238.1Mi          237.5Mi       -0.26%

                       │   base.stat   │            csinline.stat             │
                       │ peak-VM-bytes │ peak-VM-bytes  vs base               │
BiogoIgor-4               1.237Gi ± 0%    1.237Gi ± 0%       ~ (p=0.094 n=10)
BiogoKrishna-4            5.303Gi ± 0%    5.303Gi ± 0%  -0.00% (p=0.019 n=10)
BleveIndexBatch100-4      1.933Gi ± 0%    1.932Gi ± 0%  -0.01% (p=0.002 n=10)
EtcdPut-4                 11.26Gi ± 0%    11.26Gi ± 0%  -0.01% (p=0.000 n=10)
EtcdSTM-4                 11.26Gi ± 0%    11.26Gi ± 0%  -0.01% (p=0.000 n=10)
GopherLuaKNucleotide-4    1.174Gi ± 0%    1.174Gi ± 0%       ~ (p=0.261 n=10)
MarkdownRenderXHTML-4     1.174Gi ± 0%    1.174Gi ± 0%       ~ (p=0.257 n=10)
Tile38QueryLoad-4         6.995Gi ± 1%    6.994Gi ± 1%       ~ (p=0.393 n=10)
geomean                   3.340Gi         3.340Gi       -0.01%

                  │    base.stat    │             csinline.stat              │
                  │ p50-latency-sec │ p50-latency-sec  vs base               │
EtcdPut-4               132.8m ± 6%       133.3m ± 4%       ~ (p=0.739 n=10)
EtcdSTM-4               318.4m ± 3%       318.1m ± 2%       ~ (p=0.912 n=10)
Tile38QueryLoad-4       269.0µ ± 0%       268.9µ ± 0%       ~ (p=0.838 n=10)
geomean                 22.49m            22.51m       +0.08%

                  │    base.stat    │             csinline.stat              │
                  │ p90-latency-sec │ p90-latency-sec  vs base               │
EtcdPut-4               183.3m ± 6%       182.5m ± 3%       ~ (p=0.579 n=10)
EtcdSTM-4               982.4m ± 2%       985.9m ± 1%       ~ (p=0.631 n=10)
Tile38QueryLoad-4       923.5µ ± 0%       920.9µ ± 0%  -0.28% (p=0.035 n=10)
geomean                 54.99m            54.92m       -0.12%

                  │    base.stat    │             csinline.stat              │
                  │ p99-latency-sec │ p99-latency-sec  vs base               │
EtcdPut-4               239.8m ± 6%       238.9m ± 7%       ~ (p=0.971 n=10)
EtcdSTM-4                2.149 ± 2%        2.155 ± 3%       ~ (p=0.436 n=10)
Tile38QueryLoad-4       5.081m ± 1%       5.046m ± 1%  -0.69% (p=0.019 n=10)
geomean                 137.8m            137.5m       -0.25%

                  │  base.stat  │           csinline.stat            │
                  │    ops/s    │    ops/s     vs base               │
EtcdPut-4           7.524k ± 5%   7.588k ± 3%       ~ (p=0.565 n=10)
EtcdSTM-4           2.111k ± 2%   2.105k ± 1%       ~ (p=0.447 n=10)
Tile38QueryLoad-4   5.306k ± 0%   5.333k ± 0%  +0.52% (p=0.001 n=10)
geomean             4.384k        4.400k       +0.36%


---------------------------------------------------------------------------------
Section .text size (bytes):
biogo-igor-bench: 1622833 -> 1564305 (-3.61%)
biogo-krishna: 1599281 -> 1521233 (-4.88%)
bleve-index-bench: 4275569 -> 4085297 (-4.45%)
etcd: 10931345 -> 10395825 (-4.90%)
go-build-bench: 1479793 -> 1430481 (-3.33%)
gopher-lua-bench: 1738545 -> 1652753 (-4.93%)
tile38-server: 11874289 -> 11267313 (-5.11%)
tile38-bench: 3169393 -> 3020465 (-4.70%)
markdown-bench: 1574449 -> 1517425 (-3.62%)
```
</details>
