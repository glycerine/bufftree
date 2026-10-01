.PHONY: bench memory

BENCH_TIME ?= 100ms
BENCH_COUNT ?= 3

bench:
	go -C bench test -v -run '^TestReadmeBenchmarkTable$$' -count=1 -benchtime='$(BENCH_TIME)' -timeout=15m -args -bench-table -bench-table-count='$(BENCH_COUNT)'

memory:
	go -C bench test -v -run '^TestMemoryUsage100K$$' -count=1 -timeout=5m
