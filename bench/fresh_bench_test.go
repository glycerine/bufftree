package bench

import (
	"context"
	"fmt"
	"runtime/pprof"
	"testing"

	"github.com/glycerine/bufftree"
)

// Sweep the leaf geometry separately from internal fanout, keeping the
// README's fresh-key trace and public Put contract unchanged.
func BenchmarkFreshPutConfig(b *testing.B) {
	n := benchLoadSize()
	keys := make([]uint64, n)
	for i := range keys {
		keys[i] = benchKey(i) | 1
	}
	configs := []bufftree.Config{}
	for _, fanout := range []int{16, 32, 64, 128} {
		configs = append(configs, bufftree.Config{Fanout: fanout, LogSize: 32, NumBlocks: 32, BlockSize: 32})
	}
	for _, pair := range [][2]int{{8, 8}, {16, 16}, {16, 32}, {32, 16}, {32, 32}, {32, 64}, {64, 16}, {64, 64}} {
		for _, logSize := range []int{8, 16, 32, 64} {
			if pair == [2]int{32, 32} && logSize == 32 {
				continue // already covered by the fanout sweep
			}
			configs = append(configs, bufftree.Config{Fanout: 64, LogSize: logSize, NumBlocks: pair[0], BlockSize: pair[1]})
		}
	}
	for _, cfg := range configs {
		name := fmt.Sprintf("f%d-l%d-h%d-b%d", cfg.Fanout, cfg.LogSize, cfg.NumBlocks, cfg.BlockSize)
		b.Run(name, func(b *testing.B) {
			layout := benchPointLayout{name, func() benchUint64Points {
				return newBenchTree(&cfg)
			}}
			benchmarkFreshPut(b, layout, n, keys, false)
		})
	}
}

// Independent random keys remove the README trace's adjacency to loaded
// keys. Ascending and descending growth exercise redistribution extremes.
func BenchmarkFreshPutDistribution(b *testing.B) {
	n := benchLoadSize()
	for _, distribution := range []string{"Independent", "Ascending", "Descending"} {
		keys := make([]uint64, n)
		for i := range keys {
			switch distribution {
			case "Independent":
				keys[i] = benchKey(n + i)
			case "Ascending":
				keys[i] = uint64(i)
			case "Descending":
				keys[i] = ^uint64(i)
			}
		}
		for _, layout := range comparisonPointLayouts(n) {
			b.Run(distribution+"/"+layout.name, func(b *testing.B) {
				if distribution == "Independent" {
					benchmarkFreshPut(b, layout, n, keys, false)
					return
				}
				idx := layout.make()
				inserted := 0
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if inserted == n {
						b.StopTimer()
						idx = layout.make()
						inserted = 0
						b.StartTimer()
					}
					idx.Put(keys[inserted], uint64(i))
					inserted++
				}
				b.StopTimer()
			})
		}
	}
}

// BenchmarkFreshPut shares the README's exact insertion loop and labels its
// timed phase for CPU profiles. Filter pprof with -tagfocus=phase=fresh-put to
// exclude fixture loading, which Go's benchmark timer does not profile-filter.
func BenchmarkFreshPut(b *testing.B) {
	n := benchLoadSize()
	keys := make([]uint64, n)
	for i := range keys {
		keys[i] = benchKey(i) | 1
	}
	for _, layout := range comparisonPointLayouts(n) {
		b.Run(layout.name, func(b *testing.B) {
			benchmarkFreshPut(b, layout, n, keys, true)
		})
	}
}

// Include exact counting in the timed workload, exposing the cost of calling
// Len after every write versus amortizing reconciliation across a batch.
func BenchmarkFreshPutWithLen(b *testing.B) {
	n := benchLoadSize()
	keys := make([]uint64, n)
	for i := range keys {
		keys[i] = benchKey(i) | 1
	}
	for _, interval := range []int{1, 32, n} {
		for _, layout := range comparisonPointLayouts(n) {
			b.Run(fmt.Sprintf("Every%d/%s", interval, layout.name), func(b *testing.B) {
				idx := loadComparisonPoints(layout, n)
				counted := idx.(interface{ Len() int })
				counted.Len() // fixture counting is outside the timer
				inserted, pending := 0, 0
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if inserted == n {
						if pending != 0 {
							benchSink = uint64(counted.Len())
							pending = 0
						}
						b.StopTimer()
						idx = loadComparisonPoints(layout, n)
						counted = idx.(interface{ Len() int })
						counted.Len()
						inserted = 0
						b.StartTimer()
					}
					idx.Put(keys[inserted], uint64(i))
					inserted++
					pending++
					if pending == interval {
						benchSink = uint64(counted.Len())
						pending = 0
					}
				}
				if pending != 0 {
					benchSink = uint64(counted.Len())
				}
				b.StopTimer()
			})
		}
	}
}

func benchmarkFreshPut(b *testing.B, layout benchPointLayout, n int, keys []uint64, profile bool) {
	idx := loadComparisonPoints(layout, n)
	inserted := 0
	background := context.Background()
	var inserting context.Context
	if profile {
		inserting = pprof.WithLabels(background, pprof.Labels("phase", "fresh-put", "container", layout.name))
		pprof.SetGoroutineLabels(inserting)
		defer pprof.SetGoroutineLabels(background)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			if profile {
				pprof.SetGoroutineLabels(background)
			}
			idx = loadComparisonPoints(layout, n)
			inserted = 0
			if profile {
				pprof.SetGoroutineLabels(inserting)
			}
			b.StartTimer()
		}
		// Unique odd keys are absent from the initial even-key load. Include
		// insertion/growth from n to 2*n records; reload outside the timer.
		idx.Put(keys[inserted], uint64(i))
		inserted++
	}
	b.StopTimer()
}

// Each timed operation is a complete batch, including BP-tree Commit. The
// metric divides by actual inserted keys, including partial final batches.
const freshPutBatchSize = 1024

func putComparisonBatch(idx benchUint64Points, keys []uint64, offset uint64) {
	if batch, ok := idx.(interface{ PutBatch([]uint64, uint64) }); ok {
		batch.PutBatch(keys, offset)
		return
	}
	for i, k := range keys {
		idx.Put(k, offset+uint64(i))
	}
}
func benchmarkFreshPutBatch(b *testing.B, layout benchPointLayout, n int, keys []uint64) {
	idx := loadComparisonPoints(layout, n)
	background := context.Background()
	inserting := pprof.WithLabels(background, pprof.Labels("phase", "batch-put", "container", layout.name))
	pprof.SetGoroutineLabels(inserting)
	defer pprof.SetGoroutineLabels(background)
	inserted := 0
	var total uint64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			pprof.SetGoroutineLabels(background)
			idx = loadComparisonPoints(layout, n)
			inserted = 0
			pprof.SetGoroutineLabels(inserting)
			b.StartTimer()
		}
		count := min(freshPutBatchSize, n-inserted)
		putComparisonBatch(idx, keys[inserted:inserted+count], uint64(inserted))
		inserted += count
		total += uint64(count)
	}
	b.StopTimer()
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(total), "put_ns/key")
	b.ReportMetric(float64(total)/float64(b.N), "keys/op")
}
func TestComparisonBatchPuts(t *testing.T) {
	for _, n := range []int{17, 2*freshPutBatchSize + 37} {
		for _, layout := range comparisonPointLayouts(n) {
			idx := loadComparisonPoints(layout, n)
			for offset := 0; offset < n; {
				count := min(freshPutBatchSize, n-offset)
				keys := make([]uint64, count)
				for i := range keys {
					keys[i] = benchKey(offset+i) | 1
				}
				putComparisonBatch(idx, keys, uint64(offset))
				offset += count
			}
			if idx.(interface{ Len() int }).Len() != 2*n {
				t.Fatal(layout.name, "batch length")
			}
			for i := 0; i < n; i++ {
				if idx.Get(benchKey(i)) != uint64(i) || idx.Get(benchKey(i)|1) != uint64(i) {
					t.Fatal(layout.name, "batch value", i)
				}
			}
		}
	}
}
