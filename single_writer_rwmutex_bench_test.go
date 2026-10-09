package bufftree

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func BenchmarkSingleWriterRWMutex(b *testing.B) {
	b.Run("Read", func(b *testing.B) {
		var m SingleWriterRWMutex
		b.ReportAllocs()
		for b.Loop() {
			m.RLock()
			m.RUnlock()
		}
	})
	b.Run("Write", func(b *testing.B) {
		var m SingleWriterRWMutex
		b.ReportAllocs()
		for b.Loop() {
			m.Lock()
			m.Unlock()
		}
	})
	b.Run("Downgrade", func(b *testing.B) {
		var m SingleWriterRWMutex
		b.ReportAllocs()
		for b.Loop() {
			m.Lock()
			m.Downgrade()
			m.RUnlock()
		}
	})
	b.Run("ParallelRead", func(b *testing.B) {
		var m SingleWriterRWMutex
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				m.RLock()
				m.RUnlock()
			}
		})
	})
}

func BenchmarkSyncRWMutex(b *testing.B) {
	b.Run("Read", func(b *testing.B) {
		var m sync.RWMutex
		b.ReportAllocs()
		for b.Loop() {
			m.RLock()
			m.RUnlock()
		}
	})
	b.Run("Write", func(b *testing.B) {
		var m sync.RWMutex
		b.ReportAllocs()
		for b.Loop() {
			m.Lock()
			m.Unlock()
		}
	})
	b.Run("ParallelRead", func(b *testing.B) {
		var m sync.RWMutex
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				m.RLock()
				m.RUnlock()
			}
		})
	})
}

type singleWriterBenchmarkLock interface {
	Lock()
	Unlock()
	RLock()
	RUnlock()
}

// Measures writer throughput with P background readers, each summing 64 words.
// Reader operations/write is reported too: a faster writer need not imply
// higher total throughput or reader fairness. Both locks pay interface dispatch.
func benchmarkSingleWriterMixed(b *testing.B, m singleWriterBenchmarkLock) {
	var data [64]uint64
	var stop atomic.Bool
	var readerOps atomic.Uint64
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	start := make(chan struct{})
	n := runtime.GOMAXPROCS(0)
	ready.Add(n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			var count, sum uint64
			for !stop.Load() {
				m.RLock()
				for _, v := range data {
					sum += v
				}
				m.RUnlock()
				count++
			}
			runtime.KeepAlive(sum)
			readerOps.Add(count)
		}()
	}
	ready.Wait()
	b.ReportAllocs()
	b.ResetTimer()
	close(start)
	for i := 0; i < b.N; i++ {
		m.Lock()
		data[i%len(data)]++
		m.Unlock()
	}
	b.StopTimer()
	stop.Store(true)
	wg.Wait()
	b.ReportMetric(float64(readerOps.Load())/float64(b.N), "reads/write")
}

func BenchmarkSingleWriterMixed(b *testing.B) {
	b.Run("Spin", func(b *testing.B) {
		benchmarkSingleWriterMixed(b, new(SingleWriterRWMutex))
	})
	b.Run("Sync", func(b *testing.B) {
		benchmarkSingleWriterMixed(b, new(sync.RWMutex))
	})
}
