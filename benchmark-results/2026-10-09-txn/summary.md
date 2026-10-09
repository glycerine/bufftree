# Transaction API measurements — 2026-10-09

Machine: AMD Ryzen Threadripper 3960X, Linux/amd64, Go 1.26.4.
No CPU affinity was applied. Comparisons load 65,536 uint64 key/value pairs.

Commands:

```sh
make bench
go test -run '^$' -bench '^BenchmarkTransaction' -benchmem -benchtime=100ms -count=1
go test .
go test -race .
go vet .
go -C bench test .
go -C bench vet .
lean +leanprover/lean4:v4.34.1 txn.lean
```

All passed. In this workspace, Go commands used
`GOCACHE=/tmp/bufftree-go-cache` because the usual cache was not writable.

`readme.txt` is the complete `make bench` output: medians of three 100ms
samples, measuring 26 distinct cases. Both README tables are copied from it.

| Fresh insertion | BP-tree ns/key | tidwall ns/key |
| --- | ---: | ---: |
| One complete transaction per key | 1105.4 | 321.4 |
| Up to 1,024 keys per batch | 711.4 | 312.3 |

The batch row includes journal creation, tree mutation, sorting remaining dirty
streams, exact-count reconciliation, and transaction cleanup. Tidwall uses its
existing generic Map adapter; neither it nor the other baselines adds rollback
or isolation. All receive the same fresh keys in the same order. Fixtures load
outside the timer. Partial batches divide by the actual inserted count.

Batching reduces BP-tree time per key by about 36%, but the BP-tree does not beat
tidwall on this workload. The extra old-value lookup and chronological journal
are part of real rollback. These results must not be presented as the old
unlocked tree's throughput, or as equivalent concurrency guarantees.

`transactions.txt` contains separate API microbenchmarks. Its sequential
existing-key updates differ from the random fresh-insertion comparison above:
1,024 updates per transaction measured 255.8 ns/key. Forward read scans used one
80-byte transaction allocation per 4,096-entry scan; they did not allocate a
range image. Journal allocation/growth is included in write B/op, and full-table
delete/rollback includes undo storage. Isolated Commit preparation excludes
preceding writes and therefore must not replace the end-to-end batch metric.

`race.txt`, `bench-tests.txt`, `vet.txt`, and `lean.txt` preserve validation
output. The empty vet output means no diagnostics. Lean proves the abstract
transaction protocol and journal restoration, not the concrete Go implementation
or BPA cursor enumeration. The concrete checks include randomized rollback,
NaN/signed-zero representatives, reverse/forward search, mutation during scans,
physical reader purity, snapshot isolation, pending writer admission, and
managed/manual cleanup on errors, panic, and Goexit.

Historical unlocked comparison: `previous-readme-tables.md`, preserved from the
previous README with its source commit recorded.
