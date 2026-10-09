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
				return bufftree.NewBPTree[uint64, uint64](&cfg)
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
