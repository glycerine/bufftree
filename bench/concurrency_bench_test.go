package bench

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/glycerine/bufftree"
)

// Tidwall's Map is not synchronized. These benchmarks give it an external
// RWMutex; the ordinary single-thread comparisons still use the bare Map.
// Callbacks in this adapter are benchmark-local and never reenter the map.
type benchLockedTidwall struct {
	mu sync.RWMutex
	tr benchTidwallMap
}

func (t *benchLockedTidwall) Get(k uint64) uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.tr.Get(k)
}

func (t *benchLockedTidwall) Put(k, v uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tr.Put(k, v)
}

func (t *benchLockedTidwall) Scan(k uint64, length int, visit func(uint64, uint64) bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	t.tr.Scan(k, length, visit)
}

type benchConcurrentIndex interface {
	benchUint64Points
	Scan(uint64, int, func(uint64, uint64) bool)
}

func concurrentBenchmarkCases() []struct {
	name string
	make func() benchConcurrentIndex
} {
	return []struct {
		name string
		make func() benchConcurrentIndex
	}{
		{"Tree", func() benchConcurrentIndex { return bufftree.NewBPTree[uint64, uint64](nil) }},
		{"TidwallRWMutex", func() benchConcurrentIndex { return new(benchLockedTidwall) }},
	}
}

func BenchmarkConcurrentRead(b *testing.B) {
	n := benchLoadSize()
	ops := benchTrace(n, 8192, 1000)
	for _, bc := range concurrentBenchmarkCases() {
		for _, scan := range []bool{false, true} {
			name := "Get"
			if scan {
				name = "Scan1000"
			}
			b.Run(bc.name+"/"+name, func(b *testing.B) {
				tr := bc.make()
				for i := 0; i < n; i++ {
					tr.Put(benchKey(i), uint64(i))
				}
				for i := 0; i < 2; i++ {
					tr.Scan(0, n, func(uint64, uint64) bool { return true })
				}
				var total atomic.Uint64
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					var sum, count uint64
					visit := func(k, v uint64) bool { sum += v; count++; return true }
					for i := 0; pb.Next(); i++ {
						o := ops[i%len(ops)]
						if scan {
							tr.Scan(o.key, 1000, visit)
						} else {
							sum += tr.Get(o.key)
							count++
						}
					}
					runtime.KeepAlive(sum)
					total.Add(count)
				})
				b.StopTimer()
				if scan {
					reportIteration(b, total.Load())
				}
			})
		}
	}
}

// Unlike the single-writer mixed workload, every worker here is a competing
// writer. No caller-side serialization is applied to either implementation.
func BenchmarkConcurrentWrite(b *testing.B) {
	n := benchLoadSize()
	ops := benchTrace(n, 8192, 1)
	for _, bc := range concurrentBenchmarkCases() {
		b.Run(bc.name, func(b *testing.B) {
			tr := bc.make()
			for i := 0; i < n; i++ {
				tr.Put(benchKey(i), uint64(i))
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for i := 0; pb.Next(); i++ {
					tr.Put(ops[i%len(ops)].key, uint64(i))
				}
			})
		})
	}
}

// Exactly one writer updates existing keys while P readers perform lookups.
// Report reader progress as well as ns/write: spin-first locking can favor
// writers, so writer throughput by itself is not a balanced-throughput result.
func BenchmarkConcurrentUpdate(b *testing.B) {
	n := benchLoadSize()
	ops := benchTrace(n, 8192, 1)
	for _, bc := range concurrentBenchmarkCases() {
		b.Run(bc.name, func(b *testing.B) {
			tr := bc.make()
			for i := 0; i < n; i++ {
				tr.Put(benchKey(i), uint64(i))
			}
			var stop atomic.Bool
			var reads atomic.Uint64
			var wg, ready sync.WaitGroup
			start := make(chan struct{})
			readers := runtime.GOMAXPROCS(0)
			ready.Add(readers)
			for r := 0; r < readers; r++ {
				wg.Add(1)
				go func(seed int) {
					defer wg.Done()
					ready.Done()
					<-start
					var sum, count uint64
					for i := seed; !stop.Load(); i++ {
						sum += tr.Get(ops[i%len(ops)].key)
						count++
					}
					runtime.KeepAlive(sum)
					reads.Add(count)
				}(r)
			}
			ready.Wait()
			b.ReportAllocs()
			b.ResetTimer()
			close(start)
			for i := 0; i < b.N; i++ {
				tr.Put(ops[i%len(ops)].key, uint64(i))
			}
			b.StopTimer()
			stop.Store(true)
			wg.Wait()
			b.ReportMetric(float64(reads.Load())/float64(b.N), "reads/write")
		})
	}
}
