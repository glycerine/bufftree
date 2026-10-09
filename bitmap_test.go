package bufftree

import (
	"math"
	"math/rand"
	"testing"
	"unsafe"
)

func TestCompactRecordAndBitmap(t *testing.T) {
	if n := unsafe.Sizeof(entry[uint64, uint64]{}); n != 16 {
		t.Fatalf("uint64 key/value record occupies %d bytes", n)
	}
	p := newBPA[uint64, uint64](Config{})
	if len(p.dead) != 2 || len(p.data) != 1162 {
		t.Fatal("default leaf must use 1,162 compact records and two bitmap words")
	}
}

func TestBufferedOverwritesDoNotSplit(t *testing.T) {
	cfg := Config{Fanout: 3, LogSize: 7, NumBlocks: 3, BlockSize: 5}
	tr := NewBPTree[int, int](&cfg)
	for i := 0; i < 15; i++ {
		tr.Put(i, i)
	}
	for i := 0; i < 3000; i++ {
		tr.Put(i%15, -i)
	}
	if tr.root.leaf == nil || tr.Len() != 15 {
		t.Fatal("duplicate writes caused a split or changed Len")
	}
	tr.Put(15, 15)
	if tr.root.leaf != nil || tr.Len() != 16 {
		t.Fatal("fresh write failed to split a full leaf")
	}
}

func TestLogBitmapSortAcrossWords(t *testing.T) {
	for _, logSize := range []int{3, 32, 63, 64, 65, 129} {
		p := newBPA[int, int](Config{LogSize: logSize, NumBlocks: 16, BlockSize: 32})
		model := map[int]int{}
		for i := logSize - 2; i >= 0; i-- {
			p.set(i, i+100)
			model[i] = i + 100
		}
		for i := 0; i < logSize-1; i += 3 {
			p.del(i)
			delete(model, i)
		}
		checkBPA(t, p, model) // sorts the log while carrying tombstone bits
		for i := 0; i < logSize-1; i += 6 {
			p.set(i, -i)
			model[i] = -i
		}
		p.flush()
		checkBPA(t, p, model)
		for _, word := range p.dead {
			if word != 0 {
				t.Fatal("log flush retained stale bitmap bits")
			}
		}
	}
}

func TestLogClearPreservesHeaderBitmap(t *testing.T) {
	for _, logSize := range []int{3, 32, 63, 64, 65, 129} {
		p := newBPA[int, int](Config{LogSize: logSize, NumBlocks: 4, BlockSize: 128})
		es := make([]entry[int, int], 256)
		for i := range es {
			es[i] = entry[int, int]{key: i, value: i}
		}
		p.load(es)
		p.del(0)
		for i := 1; i <= logSize; i++ {
			p.set(i, -i)
		}
		p.flush()
		if !p.isDead(p.cfg.LogSize) {
			t.Fatal("clearing log bits erased the header tombstone")
		}
		if _, ok := p.get(0); ok {
			t.Fatal("deleted header became visible")
		}
		if p.resolveSize() != 255 {
			t.Fatal("buffered overwrites changed size")
		}
		p.set(0, 123)
		if p.resolveSize() != 256 {
			t.Fatal("header resurrection count")
		}
		p.flush()
		if p.isDead(p.cfg.LogSize) {
			t.Fatal("header resurrection retained tombstone")
		}
		if v, ok := p.get(0); !ok || v != 123 {
			t.Fatal("header resurrection value")
		}
	}
}

func TestBufferedMembershipAndLen(t *testing.T) {
	for _, cfg := range []Config{tinyConfig, {}, {Fanout: 5, LogSize: 65, NumBlocks: 3, BlockSize: 17}} {
		tr := NewBPTree[int, int](&cfg)
		model := map[int]int{}
		rng := rand.New(rand.NewSource(994))
		for step := 0; step < 20000; step++ {
			k := rng.Intn(1500)
			if rng.Intn(5) == 0 {
				tr.Del(k)
				delete(model, k)
			} else {
				tr.Put(k, step)
				model[k] = step
			}
			if step%113 == 0 {
				if n := tr.Len(); n != len(model) {
					t.Fatalf("Len=%d, want %d", n, len(model))
				}
				if tr.pending != nil {
					t.Fatal("Len did not reconcile pending leaves")
				}
				if tr.Len() != len(model) {
					t.Fatal("repeated Len")
				}
			}
		}
		checkTree(t, tr, model)
		// Del must retire dirty merged leaves even when Len is never called.
		for k := range model {
			tr.Del(k)
		}
		if tr.root != nil || tr.pending != nil || tr.length != 0 {
			t.Fatal("empty tree retained dirty leaves or counts")
		}
		tr.Put(1, 2)
		tr.Clear()
		if tr.Len() != 0 || tr.pending != nil {
			t.Fatal("Clear retained pending membership")
		}
		tr.Put(3, 4)
		if tr.Len() != 1 {
			t.Fatal("reuse after Clear")
		}
	}
}

func TestLenDoesNotMoveIteratorRecords(t *testing.T) {
	tr := NewBPTree[int, int](nil)
	for i := 200; i >= 0; i-- {
		tr.Put(i, i)
	}
	for i := 0; i <= 200; i += 3 {
		tr.Put(i, -i)
	}
	it := tr.Iter()
	for i := 0; i <= 200; i++ {
		if !it.Next() || it.Key() != i {
			t.Fatal("Len invalidated iterator position", i)
		}
		if tr.Len() != 201 {
			t.Fatal("incorrect Len during iteration")
		}
	}
	if it.Next() {
		t.Fatal("duplicate iterator result")
	}
	for i := 0; i <= 200; i += 5 {
		tr.Put(i, 1000+i)
	}
	count := 0
	tr.Scan(0, 1000, func(k, v int) bool { count++; return tr.Len() == 201 })
	if count != 201 {
		t.Fatal("Len invalidated scan position")
	}
}

func TestPartiallyCountedLogSort(t *testing.T) {
	tr := NewBPTree[int, int](nil)
	for i := 0; i < 256; i++ {
		tr.Put(i*2, i)
	}
	// Start with an empty log independently of the configured log size.
	tr.root.leaf.flush()
	if tr.Len() != 256 {
		t.Fatal("initial count")
	}
	// Count some buffered overwrites, then append both duplicates and fresh
	// keys before a traversal reorders the partially counted log prefix.
	tr.Put(400, -1)
	tr.Put(200, -2)
	if tr.Len() != 256 {
		t.Fatal("overwrite count")
	}
	tr.Put(100, -3)
	tr.Put(301, -4)
	tr.Put(201, -5)
	if p := tr.root.leaf; p == nil || p.countedLog != 2 || p.logN != 5 {
		t.Fatal("fixture must contain a partially counted log")
	}
	count := 0
	for range tr.All() {
		count++
	}
	if count != 258 || tr.Len() != count {
		t.Fatal("sorting changed deferred membership", count, tr.Len())
	}
	tr.Del(200)
	tr.Put(200, 999)
	if tr.Len() != 258 || tr.Get(200) != 999 {
		t.Fatal("counted log resurrection")
	}
}

func TestDeferredLenDoesNotAllocate(t *testing.T) {
	tr := NewBPTree[int, int](nil)
	for i := 0; i < 2000; i++ {
		tr.Put(i, i)
	}
	tr.Len()
	i := 0
	allocs := testing.AllocsPerRun(1000, func() {
		tr.Put(i%2000, -i)
		i++
		if tr.Len() != 2000 {
			t.Fatal("overwrite changed count")
		}
	})
	if allocs != 0 {
		t.Fatalf("Put/Len on existing keys allocated %g times", allocs)
	}
}

func TestDeferredNaNAndPointerTombstones(t *testing.T) {
	tr := NewBPTree[float64, *int](nil)
	a, b := 11, 22
	for i := 100; i >= 0; i-- {
		tr.Put(float64(i), &a)
	}
	tr.Put(math.NaN(), &a)
	tr.Put(math.Float64frombits(0xfff8000000000001), &b)
	tr.Del(99)
	tr.Del(math.NaN())
	if tr.Len() != 100 {
		t.Fatal("NaN equivalence or deletion count")
	}
	tr.Put(math.NaN(), &b)
	if tr.Len() != 101 || tr.Get(math.NaN()) != &b {
		t.Fatal("NaN resurrection")
	}
	for n := tr.firstLeaf(); n != nil; n = n.next {
		p := n.leaf
		for j := 0; j < p.cfg.LogSize+p.headerN; j++ {
			if p.isDead(j) && p.data[j].value != nil {
				t.Fatal("bitmap tombstone retains value pointer")
			}
		}
	}
}
