.PHONY: bench

BENCH_TIME ?= 100ms
BENCH_COUNT ?= 3

bench:
	go test -v -run '^TestReadmeBenchmarkTable$$' -count=1 -benchtime='$(BENCH_TIME)' -timeout=15m -args -bench-table -bench-table-count='$(BENCH_COUNT)'
