go -C bench test -v -run '^TestReadmeBenchmarkTable$' -count=1 -benchtime='250ms' -timeout=15m -args -bench-table -bench-table-count='5'
=== RUN   TestReadmeBenchmarkTable
    readme_bench_test.go:73: [1/39] Points/Tree/GetHit
    readme_bench_test.go:73: [2/39] Points/TreeHash/GetHit
    readme_bench_test.go:73: [3/39] Points/GoMap/GetHit
    readme_bench_test.go:73: [4/39] Points/Tidwall/GetHit
    readme_bench_test.go:73: [5/39] Points/RBTree/GetHit
    readme_bench_test.go:73: [6/39] Points/Tree/GetMiss
    readme_bench_test.go:73: [7/39] Points/TreeHash/GetMiss
    readme_bench_test.go:73: [8/39] Points/GoMap/GetMiss
    readme_bench_test.go:73: [9/39] Points/Tidwall/GetMiss
    readme_bench_test.go:73: [10/39] Points/RBTree/GetMiss
    readme_bench_test.go:73: [11/39] Points/Tree/Update
    readme_bench_test.go:73: [12/39] Points/TreeHash/Update
    readme_bench_test.go:73: [13/39] Points/GoMap/Update
    readme_bench_test.go:73: [14/39] Points/Tidwall/Update
    readme_bench_test.go:73: [15/39] Points/RBTree/Update
    readme_bench_test.go:73: [16/39] Points/Tree/FreshPut
    readme_bench_test.go:73: [17/39] Points/TreeHash/FreshPut
    readme_bench_test.go:73: [18/39] Points/GoMap/FreshPut
    readme_bench_test.go:73: [19/39] Points/Tidwall/FreshPut
    readme_bench_test.go:73: [20/39] Points/RBTree/FreshPut
    readme_bench_test.go:73: [21/39] Points/Dict/GetHit
    readme_bench_test.go:73: [22/39] Points/DictHash/GetHit
    readme_bench_test.go:73: [23/39] Points/Dict/Update
    readme_bench_test.go:73: [24/39] Points/DictHash/Update
    readme_bench_test.go:73: [25/39] Points/Dict/FreshPut
    readme_bench_test.go:73: [26/39] Points/DictHash/FreshPut
    readme_bench_test.go:73: [27/39] Iteration/Tree/NoHash/Scan10000
    readme_bench_test.go:73: [28/39] Iteration/Tree/Hash/Scan10000
    readme_bench_test.go:73: [29/39] Iteration/Tidwall/Scan10000
    readme_bench_test.go:73: [30/39] Iteration/RBTree/Scan10000
    readme_bench_test.go:73: [31/39] Iteration/Tree/NoHash/Scan100000
    readme_bench_test.go:73: [32/39] Iteration/Tree/Hash/Scan100000
    readme_bench_test.go:73: [33/39] Iteration/Tidwall/Scan100000
    readme_bench_test.go:73: [34/39] Iteration/RBTree/Scan100000
    readme_bench_test.go:73: [35/39] Iteration/Dict/NoHash/Iterate
    readme_bench_test.go:73: [36/39] Iteration/Dict/Hash/Iterate
    readme_bench_test.go:73: [37/39] Iteration/GoMap/Iterate
    readme_bench_test.go:73: [38/39] Iteration/Tidwall/Iterate
    readme_bench_test.go:73: [39/39] Iteration/RBTree/Iterate

go1.26.4 linux/amd64; 65536 entries; medians of 5 runs with -benchtime=250ms

Simplified table (ns/key):

| Operation (showing ns/key) | bufftree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -------------: | ------------: | -------------: |
| Get                        |     22.3 |           16.6 |         121.0 |          202.2 |
| Put                        |    108.5 |           29.0 |         125.1 |          203.2 |
| Ordered scan               |     4.66 |  not supported |          4.10 |          15.81 |
| Dict traversal             |     2.09 |          10.09 |          2.64 |          15.33 |

Detailed table (ns/key):

| Operation (showing ns/key)    | bufftree | bufftree(2) | builtin Go map | tidwall/btree | red-black tree |
| ----------------------------- | -------: | ----------: | -------------: | ------------: | -------------: |
| Tree `Get`, hit               |    110.7 |        22.3 |           16.6 |         121.0 |          202.2 |
| Tree `Get`, miss              |    108.1 |        28.4 |           16.1 |         115.9 |          222.0 |
| Tree `Put`, existing key      |    169.5 |       108.5 |           29.0 |         125.1 |          203.2 |
| Tree `Put`, fresh key         |    717.6 |       890.5 |          180.4 |         312.8 |          626.9 |
| Dict `Get`, hit               |    113.9 |        26.8 |           16.6 |         121.0 |          202.2 |
| Dict `Put`, existing key      |    115.6 |        27.7 |           29.0 |         125.1 |          203.2 |
| Dict `Put`, fresh key         |   1088.0 |      1118.3 |          180.4 |         312.8 |          626.9 |
| Ordered scan, maximum 10,000  |     4.77 |        4.72 |  not supported |          4.07 |          15.74 |
| Ordered scan, maximum 100,000 |     4.67 |        4.66 |  not supported |          4.10 |          15.81 |
| Dict traversal                |     2.25 |        2.09 |          10.09 |          2.64 |          15.33 |

--- PASS: TestReadmeBenchmarkTable (147.92s)
PASS
ok  	github.com/glycerine/bufftree/bench	147.926s
