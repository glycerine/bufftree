package bench

import (
	"fmt"
	"testing"

	"github.com/glycerine/bufftree"
)

// Separate fixed-work subbenchmarks let a sweep give fresh puts and scans
// appropriate iteration counts without changing their measured operations.
func BenchmarkTuneGeometry(b *testing.B) {
	var configs []bufftree.Config
	for _, fanout := range []int{16, 32, 48, 64, 96, 128, 192, 256, 384, 512} {
		configs = append(configs, bufftree.Config{Fanout: fanout, LogSize: 32, NumBlocks: 32, BlockSize: 32})
	}
	for _, pair := range [][2]int{{16, 16}, {16, 32}, {24, 32}, {32, 16}, {32, 24}, {32, 32}, {32, 48}, {32, 64}, {48, 32}, {64, 16}, {64, 32}, {64, 64}} {
		for _, logSize := range []int{8, 16, 24, 32, 48, 64} {
			if pair == [2]int{32, 32} && logSize == 32 {
				continue
			}
			configs = append(configs, bufftree.Config{Fanout: 64, LogSize: logSize, NumBlocks: pair[0], BlockSize: pair[1]})
		}
	}
	benchmarkConfigurations(b, configs)
}

func BenchmarkTuneRefine(b *testing.B) {
	var configs []bufftree.Config
	for _, fanout := range []int{64, 128, 192, 256, 384, 512} {
		for _, shape := range [][3]int{{32, 32, 32}, {16, 16, 32}, {32, 16, 32}, {32, 64, 32}, {32, 32, 64}} {
			configs = append(configs, bufftree.Config{Fanout: fanout, LogSize: shape[0], NumBlocks: shape[1], BlockSize: shape[2]})
		}
	}
	for _, fanout := range []int{160, 224, 320, 448, 640} {
		configs = append(configs, bufftree.Config{Fanout: fanout, LogSize: 32, NumBlocks: 32, BlockSize: 32})
	}
	for _, logSize := range []int{20, 24, 28, 36, 40, 48} {
		configs = append(configs, bufftree.Config{Fanout: 384, LogSize: logSize, NumBlocks: 32, BlockSize: 32})
	}
	for _, size := range []int{24, 28, 36, 40} {
		configs = append(configs,
			bufftree.Config{Fanout: 384, LogSize: 32, NumBlocks: size, BlockSize: 32},
			bufftree.Config{Fanout: 384, LogSize: 32, NumBlocks: 32, BlockSize: size})
	}
	benchmarkConfigurations(b, configs)
}

func BenchmarkTuneNeighborhood(b *testing.B) {
	var configs []bufftree.Config
	seen := map[bufftree.Config]bool{}
	add := func(cfg bufftree.Config) {
		if !seen[cfg] {
			seen[cfg] = true
			configs = append(configs, cfg)
		}
	}
	for _, fanout := range []int{64, 128, 160, 192, 256, 384} {
		for _, pair := range [][2]int{{36, 32}, {32, 36}} {
			for _, logSize := range []int{24, 28, 32, 36, 40} {
				add(bufftree.Config{Fanout: fanout, LogSize: logSize, NumBlocks: pair[0], BlockSize: pair[1]})
			}
		}
	}
	for _, size := range []int{32, 33, 34, 35, 36, 37, 38, 39, 40} {
		add(bufftree.Config{Fanout: 192, LogSize: 32, NumBlocks: size, BlockSize: 32})
		add(bufftree.Config{Fanout: 192, LogSize: 32, NumBlocks: 32, BlockSize: size})
	}
	for _, pair := range [][2]int{{34, 34}, {36, 36}, {28, 40}, {40, 28}, {24, 48}, {48, 24}} {
		add(bufftree.Config{Fanout: 192, LogSize: 32, NumBlocks: pair[0], BlockSize: pair[1]})
	}
	benchmarkConfigurations(b, configs)
}

func BenchmarkTuneFinalists(b *testing.B) {
	configs := []bufftree.Config{{Fanout: 64, LogSize: 32, NumBlocks: 32, BlockSize: 32}}
	for _, fanout := range []int{64, 128, 192, 256, 384} {
		configs = append(configs,
			bufftree.Config{Fanout: fanout, LogSize: 32, NumBlocks: 34, BlockSize: 32},
			bufftree.Config{Fanout: fanout, LogSize: 32, NumBlocks: 32, BlockSize: 34})
	}
	configs = append(configs,
		bufftree.Config{Fanout: 256, LogSize: 36, NumBlocks: 36, BlockSize: 32},
		bufftree.Config{Fanout: 256, LogSize: 32, NumBlocks: 36, BlockSize: 32},
		bufftree.Config{Fanout: 128, LogSize: 32, NumBlocks: 32, BlockSize: 36},
		bufftree.Config{Fanout: 256, LogSize: 32, NumBlocks: 32, BlockSize: 36},
		bufftree.Config{Fanout: 384, LogSize: 32, NumBlocks: 32, BlockSize: 36},
		bufftree.Config{Fanout: 192, LogSize: 32, NumBlocks: 40, BlockSize: 28},
		bufftree.Config{Fanout: 192, LogSize: 32, NumBlocks: 48, BlockSize: 24})
	benchmarkConfigurations(b, configs)
}

// Repeat a neighborhood sweep after changing defaults and optimizing separator
// growth. Both insertion traces remain available to avoid selecting for just
// the adjacent odd/even-key workload.
func BenchmarkTuneSecondLeg(b *testing.B) {
	configs := []bufftree.Config{{Fanout: 64, LogSize: 32, NumBlocks: 32, BlockSize: 32}}
	for _, fanout := range []int{64, 96, 128, 160, 192, 224, 256, 320, 384, 512} {
		configs = append(configs, bufftree.Config{Fanout: fanout, LogSize: 32, NumBlocks: 32, BlockSize: 34})
	}
	for _, logSize := range []int{16, 24, 28, 30, 34, 36, 40, 48} {
		configs = append(configs, bufftree.Config{Fanout: 256, LogSize: logSize, NumBlocks: 32, BlockSize: 34})
	}
	for _, blockSize := range []int{30, 31, 32, 33, 35, 36, 37, 38} {
		configs = append(configs, bufftree.Config{Fanout: 256, LogSize: 32, NumBlocks: 32, BlockSize: blockSize})
	}
	for _, numBlocks := range []int{28, 30, 31, 33, 34, 36} {
		configs = append(configs, bufftree.Config{Fanout: 256, LogSize: 32, NumBlocks: numBlocks, BlockSize: 34})
	}
	benchmarkConfigurations(b, configs)
}

func benchmarkConfigurations(b *testing.B, configs []bufftree.Config) {
	n := benchLoadSize()
	keys := make([]uint64, n)
	independent := make([]uint64, n)
	for i := range keys {
		keys[i] = benchKey(i) | 1
		independent[i] = benchKey(n + i)
	}
	ops := benchTrace(n, 8192, 100000)
	for _, cfg := range configs {
		name := fmt.Sprintf("f%d-l%d-h%d-b%d", cfg.Fanout, cfg.LogSize, cfg.NumBlocks, cfg.BlockSize)
		b.Run(name, func(b *testing.B) {
			b.Run("FreshPut", func(b *testing.B) {
				layout := benchPointLayout{name, func() benchUint64Points {
					return bufftree.NewBPTree[uint64, uint64](&cfg)
				}}
				benchmarkFreshPut(b, layout, n, keys, true)
			})
			b.Run("FreshIndependent", func(b *testing.B) {
				layout := benchPointLayout{name, func() benchUint64Points {
					return bufftree.NewBPTree[uint64, uint64](&cfg)
				}}
				benchmarkFreshPut(b, layout, n, independent, true)
			})
			b.Run("Scan100000", func(b *testing.B) {
				tr := bufftree.NewBPTree[uint64, uint64](&cfg)
				for i := 0; i < n; i++ {
					tr.Put(benchKey(i), uint64(i))
				}
				var sum, visited uint64
				visit := func(k, v uint64) bool { sum += v; visited++; return true }
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					o := ops[i%len(ops)]
					tr.Scan(o.key, o.length, visit)
				}
				b.StopTimer()
				benchSink = sum
				reportIteration(b, visited)
			})
		})
	}
}
