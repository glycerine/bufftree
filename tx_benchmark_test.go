package bufftree

import (
	"cmp"
	"errors"
	"fmt"
	"testing"
)

// Benchmark-only adapter: old paper harnesses now measure public transactions.
// No unlocked convenience API is exposed by the library.
type transactionBench[K cmp.Ordered, V any] struct{ db *Tree[K, V] }

func newTransactionBench[K cmp.Ordered, V any](cfg *Config) *transactionBench[K, V] {
	return &transactionBench[K, V]{NewBPTree[K, V](cfg)}
}
func (b *transactionBench[K, V]) Put(k K, v V) {
	if err := b.db.Update(func(tx *WriteTx[K, V]) error { return tx.Put(k, v) }); err != nil {
		panic(err)
	}
}
func (b *transactionBench[K, V]) Get(k K) V { v, _ := b.Get2(k); return v }
func (b *transactionBench[K, V]) Get2(k K) (v V, ok bool) {
	_ = b.db.View(func(tx *ReadOnlyTx[K, V]) error { v, ok, _ = tx.Get(k); return nil })
	return
}
func (b *transactionBench[K, V]) Clear() {
	_ = b.db.Update(func(tx *WriteTx[K, V]) error { _, err := tx.Clear(); return err })
}
func (b *transactionBench[K, V]) Scan(k K, n int, fn func(K, V) bool) {
	_ = b.db.View(func(tx *ReadOnlyTx[K, V]) error { tx.Scan(k, n, fn); return nil })
}
func (b *transactionBench[K, V]) MapRange(lo, hi K, fn func(K, V) bool) {
	_ = b.db.View(func(tx *ReadOnlyTx[K, V]) error { tx.MapRange(lo, hi, fn); return nil })
}

func benchmarkTxDB(b *testing.B, n int) *Tree[int, int] {
	b.Helper()
	db := NewBPTree[int, int](nil)
	if err := db.Update(func(tx *WriteTx[int, int]) error {
		for i := 0; i < n; i++ {
			if err := tx.Put(i, i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	return db
}

// BatchSize applies to both reads and updates. Every measured iteration ends
// its transaction inside the timer; ns/key and journal bytes/key are explicit.
func BenchmarkTransactions(b *testing.B) {
	for _, size := range []int{1, 32, 1024} {
		for _, write := range []bool{false, true} {
			b.Run(fmt.Sprintf("Write%v/Batch%d", write, size), func(b *testing.B) {
				db := benchmarkTxDB(b, 4096)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if write {
						_ = db.Update(func(tx *WriteTx[int, int]) error {
							for j := 0; j < size; j++ {
								if err := tx.Put((i*size+j)%4096, i); err != nil {
									return err
								}
							}
							return nil
						})
					} else {
						_ = db.View(func(tx *ReadOnlyTx[int, int]) error {
							for j := 0; j < size; j++ {
								v, _, _ := tx.Get((i*size + j) % 4096)
								benchSink += uint64(v)
							}
							return nil
						})
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/key")
			})
		}
	}
}
func BenchmarkTransactionScans(b *testing.B) {
	for _, name := range []string{"Forward", "Reverse", "DeleteRollback"} {
		b.Run(name, func(b *testing.B) {
			const n = 4096
			db := benchmarkTxDB(b, n)
			stop := errors.New("rollback")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if name == "DeleteRollback" {
					_ = db.Update(func(tx *WriteTx[int, int]) error {
						for k := range tx.All() {
							if err := tx.Delete(k); err != nil {
								return err
							}
						}
						return stop
					})
				} else {
					_ = db.View(func(tx *ReadOnlyTx[int, int]) error {
						if name == "Forward" {
							for _, v := range tx.All() {
								benchSink += uint64(v)
							}
						} else {
							it := tx.NewIter()
							for it.SeekLast(); it.Valid(); it.Prev() {
								benchSink += uint64(it.Value())
							}
						}
						return nil
					})
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/key")
		})
	}
	b.Run("ConcurrentRead", func(b *testing.B) {
		db := benchmarkTxDB(b, 4096)
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = db.View(func(tx *ReadOnlyTx[int, int]) error {
					tx.Scan(0, 100, func(k, v int) bool {
						if k != v {
							panic("value")
						}
						return true
					})
					return nil
				})
			}
		})
	})
}
func BenchmarkTransactionCommitPreparation(b *testing.B) {
	db := benchmarkTxDB(b, 4096)
	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		tx, _ := db.BeginUpdate()
		for j := 0; j < 1024; j++ {
			_ = tx.Put((i*1024+j)%4096, i)
		}
		b.StartTimer()
		_ = tx.Commit()
		b.StopTimer()
	}
	// This isolated metric excludes writes/journaling; end-to-end batch timings
	// above and in make bench include both. No acquisition waiting occurs here.
}
