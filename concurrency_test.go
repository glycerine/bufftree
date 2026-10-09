package bufftree

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTreeConcurrentReadersAndWriter(t *testing.T) {
	for _, procs := range []int{1, 2, 4} {
		for _, small := range []bool{false, true} {
			t.Run(fmt.Sprintf("P%d/small=%t", procs, small), func(t *testing.T) {
				previous := runtime.GOMAXPROCS(procs)
				defer runtime.GOMAXPROCS(previous)
				var cfg *Config
				if small {
					cfg = &Config{Fanout: 3, LogSize: 3, NumBlocks: 3, BlockSize: 4}
				}
				tr := NewBPTree[int, [2]int](cfg)
				const n = 512
				for k := 0; k < n; k++ {
					tr.Put(k, [2]int{k, ^k})
				}
				var stop, bad atomic.Bool
				var operations atomic.Uint64
				var wg sync.WaitGroup
				for r := 0; r < 2*procs+1; r++ {
					wg.Add(1)
					go func(seed int) {
						defer wg.Done()
						for i := seed; !stop.Load(); i++ {
							k := (i * 17) % n
							if v, ok := tr.Get2(k); ok && v != [2]int{k, ^k} {
								bad.Store(true)
							}
							last, count := k-1, 0
							visit := func(key int, v [2]int) bool {
								if key <= last || v != [2]int{key, ^key} {
									bad.Store(true)
								}
								last = key
								count++
								return true
							}
							switch i % 5 {
							case 0:
								tr.Scan(k, 200, visit)
								if count > 200 {
									bad.Store(true)
								}
							case 1:
								tr.Range(k, k+200, visit)
								if last >= k+200 {
									bad.Store(true)
								}
							case 2:
								it := tr.IterFrom(k)
								for j := 0; j < 20 && it.Next(); j++ {
									visit(it.Key(), it.Value())
								}
							case 3:
								tr.MapRange(k, k+200, func(key int, v [2]int) bool {
									if key < k || key >= k+200 || v != [2]int{key, ^key} {
										bad.Store(true)
									}
									return true
								})
							case 4:
								if size := tr.Len(); size < 0 || size > n {
									bad.Store(true)
								}
							}
							operations.Add(1)
							runtime.Gosched()
						}
						// Iterator exhaustion/All may overlap the writer too.
					}(r)
				}
				waitSingleWriter(t, func() bool { return operations.Load() > 0 })
				done := make(chan struct{})
				go func() {
					for pass := 0; pass < 3; pass++ {
						for k := 0; k < n; k++ {
							tr.Del(k) // Empty the tree, merging and collapsing roots.
						}
						for k := 0; k < n; k++ {
							key := (k * 129) % n
							tr.Put(key, [2]int{key, ^key})
						}
						tr.Clear()
						for k := 0; k < n; k++ {
							tr.Put(k, [2]int{k, ^k})
						}
					}
					stop.Store(true)
					wg.Wait()
					close(done)
				}()
				awaitSingleWriter(t, done)
				if bad.Load() {
					t.Fatal("concurrent reader observed invalid data or scan order")
				}
				if size := tr.Len(); size != n {
					t.Fatalf("final length = %d, want %d", size, n)
				}
			})
		}
	}
}

func TestTreePreparedScansShareReadLock(t *testing.T) {
	tr := NewBPTree[int, int](nil)
	for k := 0; k < 1000; k++ {
		tr.Put(k, k)
	}
	for i := 0; i < 2; i++ {
		tr.Scan(0, 1000, func(int, int) bool { return true })
	}
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	done := make(chan struct{})
	go func() {
		count := 0
		tr.Scan(0, 1000, func(k, v int) bool { count++; return true })
		if count != 1000 || tr.Get(123) != 123 {
			t.Error("shared scan/get lost data")
		}
		close(done)
	}()
	awaitSingleWriter(t, done)
}

func TestTreeCallbacksUnlocked(t *testing.T) {
	tr := NewBPTree[int, int](nil)
	for i := 0; i < 500; i++ {
		tr.Put(i, i)
	}
	for _, name := range []string{"Scan", "Range", "All", "MapRange"} {
		t.Run(name, func(t *testing.T) {
			callback := func(k, v int) bool {
				if tr.mu.state.Load() != 0 {
					t.Fatal("tree lock held in callback")
				}
				tr.Del(k)
				tr.Put(k, v)
				tr.Get(k)
				tr.Len()
				tr.Scan(k, 1, func(int, int) bool { return true })
				return false
			}
			switch name {
			case "Scan":
				tr.Scan(0, 500, callback)
			case "Range":
				tr.Range(0, 500, callback)
			case "All":
				tr.All()(callback)
			case "MapRange":
				// Mutate only the current key; stop immediately afterward.
				tr.MapRange(0, 500, callback)
			}
		})
	}
	for _, scan := range []func(func(int, int) bool){
		func(f func(int, int) bool) { tr.Scan(0, 500, f) },
		func(f func(int, int) bool) { tr.MapRange(0, 500, f) },
	} {
		panicSingleWriter(t, func() { scan(func(int, int) bool { panic("visitor") }) })
		tr.Put(1000, 1000)
		if tr.Get(1000) != 1000 {
			t.Fatal("panic stranded a tree lock")
		}
	}
}

func TestTreeOverlappingWritersWait(t *testing.T) {
	for _, op := range []string{"Put", "Del", "Clear"} {
		t.Run(op, func(t *testing.T) {
			var tr Tree[int, int]
			tr.mu.RLock()
			done := make(chan struct{})
			go func() {
				tr.Put(1, 1)
				close(done)
			}()
			waitSingleWriter(t, func() bool { return tr.mu.state.Load() == singleWriterBit|1 })
			secondDone := make(chan struct{})
			go func() {
				switch op {
				case "Put":
					tr.Put(2, 2)
				case "Del":
					tr.Del(1)
				case "Clear":
					tr.Clear()
				}
				close(secondDone)
			}()
			tr.mu.RUnlock()
			awaitSingleWriter(t, done)
			awaitSingleWriter(t, secondDone)
			want := 0
			if op == "Put" {
				want = 2
			}
			if tr.Len() != want {
				t.Fatalf("length after serialized writers = %d, want %d", tr.Len(), want)
			}
		})
	}
}

func TestTreeMultipleWritersDeleteWhileIterating(t *testing.T) {
	for _, procs := range []int{1, 4} {
		t.Run(fmt.Sprintf("P%d", procs), func(t *testing.T) {
			previous := runtime.GOMAXPROCS(procs)
			defer runtime.GOMAXPROCS(previous)
			tr := NewBPTree[int, int](&Config{Fanout: 3, LogSize: 4, NumBlocks: 4, BlockSize: 5})
			const workers, keys = 4, 500
			var stop, bad atomic.Bool
			defer stop.Store(true)
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				for !stop.Load() {
					last := -1
					tr.Scan(0, workers*keys, func(k, v int) bool {
						if k <= last || k != v {
							bad.Store(true)
						}
						last = k
						return true
					})
					for k, v := range tr.All() {
						if k != v {
							bad.Store(true)
						}
						break
					}
					runtime.Gosched()
				}
			}()
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					lo, hi := w*keys, (w+1)*keys
					for k := lo; k < hi; k++ {
						tr.Put(k, k)
					}
					// Multiple goroutines may now delete from their callbacks
					// while other goroutines are still inserting or scanning.
					tr.Range(lo, hi, func(k, v int) bool {
						if k%2 == 0 {
							tr.Del(k)
						}
						return true
					})
				}(w)
			}
			done := make(chan struct{})
			go func() { wg.Wait(); stop.Store(true); close(done) }()
			awaitSingleWriter(t, done)
			awaitSingleWriter(t, readerDone)
			if bad.Load() || tr.Len() != workers*keys/2 {
				t.Fatal("concurrent insert/delete lost data or scan ordering", tr.Len())
			}
			for k := 0; k < workers*keys; k++ {
				v, ok := tr.Get2(k)
				if ok != (k%2 == 1) || (ok && v != k) {
					t.Fatal("incorrect final membership", k, v, ok)
				}
			}
		})
	}
}
