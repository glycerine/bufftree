# bufftree: a BP-tree in Go

Helen Xu, Amanda Li, Brian Wheatman, Manoj Marneni, and Prashant Pandey.
*BP-tree: Overcoming the Point-Range Operation Tradeoff for In-Memory B-trees.*
PVLDB, 16(11): 2976–2989, 2023.
[Paper](https://itshelenxu.github.io/files/papers/bptree-vldb-23.pdf).

The [Xu et al. 2023 BP-tree](bptree-vldb-23.pdf) claims to solve the
read-versus-write performance trade off that in memory B-trees
have always had to contend with. Here with implement the BP-tree
in Go and benchmark it against common alternatives.

From their abstract:

> [B-trees have an] inherent tradeoff between point and
> range operations since the optimal node size for point operations
> is much smaller than the optimal node size for range operations.
> Existing implementations use a relatively small node size to achieve
> fast point operations at the cost of range operation throughput.
> We present the BP-tree, a variant of the B-tree, that overcomes
> the decades-old point-range operation tradeoff in traditional B-trees.

## benchmarks up front

Benchmarks comparing our implementation (bufftree) against common alternatives:

The Put row measures insertion of a fresh key.

| Operation (showing ns/key) | BPtree   | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -------------: | ------------: | -------------: |
| Get                        |    101.4 |           16.5 |         116.7 |          199.7 |
| Put                        |    212.6 |          168.8 |         319.1 |          583.4 |
| Ordered scan               |     3.13 |  not supported |          4.17 |          16.11 |

~~~
This compares:
a) bufftree.NewBPTree(nil) defaults.
b) The built-in Go map; `m := make(map[uint64]uint64)`
c) https://github.com/tidwall/btree
d) https://github.com/glycerine/rbtree
~~~
Use `make bench` to re-run on your machine.

Compact records, log-only buffering, and run-based scans now beat tidwall on
the random fresh-Put and repeated length-limited scan workloads measured here. See the
[diagnosis and controlled comparisons](#fresh-put-diagnosis-2026-10-09) below,
including the costs of frequent exact counting and sequential insertion.


----------------------------
This package provides a BP-tree with sorted key-order iteration.

## How it works

The BP-tree uses small internal nodes and large leaves containing buffered
partitioned arrays (BPAs).

The BPA organizes a leaf into three parts: a) a small insert buffer; 
b) a sorted header of boundary keys; and c) contiguous data blocks. 

New entries accumulate in the insert buffer and move into blocks in batches.
This avoids the cost of keeping the entire leaf sorted after every insertion. 
Point lookups check the buffer and use the header to select a block.
Ordered scans sort blocks as needed and merge in buffered entries. 
Later long scans can flush the buffer to avoid repeating that merge work.
When blocks fill unevenly, the BPA redistributes entries across them,
combining inexpensive writes with efficient sequential scans.
This does mean that a full table scan will re-write your data, which
has locking and concurrency implications. See the 
[notes on concurrency section](#notes-on-concurrency) at the
end of this README.

`Tree[K,V]` iterates in key order and supports range queries, point lookups,
updates, and deletion. Keys may be any
`cmp.Ordered` type, including named types; values may be any type. The zero
value is usable. Do not copy a tree after its first use.
The Config struct can be used to tune memory use.

```go
tree := bufftree.NewBPTree[int, string](nil)
tree.Put(30, "thirty")
tree.Put(10, "ten")
tree.Put(20, "twenty")

for key, value := range tree.All() {
    fmt.Println(key, value) // 10 ten, 20 twenty, 30 thirty
    tree.Del(key)           // Safe: iteration continues past the deleted key.
}

```

## API and iteration

The tree provides these methods:

| Method | Result |
| --- | --- |
| `Get(key)` | Value, or the zero value if absent |
| `Get2(key)` | `(value, found)` |
| `Put(key, value)` | No return value; insert or replace |
| `Del(key)` | No return value; absent keys are unchanged |
| `Len()` | Exact number of live entries |
| `Clear()` | Remove all entries |
| `All()` | `iter.Seq2[K,V]` for Go range loops |
| `Iter()` | Explicit iterator; call `Next()` before `Key()`/`Value()` |

This is a breaking API change: `Put` and `Del` no longer return previous values
or membership booleans, and `Del2` has been removed, including on iterators.
Call `Get2` before mutation if you need the previous value or membership.

`Len()` remains exact. Its first call after writes reconciles buffered
membership in changed leaves; subsequent calls without intervening writes are
constant-time. Reconciliation allocates no memory and does not move records or
disturb live iterator positions. It does update metadata, so it requires the
same external synchronization as writes. Counting after every insertion loses
the benefit of deferring membership checks; batch your counts when possible.

`Tree` also provides:

| Method | Behavior |
| --- | --- |
| `IterFrom(start)` | Iterate beginning at the first key `>= start` |
| `iterator.Seek(start)` | Position at the first key `>= start`; read it immediately |
| `Range(start, end, visit)` | Visit keys in `[start,end)` in key order |
| `Scan(start, length, visit)` | Visit at most `length` keys `>= start` in key order |
| `MapRange(start, end, visit)` | Visit `[start,end)` in unspecified order without sorting blocks |

Visitors have signature `func(K,V) bool`; returning false stops traversal.
Empty or reversed ranges and nonpositive scan lengths visit no entries.
`Valid()` reports whether an explicit iterator has a current entry.
`iterator.Del()` removes its current key; the following `Next()` advances.
Deleting through the container works as well.

Tree iteration is live. After a mutation, an iterator seeks strictly beyond its
last returned key, preserving progress across flushes, splits, merges, and root
collapse. New keys ahead of that position may be visited; keys at or behind it
are not revisited. Exhausted iterators stay exhausted until `Seek` resets them.
`Clear` ends traversal on the next advance unless new keys ahead of the current
position have since been inserted.

`MapRange` permits deleting the current key. Its visitors must otherwise leave
the tree unchanged; use `Range` for general live traversal. It copies matching
entries one leaf at a time so deletion-induced merges cannot invalidate the
visitor's progress. A leaf-sized buffer is prepared during the first insertion
and reused, so ordinary range queries allocate no heap memory. Recursive
`MapRange` calls borrow separate buffers; deeper nesting can allocate another
buffer on its first use. Buffers are cleared when returned so they do not retain
values from deleted entries.

Keys use their natural order, with floating-point NaNs comparing equal to one
another and sorting **after** all other keys in ordered tree traversal. Signed
zeros compare equal.
A stored nil value is distinguished from an absent key by `Get2`'s `found`
result. A half-open range ending at NaN excludes NaN; `IterFrom(NaN)` and
`Scan(NaN, ...)` start at the NaN entry.

Containers and iterators require external synchronization across goroutines.
Ordered traversal can write to leaves by sorting their log and blocks or
flushing buffered entries, so it
also needs exclusive synchronization against other operations. The paper's
per-node concurrent locking scheme is not implemented here.

## Layout and configuration

Each BPA allocates one contiguous record array containing an insertion log,
sorted header records, and fixed-size blocks. A separate bitmap tracks
tombstones in the log and header; block deletions compact their block in place.
A uint64 key/value record is 16 bytes, down from 24 bytes when it contained a
boolean and alignment padding. The default log and header need two 8-byte
bitmap words, so their flags do not enlarge every record.

The reserved first slot of each block mirrors its header record. After sorting,
the header and block form one contiguous scan run; this uses existing storage,
not a second scan array. Header overwrites and deletion synchronize the mirror,
including clearing deleted pointer values.

`Put` searches only the log before buffering, whether the key is new or already
in a block. Log entries shadow older records. A full log flushes records into the corresponding
blocks. Inserts append within blocks, and overflowing blocks trigger a sorted
merge and global redistribution. Ordered scans merge the log with the header
and block stream, sorting only encountered blocks and caching their sortedness.
Unordered maps filter the block stream against the log without sorting blocks.
Ordered scans process contiguous runs directly when no log entry can shadow
them, with a separate fast path for an empty log. `Scan`, `Range`, and `All`
share this traversal; explicit iterators keep their own cursors. After a leaf
has been visited, a later sufficiently long scan may flush its log. Narrow
ranges and first visits retain the merge path. Scan-driven rearrangement advances
the tree version, so existing live cursors reseek safely. Every callback still
checks for mutation before reading another borrowed record.

Point lookups descend through the internal separators, check the leaf's
insertion log, and use its sorted header to select a block. Updates and deletion
use the same BP-tree path. Deletion still locates the entry and resolves the
leaf's occupancy for rebalancing. Queries allocate no memory.

Internal nodes contain sorted separators. Leaf and internal splits propagate
upward. Deletion redistributes or merges underfull siblings and collapses a
single-child root. Deleted header records can remain as partition markers
until redistribution; their values are cleared.

```go
cfg := bufftree.Config{
    Fanout:    256, // Maximum internal children.
    LogSize:   42,
    NumBlocks: 32,
    BlockSize: 34,
}
tree := bufftree.NewBPTree[int, string](&cfg)
```

`NewBPTree[K,V](nil)` uses the default configuration.
Passing `&cfg` copies and stores the configuration before the constructor
returns. The constructor does not modify your config; changing it later does not
affect the constructed container.

Those are the defaults; zero numeric fields select defaults. Fanout must be at least 3;
other fields must be at least 2. Invalid configurations panic. Sizes are measured
in entries, so byte sizes depend on the generic types and Go struct padding.
The default leaf allocates 1,162 record slots and holds up to 1,088 live keys,
reserving one slot per block for its mirrored header and leaving at least one
unused log slot after an operation.

For uint64 keys and values, the default record array plus bitmap occupies
18,608 bytes, versus 26,112 bytes for the original 32-slot-block record array alone; these
figures exclude leaf metadata and shared tree buffers.

Adaptations to the paper include an exact (lazily reconciled) `Len` and a
conservative leaf capacity that can be fully redistributed into header/blocks.
Buffered duplicate keys temporarily overestimate leaf occupancy; flushing
resolves it before a split, so overwrites cannot spuriously split a full leaf.
Deletion rebalancing and live iteration extend the paper's brief discussion of
tombstones.

## Tests and benchmarks

Run the main package's tests from this directory:

```sh
go test -v
```

Tests cover BPA invariants, randomized operations against map/order models,
balanced tree structure, 65,536-entry bulk splitting and deletion, iterator
mutation and later appends, NaNs sorting last, zero values, and public API
examples. Allocation tests cover reads, ordered and unordered
queries, iteration, and named string/float keys. Benchmark operation traces
are also checked against an independent sorted-leaf B+ tree. The `reference/`
material is not part of the top-level package.

The separate benchmark module has its own adapter, table-formatting, and memory tests:

```sh
go -C bench test -v
```

Use these nonrecursive commands; `reference/` contains unbuildable material.

Run the standalone 100,000-entry memory comparison with:

```sh
make memory
# Or directly:
go -C bench test -v -run '^TestMemoryUsage100K$' -count=1
```

| Container         | Heap bytes | MiB  | B/key |
| ----------------- | ---------: | ---: | ----: |
| glycerine/rbtree  |    6400080 | 6.10 | 64.00 |
| bufftree.BPTree   |    2560376 | 2.44 | 25.60 |
| tidwall/btree.Map |    2513408 | 2.40 | 25.13 |
| builtin Go map    |    2364576 | 2.26 | 23.65 |

It reports retained heap bytes, MiB, and bytes per key for BPTree,
the built-in Go map, tidwall's generic Map, and rbtree. It shares the timing
benchmarks' constructors, uint64 key/value data, and insertion order; the Go map uses the same capacity hint. Each container is
measured in a fresh test process with `GOMAXPROCS=1`, with forced GC before and
after loading, and kept alive through the final heap measurement. Results subtract
the initial `runtime.MemStats.HeapAlloc` baseline and exclude discarded
temporary allocations; they measure live Go heap, rather than process RSS or
heap reserved by the runtime. `make memory` and `make bench` run separately,
and the memory test never invokes `testing.Benchmark`.
An example report is saved in
[memory-tuned.txt](benchmark-results/2026-10-09-scan/memory-tuned.txt).
The compact layout reduced BPTree retained memory from 36.10 to 24.74 B/key
before retuning. The fresh-put-focused defaults use 25.60 B/key on this workload,
including metadata and tree buffers: 3.5% above the previous geometry, but 29%
below the original layout. The longer log crosses a Go allocation-size boundary;
record width is still 16 bytes.

The benchmark families adapt the paper's experiments to Go:

| Benchmark | Experiment |
| --- | --- |
| `BenchmarkLeafCopies` | Section 4: 128 leaf copies, sizes 4–4,096; half-to-full insertions, full-leaf misses, and scans |
| `BenchmarkTree` | Section 6.1: node-size sweeps; random/sequential insertions, updates, hits/misses, scans/maps up to 100,000 entries |
| `BenchmarkYCSB` | Section 6.2: uniform and Zipfian A/B/C/E/X/Y workloads |
| `BenchmarkReferencePoints` | Identical 16-byte string keys for Tree and Go map |
| `BenchmarkComparePoints` (in `bench/`) | Identical uint64 lookup/update/fresh-insert workloads for Tree, Go map, tidwall/btree, and rbtree |
| `BenchmarkCompareIteration` (in `bench/`) | Ordered scans and full traversal for the same containers; Go map supports full traversal only |
| `BenchmarkFreshPut` (in `bench/`) | The same fresh-insert workload, with CPU-profile labels separating insertion from untimed fixture loading |
| `BenchmarkFreshPutConfig` (in `bench/`) | Original fresh-insert sweep over internal fanout, log size, header size, and block size |
| `BenchmarkTuneGeometry`, `BenchmarkTuneRefine`, `BenchmarkTuneNeighborhood`, `BenchmarkTuneFinalists`, `BenchmarkTuneSecondLeg`, `BenchmarkTuneConfirm` (in `bench/`) | Successive configuration sweeps, with fresh adjacent/independent keys and ordered scans measured separately |
| `BenchmarkFreshPutDistribution` (in `bench/`) | Independent random fresh keys, and ascending/descending growth from empty |
| `BenchmarkFreshPutWithLen` (in `bench/`) | Fresh inserts including exact Len after every write, every 32 writes, or a complete batch |
| `BenchmarkScanFirst` (in `bench/`) | First scan after loading, including lazy sorting and scan preparation |
| `BenchmarkScanMixed` (in `bench/`) | 32 existing-key writes followed by a random scan; both phases timed |
| `BenchmarkOrderedAll` (in `bench/`) | Native full ordered traversal, without a length-limit adapter for tidwall |

YCSB A uses 50% reads/50% updates, B uses 95% reads/5% updates, and C is all
reads. E uses 95% scans/5% new insertions with maximum scan length 100. X is all
ordered scans, and Y is all unordered maps, both with maximum length 10,000.
Lengths are uniform in `[0,maxLength]`. Y's endpoints are obtained through an
untimed scan of the initial data. Zipfian traces use exponent 0.99, with ranks
scrambled into key space to avoid concentrating hot keys in one range.

Benchmarks use uint64 keys/values, deterministic traces, untimed loading, and a
default load of 65,536 entries. Growing workloads reload outside the timer after
adding another load's worth of keys. Traces repeat every 8,192 operations.
Results include Go's time/allocation metrics and three traversal metrics:

* `iter_ns/key`: total timed nanoseconds divided by actual keys visited.
* `keys/op`: actual average keys visited per operation.
* `entries/s`: actual keys visited per timed second.

Using actual key counts accounts for random scan lengths and the end of the
tree. For mixed workload E, `iter_ns/key` includes its insert time in the total.
The number in a scan benchmark's name is the maximum length, not its fixed
length; use `keys/op` when comparing different maximum lengths or load sizes.

The benchmark-only B+ tree provides a sorted-leaf baseline with the same
64-child internal fanout. Its correctness is tested independently.

Run these timing commands from the repository root. `make bench` and
`go -C bench` use the benchmark module; the paper benchmarks run in the main
package:

```sh
make bench
make bench BENCH_TIME=250ms BENCH_COUNT=5
go test -v -run '^$' -bench '^BenchmarkYCSB/BP/h32-b32/' -benchmem
go test -v -run '^$' -bench '^BenchmarkTree/' -benchmem
go test -v -run '^$' -bench '^BenchmarkLeafCopies/' -benchmem
go test -v -run '^$' -bench '^BenchmarkReferencePoints/' -benchmem
go -C bench test -v -run '^$' -bench '^BenchmarkCompare(Points|Iteration)$' -benchmem -benchtime=250ms -count=5
BUFFTREE_BENCH_N=1000000 go test -v -run '^$' -bench '^BenchmarkYCSB/' -benchmem -count=5
```

`make bench` runs `bench/TestReadmeBenchmarkTable`, using three 100ms samples per
benchmark by default and reporting their median. It measures only the 22
distinct cases needed for the two README tables and reuses shared results.
The longer Make command above uses the five 250ms samples used in the saved
measurements below. `BUFFTREE_BENCH_N` also controls the Make target's load size.
Normal `go -C bench test -v` skips the measurement test; its formatting and
timing regression tests still run. The generated tables are printed for copying
into the README; the test does not overwrite documentation.

Profile fresh insertion independently with:

```sh
go -C bench test -v -run '^$' -bench '^BenchmarkFreshPut$/^Tree$' \
    -benchmem -benchtime=3s -cpuprofile=/tmp/bufftree-fresh.cpu \
    -memprofile=/tmp/bufftree-fresh.mem -o=/tmp/bufftree-fresh.test
go tool pprof -top -relative_percentages '-tagfocus=container=^Tree$' \
    /tmp/bufftree-fresh.test /tmp/bufftree-fresh.cpu
go tool pprof -top -alloc_space -ignore=loadComparisonPoints \
    /tmp/bufftree-fresh.test /tmp/bufftree-fresh.mem
```

CPU profiles include fixture loading even though the benchmark timer excludes
it. The container label selects the insertion phase. Heap profiles use stack
filtering to exclude fixture loading.

[`bench/go.mod`](bench/go.mod) pins the published competitor versions and
replaces only `github.com/glycerine/bufftree` with `..`, so comparisons measure
the local library code. Maintain its dependencies with `go -C bench mod tidy`.

These are single-goroutine experiments, including the leaf copies. They do not
reproduce the paper's 100M-entry, 48-hyperthread setup or compare against Masstree
and OpenBw-tree. Comparisons use the same key/value data and workloads on this
machine.

## Fresh-Put diagnosis (2026-10-09)

The implementation contains the paper's essential algorithms: a contiguous
log/header/block BPA, buffered flushes, global redistribution on block overflow,
lazy block sorting, and large leaves under smaller internal nodes. The slowdown
was primarily implementation overhead, with additional differences in API and
experimental setup; changing leaf sizes alone did not fix it.

Before optimization, the labelled fresh-insert CPU profile spent about 53% in
flushing. A fresh `Put` searched the log/header/block for the old value, searched
the log again to append, then repeated block searches during flush preflight
and application. Redistribution also drove the general cursor once per record,
and small-array sorting used comparator callbacks and swaps.

The first optimization pass eliminated repeated searches, specialized small
sorts, copied sorted runs during redistribution, and shared cleared scratch
space for splits. That reduced unpinned fresh Put from 622.7 to 321.2 ns while
preserving the old-value API. Its measurements are retained in the
[earlier report](benchmark-results/2026-10-09/summary.txt).

This second pass changes that API and the record layout. Put checks only the
log, buffering updates as well as new keys. Duplicate detection moves into a
single flush pass through the sorted log/header, searching each destination
block once per buffered record. An overflow rebuild deduplicates any records
already copied. Leaf occupancy is an upper bound until reconciliation; a
per-tree list tracks changed leaves for exact `Len` without scanning the whole
tree. Already-counted log prefixes are not counted again. Retired leaves leave
that list immediately. Tombstones move with records during sorting, including
custom logs spanning multiple bitmap words.

The hot log scan has no per-element bounds checks, tombstone-free sorting checks
bitmap words instead of individual flags, and internal-node fields used during
descent are grouped together. No unsafe code, assembly, or architecture-specific
instructions are needed. The measurements in this diagnosis section used the
then-default configuration (fanout 64, log/header/block sizes 32). The subsequent
scan optimization and fresh-parameter sweep described below change those defaults.

Five fixed-size samples of the fresh-key trace, each timing 1,048,576 inserts
in complete 65,536-key batches, gave these medians (ns/Put). Both binaries were
pinned to CPU 6 with `-cpu=1`; "before" is the already-optimized first pass:

| Container | Before bitmap/API change | After |
| --- | ---: | ---: |
| BP-tree | 177.4 | 119.6 |
| tidwall | 151.2 | 149.1 |

That is 33% less BP-tree insertion time and 20% less time than tidwall on this
trace. Allocation traffic fell from 41 to 28 B/Put (allocation bytes, not
retained memory). Unpinned medians were 230.5 ns for BP-tree and 308.1 ns for
tidwall. CPU placement matters considerably on this Threadripper; do not
compare pinned timings directly with unpinned ones or with older README runs.

Additional pinned comparisons (three-sample medians, ns/Put):

| Workload | BP-tree | tidwall |
| --- | ---: | ---: |
| 1,048,576 loaded keys, growing to 2,097,152 | 205.4 | 349.6 |
| Independent random fresh keys, 65,536 loaded | 140.8 | 152.7 |
| Ascending growth from empty | 94.44 | 45.24 |
| Descending growth from empty | 137.9 | 54.51 |
| Fresh inserts plus exact Len every write | 183.4 | 154.4 |
| Fresh inserts plus exact Len every 32 writes | 184.8 | 154.4 |
| Fresh inserts plus exact Len every 65,536 writes | 124.0 | 153.0 |

The count benchmarks include final reconciliation inside the timer. Frequent
counting gives back the random-insert advantage, because unflushed keys still
need membership checks. Tidwall also remains substantially faster for sequential
growth. This is a workload-specific improvement, not a universal winner.

`perf stat` on equal pinned workloads measured 21% fewer instructions, 32%
fewer cycles, 27% fewer branch misses, and 53% fewer L1 data-load misses versus
the first pass. The L1 miss rate fell from 9.94% to 6.40%. These counters include
fixture loading and benchmark resets and were multiplexed; they are not
insertion-only counters or proof that all gains come from L1 behavior.

Pinned regression measurements also improved Tree hit/miss/update time from
100.7/98.15/105.9 to 93.26/91.26/91.72 ns, and ordered scans from 4.597 to
4.139 ns/key.

The paper's performance claims also need their original context:

* Section 3's insert checks only the log before buffering. Section 5 permits
  a leaf count that temporarily includes duplicates. The new write path follows
  this approach; the old previous-value contract forced an immediate base lookup.
* The paper uses 16-byte uint64 key/value records. Our separate tombstone bitmap
  now permits the same record width, instead of 24 bytes including flag/padding.
* Section 6 compares concurrent C++ implementations, including TLX-based B+
  trees, using 100M loaded keys and 48 hyperthreads. Its default BP-tree beats
  its best insert-oriented B-tree by about 1.04x on random insertion; it does
  not promise to beat every optimized single-threaded Go B-tree.

See [summary.txt](benchmark-results/2026-10-09-bitmap/summary.txt) for reproduction
commands, validation, profile/counter files, and benchmark qualifications.

## Ordered scans and retuning (2026-10-09)

Ordered traversal now visits contiguous header/block runs instead of advancing a
merge cursor for every key. Header mirrors occupy the blocks' already-reserved
slots. Repeated long scans can settle buffered writes once; first visits and
narrow ranges retain lazy merging. `All` uses the same run-based path. Live
mutation checks remain, including safe reseeking after nested scan preparation.

With the final defaults, five pinned samples measured ordered scans at 3.003 and
3.089 ns/key for maximum lengths 10,000 and 100,000, versus tidwall's 3.802 and
3.785. The old BP-tree measured 4.216 and 4.179 on the same traces. Fresh puts
improved from 121.5 to 108.4 ns, versus tidwall's 152.3 in the final run. These
CPU-6, single-thread timings are separate from the unpinned README tables.

First scans still pay for lazy sorting, and the write-focused defaults make
them slower: a first full scan measured 5.133 ns/key versus tidwall's 3.747.
Native full traversal improved from 8.099 to 3.885 ns/key, but tidwall's native
`Map.Scan` remains faster at 2.376. It has no length-limit adapter. These are
workload-specific gains, not a claim that every ordered traversal wins.

The fresh-put sweep covered 237 distinct configurations in successive rounds.
After selecting a promising geometry, separator storage was changed to grow
geometrically and clear only removed keys, then the neighborhood was swept again.
The final default is **fanout 256, log 42, 32 blocks, block size 34**.
Selection prioritizes fresh puts: equal-weight geometric mean of median insertion
times for adjacent and independent fresh keys at 65,536 and 1,048,576 loaded keys.
Scans do not contribute to that score. The final confirmation used five samples
of 1,048,576 timed inserts per workload.

The winning score was 153.97 ns versus 154.66 for the intermediate 32-entry-log
configuration, a further 0.4% reduction. The nearest candidate scored 154.07:
these tiny differences are not statistically established wins, but the best
observed configuration is applied rather than discarded. Results are specific
to this machine, key/value sizes, and these traces—not a universal optimum.

See the [scan and tuning report](benchmark-results/2026-10-09-scan/summary.txt)
for controlled comparisons, caveats, sweep results, and reproduction commands.

## Measured performance

Measured on 2026-10-09 on an AMD Ryzen Threadripper 3960X, Linux/amd64, Go 1.26.4, with
65,536 uint64 keys/values and the default leaf layout. These are medians of
five 250ms runs using identical data and uniform point-operation traces.
The `bufftree` column uses the current implementation with its default
configuration. Baselines are
published `github.com/tidwall/btree v1.8.1` and `github.com/glycerine/rbtree v0.2.2`;
neither competitor has a local module replacement. Reads, existing-key updates,
and traversals report `0 B/op` and `0 allocs/op`; fresh puts include allocation
and growth costs.

| Operation (showing ns/key)    |   BPtree | builtin Go map | tidwall/btree | red-black tree |
| ----------------------------- | -------: | -------------: | ------------: | -------------: |
| Tree `Get`, hit               |    101.4 |           16.5 |         116.7 |          199.7 |
| Tree `Get`, miss              |     98.4 |           16.1 |         118.4 |          221.4 |
| Tree `Put`, existing key      |     97.2 |           27.1 |         126.9 |          207.0 |
| Tree `Put`, fresh key         |    212.6 |          168.8 |         319.1 |          583.4 |
| Ordered scan, maximum 10,000  |     3.11 |  not supported |          4.13 |          15.80 |
| Ordered scan, maximum 100,000 |     3.13 |  not supported |          4.17 |          16.11 |

Go map updates are plain assignments, matching the new void `Put` contract.
Point benchmarks use the same interface dispatch
for all containers. The tidwall baseline uses its generic
[`Map`](https://github.com/tidwall/btree/blob/v1.8.1/map.go), default degree 32,
with no path hints or copies. The rbtree adapter reuses a pointer-shaped query
object to avoid boxing allocations; updates use `InsertGetIt` and change an
existing item's value with one search. Competitor return values are discarded.

Fresh Put rows insert unique odd keys into the initial even-key dataset, using
the same scrambled keys for every container. Each batch grows from 65,536 to
131,072 entries; loading a new initial dataset between batches is outside the
timer. This includes allocations, leaf splits, and BPA redistribution during
insertion. The simplified table's Put row uses these fresh-insertion measurements.

Fresh-Put profiling identified repeated searches, sorting/merge overhead, and
temporary allocations. Leaves now reuse one buffer owned by their tree for
redistribution and splits, reducing temporary allocation bytes.
The buffer adds one leaf's capacity per tree
(17 KiB with the default uint64 layout), and its entries are cleared after use
so it does not retain old keys or values.

Native Go maps do not provide ordered scans. Traversal figures
report `iter_ns/key` using actual visited keys; scan lengths vary up to the
named maximum and stop at the tree's end.

Separate reference-style benchmarks cover 16-byte string keys and new
insertions. Fresh insertions allocate storage and redistribute BPA records;
reads and updates to existing keys have different costs. CPU profiling guided
log-only buffering, bulk block traversal, cheaper log-shadow checks, and the
shared redistribution buffer.

Current benchmark runs and validation logs are saved in
[benchmark-results/2026-10-09-scan](benchmark-results/2026-10-09-scan).
The tables use measured values from
[readme-tuned.txt](benchmark-results/2026-10-09-scan/readme-tuned.txt).
The 2026-10-01 reports describe earlier implementations.

## notes on concurrency

Be aware: we do no locking at present. The paper discusses approaches,
but they have pretty severe sounding trade offs. This is because
**an ordered scan can rearrange stored records in memory**, 
while preserving their logical key/value contents. 

The paper's locking approach tracks whether each block and the 
log are sorted, so subsequent scans reuse that ordering until writes disturb it. 
[Section 3](https://itshelenxu.github.io/files/papers/bptree-vldb-23.pdf#page=6).

Section 5 explicitly addresses the locking implications. A scan first acquires a leaf’s read lock and checks sortedness. If everything it needs is sorted, it proceeds under that shared lock; otherwise, it releases the read lock and acquires the leaf’s write lock to sort. This temporarily blocks other readers and writers accessing that leaf. [Section 5](https://itshelenxu.github.io/files/papers/bptree-vldb-23.pdf#page=8).

Here is the scary part: it is a classic hazard to try and upgrade from a read lock
to a write lock! Other readers or even other writers may have priority, and so the writer may have to
yield to them! The opportunities for deadlock or livelock are multidinous, and
this would need very careful conconcurrency modeling to get right and work well.
Thus it is out of scope for now.

In the paper, traversal uses hand-over-hand locking: acquire the next node’s lock before releasing the previous one, with locks acquired top-down and then left-to-right to prevent deadlock. Thus synchronization follows the traversal through individual nodes rather than holding the entire tree exclusively. [Section 2.1](https://itshelenxu.github.io/files/papers/bptree-vldb-23.pdf#page=4). [jea note: I'm not convinced this would not stall or confuse the first writer badly... what if there is rebalancing and the node is no longer even the right node...!]

Our Go implementation currently requires external synchronization; shared ordered scans need an exclusive lock.

------------------
Copyright (C) 2026, Jason E. Aten, Ph.D.

License: MIT. See the LICENSE file.
