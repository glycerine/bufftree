# Optimizing 1,024-Put transactions — 2026-10-09

Baseline source: `fd48c40bcc6f6c9161ae43de82e71c7a43324675`.
Machine: AMD Ryzen Threadripper 3960X, Linux/amd64, Go 1.26.4.
Geometry unchanged: fanout 256, log 42, 32 blocks of size 34.
Transactions retain real rollback and prepare reader-visible state before unlock.

## Results

The unpinned `make bench` run uses 65,536 initially loaded uint64 keys/values and
medians of three 100ms samples. Each batch inserts up to 1,024 fresh keys,
including journal creation, mutation, commit preparation, cleanup, and unlocking.

| Fresh batch Put | ns/key |
| --- | ---: |
| Previous transaction README measurement | 711.4 |
| Optimized BP-tree | 268.6 |
| tidwall | 303.7 |

The optimized BP-tree takes about 62% less time than the previous transaction
implementation on this run and 12% less than tidwall (13% more keys/second).
Single-key transactions still lose to tidwall. See `readme.txt` for every row.

Pinned confirmation uses CPU 6, `-cpu=1`, five samples, and complete transaction
batches. Baseline samples insert 1,048,576 keys each; final samples insert
2,097,152 keys each. Both repeat the same complete 65,536-key growth cycles,
loading the next fixture outside the timer.

| Workload | Baseline BP-tree | Final BP-tree | Final tidwall |
| --- | ---: | ---: | ---: |
| Fresh keys adjacent to loaded keys | 352.9 | 136.9 | 149.9 |
| Independent scrambled fresh keys | not measured | 151.5 | 152.8 |

These are median ns/key. The final adjacent workload is 61% below the baseline
and about 9% below tidwall. The independent-key result is effectively a tie;
this is not a universal performance claim. Tidwall's baseline adjacent median
was 151.1 ns/key, close to its final 149.9 ns/key.

The competitors retain their existing adapters. Tidwall uses its generic Map
at default degree 32, without rollback or transaction semantics. All receive
the same key sequence. No write or finalization cost moved outside the timer.
Batch sizes, constructor defaults, and benchmark work were not reduced.

## Profile and changes

The original profile spent about 29% of sampled time in the undo preimage path,
17% in deferred membership reconciliation, and 31% in publication (overlapping
inclusive costs). It descended twice and searched membership repeatedly.

Changes, in order:

1. Fuse preimage discovery and mutation. One descent/lookup serves both. Update
   existing records in place; count fresh membership immediately. This eliminates
   a second tree descent and deferred duplicate searches for ordinary writes.
2. Compact undo records from 56 to 24 bytes for uint64 pairs. Store Clear images
   separately, retaining chronological reverse replay. Reuse cleared journal
   capacity up to 4,096 records; terminal cleanup also clears the inline record.
3. Skip exact absence searches using a per-leaf blocked membership filter.
   A possible match ALWAYS falls back to exact lookup. Go's comparable hash
   preserves ordinary equality; our wrapper maps all NaNs to one fixed hash.
   Filters are updated on insertion, rebuilt for cross-leaf loads, and preserved
   during redistribution within a leaf. Deleted bits may remain as false positives.
4. Track ordered prefixes in logs and blocks. Publication sorts only the
   disturbed suffix into its prefix. Deleting by swapping records shortens the
   affected block prefix, preventing stale sortedness claims.
5. Track the conservative fact that a log has no live base shadows. Known fresh
   logs can flush without repeating block membership searches. Unchecked core
   appends invalidate that fact when a duplicate could exist.

Pinned trial medians (ns/key):

| Trial | BP-tree |
| --- | ---: |
| Baseline | 352.9 |
| Fused lookup/counting and compact reused journal | 215.4 |
| Binary search of sorted log/base regions (rejected) | 249.9 |
| Linear lookup with log prefix tracking | 213.8 |
| Membership filter, smaller size | 168.3 |
| Block prefix tracking | 153.8 |
| Larger filter (1 KiB per default leaf) | 148.7 |
| No-shadow flush shortcut | 135.6 |
| Final five-run confirmation | 136.9 |

Raw outputs are retained in the corresponding `.txt` files. Trial differences
of a few nanoseconds are not independent statistical claims. The final profile
now primarily shows routing and sorting, rather than repeated membership checks.

## Memory and correctness

The same 100,000-entry heap fixture measured 25.62 B/key before and 27.37 B/key
after, about 6.8% more retained memory. The entry width remains 16 bytes. Filters
and prefix metadata account for the increase. The point-at-a-time load does not
retain a batch journal; after batch use the database may also retain at most
96 KiB of cleared uint64 undo scratch. It retains no old key/value references.
Total allocation traffic in the adjacent timed batch fell from 144,068 to
24,093 bytes/batch, including tree growth allocations.

Validation passed:

* Main package and benchmark-module tests, vet for both, and `go test -race .`.
* `go test -tags=purego .`, exercising the portable comparable-hash path.
* New tests for filter soundness during structural changes and rollback, forced
  filter collisions, named types, NaN/signed-zero equivalence, scratch reuse,
  and release of pointer-valued undo references after journal growth.
* Existing randomized journal/iterator models, concurrent reader purity,
  snapshot isolation, callback lifecycle tests, and BPA structural tests.
* Lean checks for the transaction model and the added abstract implication that
  a sound filter's negative probe proves absence. Concrete Go hashing/layout and
  sorted-prefix maintenance are tested, not claimed to be formally verified.

## Reproduction

In this workspace commands use `GOCACHE=/tmp/bufftree-go-cache` because the
usual cache is read-only. The scratch binaries below are outside the repository.

```sh
go -C bench test -c -o /tmp/bufftree-batch-final.test
taskset -c 6 /tmp/bufftree-batch-final.test -test.run '^$' \
  -test.bench '^BenchmarkTxnBatchDistribution$' \
  -test.benchtime=2048x -test.count=5 -test.cpu=1 -test.benchmem

taskset -c 6 /tmp/bufftree-batch-final.test -test.run '^$' \
  -test.bench '^BenchmarkComparePoints$/^Tree$/^FreshPutBatch$' \
  -test.benchtime=8192x -test.cpu=1 -test.cpuprofile=/tmp/batch.cpu

go tool pprof -top -relative_percentages '-tagfocus=container=^Tree$' \
  /tmp/bufftree-batch-final.test /tmp/batch.cpu
make bench
make memory
go test .
go test -race .
go test -tags=purego .
go vet .
go -C bench test .
go -C bench vet .
lean +leanprover/lean4:v4.34.1 txn.lean
```

The batch benchmark labels timed insertion/finalization separately from fixture
loading. CPU profiles include untimed work unless filtered; use the container
label. Saved `before.cpu` and `final.cpu` plus their text summaries preserve the
profiles. Pinned and unpinned timings should only be compared within their own
series. The filter seed is randomized per process, so collision costs can vary.
