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

The Put row measures one fresh key in a complete write transaction.
**Put batch (amortized)** measures 1,024 fresh keys per write transaction,
including undo journaling and commit preparation, divided by actual keys.
Other containers insert the same keys but do not provide the BP-tree transaction
or rollback guarantee. Scans use one read-only transaction per scan.

| Operation (showing ns/key) | BPTree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -----: | -------------: | ------------: | -------------: |
| Get                        |  325.1 |           20.0 |         135.1 |          206.2 |
| Put                        |  652.7 |          164.7 |         309.7 |          628.1 |
| Put batch (amortized)      |  268.6 |          166.4 |         303.7 |          638.8 |
| Ordered scan               |   6.16 |  not supported |          5.02 |          18.56 |

~~~
This compares:
a) bufftree.NewBPTree(nil) defaults.
b) The built-in Go map; `m := make(map[uint64]uint64)`
c) https://github.com/tidwall/btree
d) https://github.com/glycerine/rbtree
~~~
Use `make bench` to re-run on your machine.

The optimized 1,024-key batch takes 268.6 ns/key versus tidwall’s 303.7 ns/key
on this workload, including rollback journaling and commit preparation. Single-key
transactions remain slower. See [batch optimization](#batch-transaction-optimization-2026-10-09)
for pinned confirmation, independent-key results, and the memory tradeoff. Earlier
unlocked measurements below are historical baselines.

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
Write transactions may sort blocks as needed for their own scans. Commit and
rollback prepare modified leaves before releasing the exclusive lock. Shared
read transactions merge the already sorted streams without modifying storage.
When blocks fill unevenly, the BPA redistributes entries across them.
`Tree[K,V]` iterates in key order and supports range queries, point lookups,
updates, and deletion. Keys may be any
`cmp.Ordered` type, including named types; values may be any type. The zero
value is usable. Do not copy a tree after its first use.
The Config struct can be used to tune memory use.

```go
db := bufftree.NewBPTree[int, string](nil)
err := db.Update(func(tx *bufftree.WriteTx[int, string]) error {
    for k, v := range map[int]string{30: "thirty", 10: "ten", 20: "twenty"} {
        if err := tx.Put(k, v); err != nil { return err }
    }
    for key, value := range tx.All() {
        fmt.Println(key, value) // 10 ten, 20 twenty, 30 thirty
        if err := tx.Delete(key); err != nil { return err }
    }
    return nil // Commit. Returning an error rolls back every write above.
})
if err != nil { panic(err) }
```

## Transactions and iteration

Every data operation requires a transaction. A `ReadOnlyTx` holds shared access
for its entire lifetime, so overlapping readers see stable committed contents.
A `WriteTx` excludes all other transactions and sees its own writes. There are
no direct `Tree.Put`, `Get`, `Len`, or scan methods.

| Database method | Lifetime |
| --- | --- |
| `View(func(*ReadOnlyTx) error)` | Shared lock through callback return or panic |
| `Update(func(*WriteTx) error)` | Exclusive lock; commit on nil, rollback on error/panic/Goexit |
| `BeginView()` | Caller must `Close()` |
| `BeginUpdate()` | Returns `(tx, error)`; immediately defer `tx.Rollback()` |

Manual write transactions end with `Commit` or `Rollback`. First terminal action
wins; cleanup is idempotent. Explicit commit/rollback inside `Update` ends the
handle, but the wrapper keeps the lock until the callback exits. An error after
explicit commit cannot undo that commit. Explicit `Close` inside `View` similarly
leaves lock release to the wrapper.

Use each transaction sequentially in one goroutine. **Never begin a nested
transaction on the same database**, including read-within-read. Pass the existing
transaction to helpers. Callbacks may use that transaction again, including to
mutate during a write scan. They must not wait on work that needs the same lock.

| Transaction method | Behavior |
| --- | --- |
| `Get(key)` | `(value, found, error)`; nil/zero values can be present |
| `Len()` | Exact `int64`; read-only transactions do no reconciliation |
| `NewIter()` | Unpositioned bidirectional iterator |
| `GetKV`, `Find`, `FindIt` | Owned result holders; `Exact`, `GTE`, `LTE`, `GT`, `LT` |
| `All()` | Streaming `iter.Seq2[K,V]` |
| `Scan(start, length, visit)` | At most length keys at or above start |
| `Ascend`, `Descend` | Inclusive pivot, ascending/descending |
| `Range` / `AscendRange(lo, hi, visit)` | `[lo, hi)` |
| `DescendRange(hi, lo, visit)` | `(lo, hi]`, descending |
| `MapRange(lo, hi, visit)` | Same streaming traversal as `Range` |
| Write-only `Put`, `Delete` / `Del` | Return error; journal before mutation |
| Write-only `DeleteRange(lo, hi, loInclusive, hiInclusive)` | `(count, allGone, error)` |
| Write-only `Clear()` | `(allGone, error)`; fully rollbackable |
| Write-only `Merge(key, fn)` | Callback returns `(value, write, delete)`; both flags true is an error |

`FindIt` returns `(kv, exact, err, it)`. A miss returns an invalid iterator;
invalid modifiers or a closed transaction return an error without registering
resources. `KVcloser.Close()` is nil-safe and never unlocks the database.
Closing a holder removes it from the active resource registry. The transaction
automatically closes all outstanding iterators and result holders.

```go
err = db.View(func(tx *bufftree.ReadOnlyTx[int, string]) error {
    it := tx.NewIter()
    defer it.Close()
    for it.Seek(10); it.Valid(); it.Next() {
        fmt.Println(it.Key(), it.Value())
    }
    return nil
})
```

`Seek`, `SeekFirst`, and `SeekLast` position immediately. `Next` and `Prev` return
nothing; test `Valid`. Moving an invalid iterator does nothing until another
seek. `KV()` returns an iterator-owned pair, valid until movement or cleanup.
`Key` and `Value` return zero values on an invalid iterator. A closed iterator
cannot access tree storage. Closed transaction methods return `ErrTxClosed`;
helpers without an error result panic with it.

Read-only scans see one stable state across their whole span. Write scans are
live: deletion of the current entry, splits, merges, nested preparation, and
`Clear` invalidate cursors; the next step seeks strictly beyond the saved key
(or before it in reverse). A newly inserted key ahead may be visited; one behind
is not revisited. Self-extending callbacks may therefore fail to terminate.
There is no range copy before invoking a visitor, and returning false stops
immediately.

Keys use their natural order, with NaNs equivalent and sorted **after** all
other keys. Signed zeros compare equal; zero and empty strings are real keys,
not unbounded endpoint markers. Use `SeekFirst`/`SeekLast` for unbounded scans.

**Values containing references must be immutable.** `Get` and iterators copy Go
values shallowly. Replace a slice, map, or object binding with `Put`; mutating it
in place bypasses synchronization and rollback. Undo memory grows with write
history. `Clear` records the removed contents, and deleting an entire table uses
O(deleted entries) journal space even though its scan streams. Commit provides
atomic in-memory publication, not disk durability.

See [txn_design.md](txn_design.md) for the lifecycle and proof obligations.

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

`WriteTx.Put` descends once. A per-leaf membership filter can prove a key absent;
a possible match uses exact lookup. The transaction journals the old binding or
absence before modifying storage. Existing entries are updated in place; fresh
keys enter the insertion log with membership already counted. When all log keys
are known not to shadow live base entries, flushing skips duplicate searches.
Unchecked core operations conservatively disable that shortcut when necessary.
A full log flushes into blocks; block overflow redistributes records.

Publication sorts the remaining log and changed blocks without requiring a
flush of every log. Sorted-prefix lengths let preparation skip already ordered
records, inserting only the disturbed suffix into order. Pure forward and reverse cursors merge the log with the
header/block stream, with buffered records and tombstones taking precedence.
`Scan`, `Range`, `MapRange`, and `All` consume contiguous runs. Explicit iterators
keep their own small cursors. Writer-side preparation and logical writes advance
a transaction epoch, so stale writer cursors reseek before touching old storage.
Every callback traversal checks lifetime and mutation before reusing a run.

Point lookups descend through internal separators, check the insertion log,
and use the sorted header to select a block. Deletion also resolves occupancy
for rebalancing. Lookups within an existing transaction allocate no memory;
transaction entry and explicit resource handles have their own allocation costs.

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
examples. Transaction tests cover whole-span isolation, physically pure concurrent
readers, writer admission, real rollback, managed/manual lifetime, resource cleanup,
panics, and Goexit. Core-level allocation tests cover the underlying algorithms;
transaction allocation costs are measured separately. Benchmark operation traces
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

Current `make memory` results, after the batch transaction optimization:

| Container         | Heap bytes | MiB  | B/key |
| ----------------- | ---------: | ---: | ----: |
| bufftree.BPTree   |    2736576 | 2.61 | 27.37 |
| builtin Go map    |    2364576 | 2.26 | 23.65 |
| tidwall/btree.Map |    2513408 | 2.40 | 25.13 |
| glycerine/rbtree  |    6400080 | 6.10 | 64.00 |

It reports retained heap bytes, MiB, and bytes per key for BPTree,
the built-in Go map, tidwall's generic Map, and rbtree. It shares the timing
benchmarks' constructors, uint64 key/value data, and insertion order; the Go map uses the same capacity hint. Each container is
measured in a fresh test process with `GOMAXPROCS=1`, with forced GC before and
after loading, and kept alive through the final heap measurement. Results subtract
the initial `runtime.MemStats.HeapAlloc` baseline and exclude discarded
temporary allocations; they measure live Go heap, rather than process RSS or
heap reserved by the runtime. `make memory` and `make bench` run separately,
and the memory test never invokes `testing.Benchmark`.
The [current memory report](benchmark-results/2026-10-09-txn-opt/memory.txt)
measures 27.37 B/key for BP-tree, versus 25.62 B/key for the baseline transaction implementation.
The filter and sorted-prefix metadata add about 6.8% on this load. Record width
remains 16 bytes. The point-at-a-time memory fixture does not retain a batch
journal; a database used for batches may additionally keep up to 4,096 cleared
undo records (at most 96 KiB for uint64 keys and values). No old value references
are retained in that scratch buffer.

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
| `BenchmarkScanFirst` (in `bench/`) | First scan after loading, publication preparation is paid by preceding writes |
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
benchmark by default and reporting their median. It measures only the 26
distinct cases needed for the two README tables and reuses shared results.
The longer Make command above increases sampling to five 250ms runs. The
current tables below use the default three 100ms runs. `BUFFTREE_BENCH_N` also controls the Make target's load size.
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

## Historical unlocked measurements

The following two sections describe the earlier, nontransactional implementation.
Their timings and API tradeoffs are retained for comparison and do not describe
the current transaction cost.

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

These historical scan and tuning figures precede the transaction API.

## Measured performance

Measured on 2026-10-09 on an AMD Ryzen Threadripper 3960X, Linux/amd64,
Go 1.26.4, with 65,536 uint64 keys/values and the default leaf layout. These
are unpinned medians of three 100ms samples from `make bench`. The BP-tree
uses the public transaction API. Each point operation and each scan includes
transaction entry and exit. Batch Put uses up to 1,024 fresh keys in one
`Update`; its ns/key includes the journal and Commit. Partial batches divide
by their actual key count.

Baselines are published `github.com/tidwall/btree v1.8.1` and
`github.com/glycerine/rbtree v0.2.2`. The tidwall baseline is its generic `Map`
with default degree 32 and no path hints or copies. Baselines perform the same
operations without adding transaction isolation or rollback. These are API
cost comparisons, not equivalent concurrency guarantees.

| Operation (showing ns/key)    | BPTree | builtin Go map | tidwall/btree | red-black tree |
| ----------------------------- | -----: | -------------: | ------------: | -------------: |
| Tree `Get`, hit               |  325.1 |           20.0 |         135.1 |          206.2 |
| Tree `Get`, miss              |  358.5 |           19.1 |         135.5 |          250.0 |
| Tree `Put`, existing key      |  402.6 |           32.5 |         144.6 |          227.1 |
| Tree `Put`, fresh key         |  652.7 |          164.7 |         309.7 |          628.1 |
| Put batch (amortized)         |  268.6 |          166.4 |         303.7 |          638.8 |
| Ordered scan, maximum 10,000  |   6.23 |  not supported |          5.10 |          18.01 |
| Ordered scan, maximum 100,000 |   6.16 |  not supported |          5.02 |          18.56 |

Fresh inserts use the same unique odd keys against an initially even-key
65,536-entry fixture. Every container grows to 131,072 entries, then reloads
outside the timer. Both single-key and batch rows include insertion, growth,
and finalization costs inside the timer. The batch fixture and key order match
the single-key workload. Go map uses assignments; competitor return values are
discarded.

Scan figures divide by actual visited keys; lengths vary up to the named maximum
and stop at the tree's end. No full result is copied before callbacks. Earlier
unlocked README tables are preserved in
[previous-readme-tables.md](benchmark-results/2026-10-09-txn/previous-readme-tables.md).
The current tables come from
[readme.txt](benchmark-results/2026-10-09-txn-opt/readme.txt).

Additional transaction benchmarks cover batched reads/writes, reverse traversal,
concurrent read scans, deleting scans with rollback, and isolated commit
preparation:

```sh
go test -run '^$' -bench '^BenchmarkTransaction' -benchmem
```

The isolated commit benchmark excludes preceding mutations; the end-to-end Put
rows include them. `B/op` in a batch benchmark is per batch, while `put_ns/key`
is per inserted key. These single-thread comparison workloads do not measure
lock acquisition contention or promise bounded writer latency.

## Batch transaction optimization (2026-10-09)

Profiling the complete 1,024-key transaction identified repeated routing and
membership searches, journal growth, and sorting already ordered prefixes.
The implementation now:

* Reuses one descent and lookup for undo, mutation, and exact membership counts.
* Updates existing bindings in place and avoids duplicate searches when flushing
  a log proven not to shadow live base entries.
* Uses a blocked membership filter to reject absent keys without exact searching.
  Possible matches still take the exact path. NaNs are normalized before hashing;
  named types and signed zeros retain the BP-tree's equality semantics.
* Keeps sorted-prefix lengths for the log and blocks, so publication repairs only
  disturbed suffixes.
* Stores compact chronological undo records, separates `Clear` images, and reuses
  bounded, cleared journal storage. Rollback and publication guarantees remain.

Five CPU-6 runs, one execution thread, each timing 2,097,152 inserts in complete
1,024-key transactions, gave these medians:

| Fresh-key workload | BP-tree ns/key | tidwall ns/key |
| --- | ---: | ---: |
| Adjacent to initially loaded keys | 136.9 | 149.9 |
| Independent scrambled keys | 151.5 | 152.8 |

The original transaction implementation took 352.9 ns/key on the pinned adjacent
trace. The optimized version takes about 61% less time. It is about 9% below
tidwall on that trace; the independent-key result is effectively a tie. The
unpinned `make bench` batch row is about 12% below tidwall. These runs still use
the original geometry: fanout 256, log 42, 32 blocks of size 34. The gains come
from implementation changes, not a change to batch size or removal of rollback.

Sorting remains writer work, before publication. The filter never permits a
probabilistic answer: a false positive costs an exact search, and tests exercise
forced collisions, equivalent float keys, structural changes, and rollback.
The abstract negative-filter implication is also checked in `txn.lean`; concrete
hash/layout correctness is tested in Go.

See [the profiling and validation report](benchmark-results/2026-10-09-txn-opt/summary.md)
for commands, successive measurements, profiles, and limitations. Pinned and
unpinned measurements must be compared within their own runs.

## notes on concurrency

The implementation uses one database-wide `sync.RWMutex`. Read-only transactions
hold `RLock`; writes hold `Lock`. Transaction methods reuse ownership and never
upgrade or reacquire that lock. Commit and rollback sort remaining dirty streams
and finalize counts **before unlocking**, so shared scans make no physical tree
changes. Logs can remain buffered; publication does not flush every log.

Read consistency lasts for the entire transaction, including a multi-leaf scan.
A writer cannot publish between two entries in a read transaction. Writer
progress requires active transactions to finish and ordinary scheduler/lock
admission progress; the API does not promise FIFO acquisition or bounded waiting.
Long transactions and leaked manual handles delay other work by design.

[txn.lean](txn.lean) checks abstract ownership, read-ready publication, stable
read spans, no internal wait cycles, terminal behavior, and reverse journal
restoration. It does not verify the concrete Go implementation or BPA enumeration.
Unit tests, randomized model comparisons, and `go test -race .` exercise those
implementation obligations. The paper's per-node locking scheme is not used.

------------------
Copyright (C) 2026, Jason E. Aten, Ph.D.

License: MIT. See the LICENSE file.
