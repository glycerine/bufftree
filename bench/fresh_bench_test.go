package bench

import (
	"context"
	"runtime/pprof"
	"testing"
)

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
		inserting = pprof.WithLabels(background, pprof.Labels("phase", "fresh-put"))
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
