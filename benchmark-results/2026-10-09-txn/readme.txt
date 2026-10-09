go -C bench test -v -run '^TestReadmeBenchmarkTable$' -count=1 -benchtime='100ms' -timeout=15m -args -bench-table -bench-table-count='3'
=== RUN   TestReadmeBenchmarkTable
    readme_bench_test.go:70: [1/26] Points/Tree/GetHit
    readme_bench_test.go:70: [2/26] Points/GoMap/GetHit
    readme_bench_test.go:70: [3/26] Points/Tidwall/GetHit
    readme_bench_test.go:70: [4/26] Points/RBTree/GetHit
    readme_bench_test.go:70: [5/26] Points/Tree/GetMiss
    readme_bench_test.go:70: [6/26] Points/GoMap/GetMiss
    readme_bench_test.go:70: [7/26] Points/Tidwall/GetMiss
    readme_bench_test.go:70: [8/26] Points/RBTree/GetMiss
    readme_bench_test.go:70: [9/26] Points/Tree/Update
    readme_bench_test.go:70: [10/26] Points/GoMap/Update
    readme_bench_test.go:70: [11/26] Points/Tidwall/Update
    readme_bench_test.go:70: [12/26] Points/RBTree/Update
    readme_bench_test.go:70: [13/26] Points/Tree/FreshPut
    readme_bench_test.go:70: [14/26] Points/GoMap/FreshPut
    readme_bench_test.go:70: [15/26] Points/Tidwall/FreshPut
    readme_bench_test.go:70: [16/26] Points/RBTree/FreshPut
    readme_bench_test.go:70: [17/26] Points/Tree/FreshPutBatch
    readme_bench_test.go:70: [18/26] Points/GoMap/FreshPutBatch
    readme_bench_test.go:70: [19/26] Points/Tidwall/FreshPutBatch
    readme_bench_test.go:70: [20/26] Points/RBTree/FreshPutBatch
    readme_bench_test.go:70: [21/26] Iteration/Tree/Scan10000
    readme_bench_test.go:70: [22/26] Iteration/Tidwall/Scan10000
    readme_bench_test.go:70: [23/26] Iteration/RBTree/Scan10000
    readme_bench_test.go:70: [24/26] Iteration/Tree/Scan100000
    readme_bench_test.go:70: [25/26] Iteration/Tidwall/Scan100000
    readme_bench_test.go:70: [26/26] Iteration/RBTree/Scan100000

go1.26.4 linux/amd64; 65536 entries; medians of 3 runs with -benchtime=100ms

Simplified table (ns/key):

| Operation (showing ns/key) | BPTree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -----: | -------------: | ------------: | -------------: |
| Get                        |  321.2 |           19.8 |         133.8 |          226.5 |
| Put                        | 1105.4 |          167.0 |         321.4 |          648.2 |
| Put batch (amortized)      |  711.4 |          166.4 |         312.3 |          640.7 |
| Ordered scan               |   5.74 |  not supported |          4.75 |          17.74 |

Detailed table (ns/key):

| Operation (showing ns/key)    | BPTree | builtin Go map | tidwall/btree | red-black tree |
| ----------------------------- | -----: | -------------: | ------------: | -------------: |
| Tree `Get`, hit               |  321.2 |           19.8 |         133.8 |          226.5 |
| Tree `Get`, miss              |  323.2 |           18.8 |         140.4 |          240.2 |
| Tree `Put`, existing key      |  969.2 |           31.8 |         150.8 |          228.5 |
| Tree `Put`, fresh key         | 1105.4 |          167.0 |         321.4 |          648.2 |
| Put batch (amortized)         |  711.4 |          166.4 |         312.3 |          640.7 |
| Ordered scan, maximum 10,000  |   5.84 |  not supported |          4.82 |          17.22 |
| Ordered scan, maximum 100,000 |   5.74 |  not supported |          4.75 |          17.74 |

--- PASS: TestReadmeBenchmarkTable (28.92s)
PASS
ok  	github.com/glycerine/bufftree/bench	28.926s
