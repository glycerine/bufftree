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

| Operation (showing ns/key) | BPTree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -----: | -------------: | ------------: | -------------: |
| Get                        |  101.4 |           16.7 |         111.7 |          203.1 |
| Put                        |  236.0 |          169.8 |         308.9 |          576.0 |
| Ordered scan               |   3.40 |  not supported |          4.10 |          15.76 |
| Dict traversal             |   2.70 |          10.05 |          2.57 |          15.36 |

Detailed table (ns/key):

| Operation (showing ns/key)    | BPTree | builtin Go map | tidwall/btree | red-black tree |
| ----------------------------- | -----: | -------------: | ------------: | -------------: |
| Tree `Get`, hit               |  101.4 |           16.7 |         111.7 |          203.1 |
| Tree `Get`, miss              |  100.9 |           16.1 |         117.4 |          224.3 |
| Tree `Put`, existing key      |   98.5 |           26.7 |         114.0 |          202.7 |
| Tree `Put`, fresh key         |  236.0 |          169.8 |         308.9 |          576.0 |
| Dict `Get`, hit               |  103.3 |           16.7 |         111.7 |          203.1 |
| Dict `Put`, existing key      |  103.5 |           26.7 |         114.0 |          202.7 |
| Dict `Put`, fresh key         |  542.6 |          169.8 |         308.9 |          576.0 |
| Ordered scan, maximum 10,000  |   3.42 |  not supported |          4.10 |          15.72 |
| Ordered scan, maximum 100,000 |   3.40 |  not supported |          4.10 |          15.76 |
| Dict traversal                |   2.70 |          10.05 |          2.57 |          15.36 |

--- PASS: TestReadmeBenchmarkTable (90.06s)
PASS
