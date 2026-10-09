=== RUN   TestReadmeBenchmarkTable
    readme_bench_test.go:69: [1/22] Points/Tree/GetHit
    readme_bench_test.go:69: [2/22] Points/GoMap/GetHit
    readme_bench_test.go:69: [3/22] Points/Tidwall/GetHit
    readme_bench_test.go:69: [4/22] Points/RBTree/GetHit
    readme_bench_test.go:69: [5/22] Points/Tree/GetMiss
    readme_bench_test.go:69: [6/22] Points/GoMap/GetMiss
    readme_bench_test.go:69: [7/22] Points/Tidwall/GetMiss
    readme_bench_test.go:69: [8/22] Points/RBTree/GetMiss
    readme_bench_test.go:69: [9/22] Points/Tree/Update
    readme_bench_test.go:69: [10/22] Points/GoMap/Update
    readme_bench_test.go:69: [11/22] Points/Tidwall/Update
    readme_bench_test.go:69: [12/22] Points/RBTree/Update
    readme_bench_test.go:69: [13/22] Points/Tree/FreshPut
    readme_bench_test.go:69: [14/22] Points/GoMap/FreshPut
    readme_bench_test.go:69: [15/22] Points/Tidwall/FreshPut
    readme_bench_test.go:69: [16/22] Points/RBTree/FreshPut
    readme_bench_test.go:69: [17/22] Iteration/Tree/Scan10000
    readme_bench_test.go:69: [18/22] Iteration/Tidwall/Scan10000
    readme_bench_test.go:69: [19/22] Iteration/RBTree/Scan10000
    readme_bench_test.go:69: [20/22] Iteration/Tree/Scan100000
    readme_bench_test.go:69: [21/22] Iteration/Tidwall/Scan100000
    readme_bench_test.go:69: [22/22] Iteration/RBTree/Scan100000

go1.26.4 linux/amd64; 65536 entries; medians of 5 runs with -benchtime=250ms

Simplified table (ns/key):

| Operation (showing ns/key) | BPTree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -----: | -------------: | ------------: | -------------: |
| Get                        |  101.4 |           16.5 |         116.7 |          199.7 |
| Put                        |  212.6 |          168.8 |         319.1 |          583.4 |
| Ordered scan               |   3.13 |  not supported |          4.17 |          16.11 |

Detailed table (ns/key):

| Operation (showing ns/key)    | BPTree | builtin Go map | tidwall/btree | red-black tree |
| ----------------------------- | -----: | -------------: | ------------: | -------------: |
| Tree `Get`, hit               |  101.4 |           16.5 |         116.7 |          199.7 |
| Tree `Get`, miss              |   98.4 |           16.1 |         118.4 |          221.4 |
| Tree `Put`, existing key      |   97.2 |           27.1 |         126.9 |          207.0 |
| Tree `Put`, fresh key         |  212.6 |          168.8 |         319.1 |          583.4 |
| Ordered scan, maximum 10,000  |   3.11 |  not supported |          4.13 |          15.80 |
| Ordered scan, maximum 100,000 |   3.13 |  not supported |          4.17 |          16.11 |

--- PASS: TestReadmeBenchmarkTable (64.59s)
PASS
