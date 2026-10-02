go -C bench test -v -run '^TestReadmeBenchmarkTable$' -count=1 -benchtime='250ms' -timeout=15m -args -bench-table -bench-table-count='5'
=== RUN   TestReadmeBenchmarkTable
    readme_bench_test.go:73: [1/29] Points/Tree/GetHit
    readme_bench_test.go:73: [2/29] Points/GoMap/GetHit
    readme_bench_test.go:73: [3/29] Points/Tidwall/GetHit
    readme_bench_test.go:73: [4/29] Points/RBTree/GetHit
    readme_bench_test.go:73: [5/29] Points/Tree/GetMiss
    readme_bench_test.go:73: [6/29] Points/GoMap/GetMiss
    readme_bench_test.go:73: [7/29] Points/Tidwall/GetMiss
    readme_bench_test.go:73: [8/29] Points/RBTree/GetMiss
    readme_bench_test.go:73: [9/29] Points/Tree/Update
    readme_bench_test.go:73: [10/29] Points/GoMap/Update
    readme_bench_test.go:73: [11/29] Points/Tidwall/Update
    readme_bench_test.go:73: [12/29] Points/RBTree/Update
    readme_bench_test.go:73: [13/29] Points/Tree/FreshPut
    readme_bench_test.go:73: [14/29] Points/GoMap/FreshPut
    readme_bench_test.go:73: [15/29] Points/Tidwall/FreshPut
    readme_bench_test.go:73: [16/29] Points/RBTree/FreshPut
    readme_bench_test.go:73: [17/29] Points/Dict/GetHit
    readme_bench_test.go:73: [18/29] Points/Dict/Update
    readme_bench_test.go:73: [19/29] Points/Dict/FreshPut
    readme_bench_test.go:73: [20/29] Iteration/Tree/Scan10000
    readme_bench_test.go:73: [21/29] Iteration/Tidwall/Scan10000
    readme_bench_test.go:73: [22/29] Iteration/RBTree/Scan10000
    readme_bench_test.go:73: [23/29] Iteration/Tree/Scan100000
    readme_bench_test.go:73: [24/29] Iteration/Tidwall/Scan100000
    readme_bench_test.go:73: [25/29] Iteration/RBTree/Scan100000
    readme_bench_test.go:73: [26/29] Iteration/Dict/Iterate
    readme_bench_test.go:73: [27/29] Iteration/GoMap/Iterate
    readme_bench_test.go:73: [28/29] Iteration/Tidwall/Iterate
    readme_bench_test.go:73: [29/29] Iteration/RBTree/Iterate

go1.26.4 linux/amd64; 65536 entries; medians of 5 runs with -benchtime=250ms

Simplified table (ns/key):

| Operation (showing ns/key) | bufftree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -------------: | ------------: | -------------: |
| Get                        |    109.2 |           16.7 |         116.2 |          201.1 |
| Put                        |    172.1 |           28.7 |         123.8 |          202.9 |
| Ordered scan               |     5.08 |  not supported |          4.14 |          15.62 |
| Dict traversal             |     2.56 |          10.08 |          2.59 |          15.37 |

Detailed table (ns/key):

| Operation (showing ns/key)    | bufftree | builtin Go map | tidwall/btree | red-black tree |
| ----------------------------- | -------: | -------------: | ------------: | -------------: |
| Tree `Get`, hit               |    109.2 |           16.7 |         116.2 |          201.1 |
| Tree `Get`, miss              |    111.5 |           15.6 |         117.8 |          221.8 |
| Tree `Put`, existing key      |    172.1 |           28.7 |         123.8 |          202.9 |
| Tree `Put`, fresh key         |    682.4 |          178.7 |         307.4 |          599.8 |
| Dict `Get`, hit               |    103.5 |           16.7 |         116.2 |          201.1 |
| Dict `Put`, existing key      |    103.3 |           28.7 |         123.8 |          202.9 |
| Dict `Put`, fresh key         |    919.2 |          178.7 |         307.4 |          599.8 |
| Ordered scan, maximum 10,000  |     5.19 |  not supported |          4.13 |          15.64 |
| Ordered scan, maximum 100,000 |     5.08 |  not supported |          4.14 |          15.62 |
| Dict traversal                |     2.56 |          10.08 |          2.59 |          15.37 |

--- PASS: TestReadmeBenchmarkTable (97.59s)
PASS
ok  	github.com/glycerine/bufftree/bench	97.594s
