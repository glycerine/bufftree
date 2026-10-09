package bench

import (
	"fmt"
	"testing"

	"github.com/glycerine/bufftree"
)

func scanComparisonLayouts() []benchScanLayout {
	return []benchScanLayout{
		{"Tree", func() benchOrderedScan { return bufftree.NewBPTree[uint64, uint64](nil) }},
		{"Tidwall", func() benchOrderedScan { return &benchTidwallMap{} }},
	}
}

// Include lazy sorting/scan preparation in the first query after loading.
// Fresh fixtures are constructed outside the timer for both containers.
func BenchmarkScanFirst(b *testing.B) {
	n := benchLoadSize()
	for _, layout := range scanComparisonLayouts() {
		for _, length := range []int{100, 10000, n} {
			b.Run(fmt.Sprintf("%s/Scan%d", layout.name, length), func(b *testing.B) {
				var sum, visited uint64
				visit := func(k, v uint64) bool { sum += v; visited++; return true }
				b.ReportAllocs()
				b.ResetTimer()
				b.StopTimer()
				for i := 0; i < b.N; i++ {
					tr := layout.make()
					for j := 0; j < n; j++ {
						tr.Put(benchKey(j), uint64(j))
					}
					b.StartTimer()
					tr.Scan(0, length, visit)
					b.StopTimer()
				}
				benchSink = sum
				reportIteration(b, visited)
			})
		}
	}
}

// Time both writes and scans, so settling the log cannot hide costs in setup.
func BenchmarkScanMixed(b *testing.B) {
	n := benchLoadSize()
	for _, layout := range scanComparisonLayouts() {
		for _, length := range []int{100, 10000, n} {
			b.Run(fmt.Sprintf("%s/Write32Scan%d", layout.name, length), func(b *testing.B) {
				tr := layout.make()
				for j := 0; j < n; j++ {
					tr.Put(benchKey(j), uint64(j))
				}
				var sum, visited uint64
				visit := func(k, v uint64) bool { sum += v; visited++; return true }
				tr.Scan(0, n, visit)
				sum, visited = 0, 0
				ops := benchTrace(n, 8192, length)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for j := 0; j < 32; j++ {
						tr.Put(ops[(i*32+j)%len(ops)].key, uint64(i+j))
					}
					o := ops[i%len(ops)]
					tr.Scan(o.key, o.length, visit)
				}
				b.StopTimer()
				benchSink = sum
				reportIteration(b, visited)
			})
		}
	}
}
