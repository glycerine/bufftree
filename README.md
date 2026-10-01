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

| Operation (showing ns/key) | bufftree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -------------: | ------------: | -------------: |
| Get                        |     20.8 |           16.7 |         114.3 |          201.9 |
| Put                        |    100.8 |           28.9 |         126.8 |          204.4 |
| Ordered scan               |     4.49 |  not supported |          4.12 |          15.87 |
| Dict traversal             |     2.60 |          10.02 |          2.54 |          15.42 |

~~~
This compares:
a) buftree.NewBPTree(nil) defaults.
b) The built-in Go map; `m := make(map[uint64]uint64)`
c) https://github.com/tidwall/btree
d) https://github.com/glycerine/rbtree
~~~
Use `make bench` to re-run on your machine.

## Conclusion: the classic in memory b-tree tidwall.Btree remains the king of speed and space.


----------------------------
This package supports either insertion-ordered iteration using bufftree.Dict, 
or sorted key-order iteration using bufftree.BPTree.

## How it works

The BP-tree uses small internal nodes and large leaves containing buffered
partitioned arrays (BPAs).

The BPA organizes a leaf into three parts: a) a small insert buffer; 
b) a sorted header of boundary keys; and c) contiguous data blocks. 

New entries accumulate in the insert buffer and move into blocks in batches.
This avoids the cost of keeping the entire leaf sorted after every insertion. 
Point lookups check the buffer and use the header to select a block.
Ordered scans sort blocks as needed and merge in buffered entries. 
When blocks fill unevenly, the BPA redistributes entries across them,
combining inexpensive writes with efficient sequential scans.
This does mean that a full table scan will re-write your data, which
has locking and concurrency implications. See the 
[notes on concurrency section](#notes-on-concurrency) at the
end of this README.

`Tree[K,V]` iterates in key order and supports range queries. `Dict[K,V]`
iterates in insertion order. Both maintain a BP-tree and, by default, an
additional hash index for fast point lookups. Keys may be any
`cmp.Ordered` type, including named types; values may be any type. Both zero
values are usable. Do not copy either container after its first use.
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

dict := bufftree.NewDict[string, int]()
dict.Put("charlie", 3)
dict.Put("alice", 1)
dict.Put("bob", 2)
dict.Put("alice", 10) // Updating preserves the original position.

it := dict.Iter()
for it.Next() {
    fmt.Println(it.Key(), it.Value()) // charlie 3, alice 10, bob 2
    it.Del()
}
```

## API and iteration

Both containers provide these methods:

| Method | Result |
| --- | --- |
| `Get(key)` | Value, or the zero value if absent |
| `Get2(key)` | `(value, found)` |
| `Put(key, value)` | `(oldValue, replaced)` |
| `Del2(key)` | `(oldValue, found)` |
| `Del(key)` | Whether the key was removed |
| `Len()` | Exact number of live entries |
| `Clear()` | Remove all entries |
| `All()` | `iter.Seq2[K,V]` for Go range loops |
| `Iter()` | Explicit iterator; call `Next()` before `Key()`/`Value()` |

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
`iterator.Del2()` also returns `(oldValue, found)`.
Deleting through the container works as well.

Tree iteration is live. After a mutation, an iterator seeks strictly beyond its
last returned key, preserving progress across flushes, splits, merges, and root
collapse. New keys ahead of that position may be visited; keys at or behind it
are not revisited. Exhausted iterators stay exhausted until `Seek` resets them.
`Clear` ends traversal on the next advance unless new keys ahead of the current
position have since been inserted.

Dictionary iteration is live in insertion order. It supports deletion of current
and upcoming entries and observes updates to upcoming values. New entries
appended before the iterator reaches the end are visited, including when the
current tail was deleted before appending. Deleting and reinserting a key
appends it at the end as a new entry. Once `Next` returns false, that iterator
stays exhausted. `Clear` ends existing dictionary iterators. A loop that keeps
appending new entries can keep extending its own traversal.

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
zeros compare equal. Dictionary traversal always follows insertion order.
A stored nil value is distinguished from an absent key by `Get2`'s `found`
result. A half-open range ending at NaN excludes NaN; `IterFrom(NaN)` and
`Scan(NaN, ...)` start at the NaN entry.

Containers and iterators require external synchronization across goroutines.
Ordered traversal can write to leaves by sorting their log and blocks, so it
also needs exclusive synchronization against other operations. The paper's
per-node concurrent locking scheme is not implemented here.

## Layout and configuration

Each BPA allocates one contiguous record array containing an insertion log,
sorted header records, and fixed-size blocks. Log entries shadow older records;
deletions use tombstones. Existing values are overwritten in place; new keys
and tombstones go into the log. A full log flushes records into the corresponding
blocks. Inserts append within blocks, and overflowing blocks trigger a sorted
merge and global redistribution. Ordered scans merge the log with the header
and block stream, sorting only encountered blocks and caching their sortedness.
Unordered maps filter the block stream against the log without sorting blocks.
Ordered scans process contiguous block segments directly when no log entry can
shadow them, while checking for callback mutations to preserve live traversal.

The hash index uses linear probing, at most 50% occupancy, and backward-shift
deletion. By default it retains full 64-bit hashes and checks them before key
equality. It uses the standard library's `maphash` and handles
NaNs as a single canonical key. Cached leaf pointers avoid a tree descent for
updates and deletions; splits and merges repair those pointers. Hashing is
maintained during writes, so queries never build an index or allocate lazily.
The insertion-order chain uses an end marker that becomes the next appended
entry, preserving iterator progress even when its previous tail was deleted.

Internal nodes contain sorted separators. Leaf and internal splits propagate
upward. Deletion redistributes or merges underfull siblings and collapses a
single-child root. Deleted header records can remain as partition markers
until redistribution; their values are cleared.

```go
cfg := bufftree.Config{
    Fanout:    64, // Maximum internal children.
    LogSize:   32,
    NumBlocks: 32,
    BlockSize: 32,
}
tree := bufftree.NewBPTree[int, string](&cfg)
dict := bufftree.NewDictWithConfig[int, string](cfg)
```

`NewBPTree[K,V](nil)` uses the default configuration. Passing `&cfg` copies
the configuration into the tree; constructing a tree never modifies your
config, and changing it later does not affect that tree.

Those are the defaults; zero numeric fields select defaults. Fanout must be at least 3;
other fields must be at least 2. Invalid configurations panic. Sizes are measured
in entries, so byte sizes depend on the generic types and Go struct padding.
The default leaf allocates 1,088 record slots and holds up to 1,024 live keys,
leaving at least one spare slot in each block and in the log after an operation.

Set `Config.DisablePointIndex` to true to use only the BP-tree for point
operations and save the hash index's extra storage. Ordered operations use the
same BP-tree in both modes. The hash index stores a second copy of values;
dictionary entries are referenced by pointer.

`Config.HashNoCache` defaults to false: indexed entries retain their full
hashes, also reused during growth and backward-shift deletion. Each lookup
still hashes its requested key once. Setting `HashNoCache: true` uses one-byte
fingerprints instead, saving seven bytes per allocated hash-table slot; growth
and deletion then recompute hashes of moved entries. Both modes compare keys
to confirm matches and allocate no memory on queries. `HashNoCache` has no
effect when `DisablePointIndex` is true.

Adaptations to the paper include an exact `Len` and returning previous values,
the additional point index, in-place overwrites, and a conservative leaf capacity
that can be fully redistributed into header/blocks. Deletion rebalancing and
live iteration extend the paper's brief discussion of tombstones.

## Tests and benchmarks

Run the main package's tests from this directory:

```sh
go test -v
```

Tests cover BPA invariants, randomized operations against map/order models,
balanced tree structure, 65,536-entry bulk splitting and deletion, iterator
mutation and later appends, NaNs sorting last, zero values, hash collision chains
and growth/deletion with and without cached hashes,
and public API examples. Allocation tests cover reads, ordered and unordered
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

It reports retained heap bytes, MiB, and bytes per key for Dict and BPTree with
and without their hash index, the built-in Go map, tidwall's generic Map, and
rbtree. It shares the timing benchmarks' constructors, uint64 key/value data,
and insertion order; the Go map uses the same capacity hint. Each container is
measured in a fresh test process with `GOMAXPROCS=1`, with forced GC before and
after loading, and kept alive through the final heap measurement. Results subtract
the initial `runtime.MemStats.HeapAlloc` baseline and exclude discarded
temporary allocations; they measure live Go heap, rather than process RSS or
heap reserved by the runtime. The test also prints the added cost of each
hash index, using the default cached hashes. `make memory` and `make bench`
run separately, and the memory test never invokes `testing.Benchmark`.
An example report is saved in
[memory-shared-buffer.txt](benchmark-results/2026-10-01/memory-shared-buffer.txt).

The benchmark families adapt the paper's experiments to Go:

| Benchmark | Experiment |
| --- | --- |
| `BenchmarkLeafCopies` | Section 4: 128 leaf copies, sizes 4–4,096; half-to-full insertions, full-leaf misses, and scans |
| `BenchmarkTree` | Section 6.1: node-size sweeps; random/sequential insertions, updates, hits/misses, scans/maps up to 100,000 entries |
| `BenchmarkYCSB` | Section 6.2: uniform and Zipfian A/B/C/E/X/Y workloads |
| `BenchmarkDict` | Dictionary reads, updates, insertion-order traversal, and deleting current during traversal |
| `BenchmarkReferencePoints` | Identical 16-byte string keys for Tree, Tree without hashing, Dict, a port of the reference's stable-slot hash layout, and Go map |
| `BenchmarkComparePoints` (in `bench/`) | Identical uint64 lookup/update/fresh-insert workloads for Tree and Dict in all index/cache modes, Go map, tidwall/btree, and rbtree |
| `BenchmarkCompareIteration` (in `bench/`) | Ordered scans and full traversal for the same containers; Go map supports full traversal only |
| `BenchmarkFreshPut` (in `bench/`) | The same fresh-insert workload, with CPU-profile labels separating insertion from untimed fixture loading |

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
go test -v -run '^$' -bench '^BenchmarkDict/' -benchmem
go test -v -run '^$' -bench '^BenchmarkReferencePoints/' -benchmem
go -C bench test -v -run '^$' -bench '^BenchmarkCompare(Points|Iteration)$' -benchmem -benchtime=250ms -count=5
BUFFTREE_BENCH_N=1000000 go test -v -run '^$' -bench '^BenchmarkYCSB/' -benchmem -count=5
```

`make bench` runs `bench/TestReadmeBenchmarkTable`, using three 100ms samples per
benchmark by default and reporting their median. It measures only the 39
distinct cases needed for the two README tables and reuses shared results.
The longer Make command above uses the five 250ms samples used in the saved
measurements below. `BUFFTREE_BENCH_N` also controls the Make target's load size.
Normal `go -C bench test -v` skips the measurement test; its formatting and
timing regression tests still run. The generated tables are printed for copying
into the README; the test does not overwrite documentation.

Profile fresh insertion independently with:

```sh
go -C bench test -v -run '^$' -bench '^BenchmarkFreshPut$/^(TreeHash|DictHash)$' \
    -benchmem -benchtime=3s -cpuprofile=/tmp/bufftree-fresh.cpu \
    -memprofile=/tmp/bufftree-fresh.mem -o=/tmp/bufftree-fresh.test
go tool pprof -top -relative_percentages -tagfocus=container=TreeHash \
    /tmp/bufftree-fresh.test /tmp/bufftree-fresh.cpu
go tool pprof -top -alloc_space -ignore=loadComparisonPoints \
    /tmp/bufftree-fresh.test /tmp/bufftree-fresh.mem
```

CPU profiles include fixture loading even though the benchmark timer excludes
it. The container label selects the insertion phase; use `container=DictHash`
for Dict. Heap profiles use stack filtering to exclude fixture loading.

[`bench/go.mod`](bench/go.mod) pins the published competitor versions and
replaces only `github.com/glycerine/bufftree` with `..`, so comparisons measure
the local library code. Maintain its dependencies with `go -C bench mod tidy`.

These are single-goroutine experiments, including the leaf copies. They do not
reproduce the paper's 100M-entry, 48-hyperthread setup or compare against Masstree
and OpenBw-tree. The reference-style hash port uses `maphash` instead of xxhash,
uint64 values, and no internal locking, so its numbers compare the layouts on
the same dataset rather than reproducing every detail of the reference comments.

## Measured performance

Measured on an AMD Ryzen Threadripper 3960X, Linux/amd64, Go 1.26.4, with
65,536 uint64 keys/values and the default leaf layout. These are medians of
five 250ms runs using identical data and uniform point-operation traces.
The `bufftree` column uses `Config.DisablePointIndex: true`; the hash-index
column uses the default configuration, retaining full hashes
(`HashNoCache: false`). Both use the current implementation. Baselines are
published `github.com/tidwall/btree v1.8.1` and `github.com/glycerine/rbtree v0.2.2`;
neither competitor has a local module replacement. Reads, existing-key updates,
and traversals report `0 B/op` and `0 allocs/op`; fresh puts include allocation
and growth costs.

| Operation (showing ns/key)    | bufftree | bufftree(2) | builtin Go map | tidwall/btree | red-black tree |
| ----------------------------- | -------: | ----------: | -------------: | ------------: | -------------: |
| Tree `Get`, hit               |    105.6 |        20.8 |           16.7 |         114.3 |          201.9 |
| Tree `Get`, miss              |    104.2 |        27.0 |           15.9 |         116.5 |          220.1 |
| Tree `Put`, existing key      |    164.4 |       100.8 |           28.9 |         126.8 |          204.4 |
| Tree `Put`, fresh key         |    637.1 |       758.0 |          180.1 |         319.2 |          598.6 |
| Dict `Get`, hit               |    101.9 |        26.6 |           16.7 |         114.3 |          201.9 |
| Dict `Put`, existing key      |    102.6 |        27.4 |           28.9 |         126.8 |          204.4 |
| Dict `Put`, fresh key         |    938.1 |       967.5 |          180.1 |         319.2 |          598.6 |
| Ordered scan, maximum 10,000  |     4.75 |        4.54 |  not supported |          4.16 |          15.94 |
| Ordered scan, maximum 100,000 |     4.73 |        4.49 |  not supported |          4.12 |          15.87 |
| Dict traversal                |     2.34 |        2.60 |          10.02 |          2.54 |          15.42 |

buftree(2) means with hash index (faster, uses more memory). This is the default.

Go map updates include reading the old value before assignment, matching
`Put`'s return-value contract. Point benchmarks use the same interface dispatch
for all containers. The tidwall baseline uses its generic
[`Map`](https://github.com/tidwall/btree/blob/v1.8.1/map.go), default degree 32,
with no path hints or copies. The rbtree adapter reuses a pointer-shaped query
object to avoid boxing allocations; updates use `InsertGetIt` and change an
existing item's value, returning its old value with one search.

Fresh Put rows insert unique odd keys into the initial even-key dataset, using
the same scrambled keys for every container. Each batch grows from 65,536 to
131,072 entries; loading a new initial dataset between batches is outside the
timer. This includes allocations, leaf splits, and hash-table growth during
insertion. The simplified table's Put row measures an existing-key update.

Fresh-Put profiling identified repeated temporary allocations during BPA
redistribution. Leaves now reuse one buffer owned by their tree, reducing
temporary allocation bytes. Matched five-run trials improved fresh Put by
about 17% for Tree and 10% for Dict with the default hash index. The buffer adds
one leaf's capacity per tree (24 KiB with the default uint64 layout), and its
entries are cleared after use so it does not retain old keys or values.
The trial results and profiling commands are saved in
[fresh-put-summary.txt](benchmark-results/2026-10-01/fresh-put-summary.txt).

The final row measures visiting all 65,536 values and summing them. Dict visits
in insertion order, Go map in unspecified order, and tidwall/btree and rbtree
in key order. Native Go maps do not provide ordered scans. Traversal figures
report `iter_ns/key` using actual visited keys; scan lengths vary up to the
named maximum and stop at the tree's end.

The hash-cache benefit depends on the workload. With `HashNoCache: true`,
these uint64 hit lookups measured 21.0 ns for Tree and 26.8 ns for Dict;
results for all cache modes are included in the saved comparison data.
Separate reference-style benchmarks cover 16-byte string keys, new insertions,
and the stable-slot hash layout. New entries maintain the BP-tree as well as
any enabled hash index; their costs and allocation behavior differ from reads
and updates to existing keys.

CPU profiling guided cached leaf pointers, direct value overwrites, bulk block
traversal, cheaper log-shadow checks, and the shared redistribution buffer.
The fresh-Put allocation profile before buffer reuse identified redistribution,
hash growth, and leaf creation as major costs; stack filtering excludes fixture
loading from that report.

Raw benchmark runs, tests, profiles, and reproduction commands are saved in
[benchmark-results/2026-10-01](benchmark-results/2026-10-01).
The tables use
[readme-shared-buffer.txt](benchmark-results/2026-10-01/readme-shared-buffer.txt).
Earlier comparison runs and cache-mode results are saved in
[comparison-extended.txt](benchmark-results/2026-10-01/comparison-extended.txt),
with those medians in
[comparison-extended-medians.csv](benchmark-results/2026-10-01/comparison-extended-medians.csv).

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

Our Go implementation currently requires external synchronization; shared ordered scans need an exclusive lock. Dict’s insertion-ordered traversal follows its linked sequence and does not sort BPA blocks.

------------------
Copyright (C) 2026, Jason E. Aten, Ph.D.

License: MIT. See the LICENSE file.
