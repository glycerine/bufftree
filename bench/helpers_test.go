package bench

import (
	"math/rand"
	"os"
	"strconv"
	"testing"
)

var benchSink uint64

func reportIteration(b *testing.B, keys uint64) {
	if keys == 0 {
		return
	}
	b.ReportMetric(float64(keys)/b.Elapsed().Seconds(), "entries/s")
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(keys), "iter_ns/key")
	b.ReportMetric(float64(keys)/float64(b.N), "keys/op")
}

func benchLoadSize() int {
	if s := os.Getenv("BUFFTREE_BENCH_N"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 2 {
			panic("BUFFTREE_BENCH_N must be an integer >= 2")
		}
		return n
	}
	return 1 << 16
}

// Keep the key permutation and uniform RNG sequence identical to the paper
// benchmarks in ../benchmark_test.go, so existing measurements stay comparable.
// Even keys are loaded records; odd keys are disjoint miss keys.
func benchKey(rank int) uint64 {
	const mask = uint64(1<<63 - 1)
	x := uint64(rank) & mask
	x ^= x >> 30
	x = x * 0x3f58476d1ce4e5b9 & mask
	x ^= x >> 27
	x = x * 0x14d049bb133111eb & mask
	x ^= x >> 31
	return x << 1
}

type benchOp struct {
	key    uint64
	length int
}

func benchTrace(n, count, maxLen int) []benchOp {
	rng := rand.New(rand.NewSource(2023))
	ops := make([]benchOp, count)
	for i := range ops {
		rank := rng.Intn(n)
		rng.Intn(100) // Consume the paper benchmarks' mixed-workload roll.
		ops[i] = benchOp{key: benchKey(rank), length: rng.Intn(maxLen + 1)}
	}
	return ops
}
