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
| Get                        |     20.8 |           16.7 |         114.3 |          201.9 |
| Put                        |    100.8 |           28.9 |         126.8 |          204.4 |
| Ordered scan               |     4.49 |  not supported |          4.12 |          15.87 |
| Dict traversal             |     2.60 |          10.02 |          2.54 |          15.42 |

Detailed table (ns/key):

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

--- PASS: TestReadmeBenchmarkTable (143.98s)
PASS
ok  	github.com/glycerine/bufftree/bench	143.985s
