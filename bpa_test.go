package bufftree

import (
	"cmp"
	"math/rand"
	"slices"
	"testing"
)

func TestBPALogFlushAndRedistribute(t *testing.T) {
	p := newBPA[int, int](Config{LogSize: 4, NumBlocks: 4, BlockSize: 4})
	model := map[int]int{}
	for _, k := range []int{7, 15, 19, 89, 13, 8, 17, 32, 50, 93, 95, 25, 22, 27} {
		p.set(k, k*10)
		model[k] = k * 10
		checkBPA(t, p, model)
	}
	// A newer value shadows its copy in the header or block.
	p.set(19, 999)
	model[19] = 999
	checkBPA(t, p, model)
	p.del(19)
	delete(model, 19)
	checkBPA(t, p, model)
	p.set(19, 123)
	model[19] = 123
	p.flush()
	checkBPA(t, p, model)
}

func TestBPARandom(t *testing.T) {
	for _, cfg := range []Config{{LogSize: 2, NumBlocks: 2, BlockSize: 2}, {LogSize: 4, NumBlocks: 4, BlockSize: 4}, {LogSize: 3, NumBlocks: 5, BlockSize: 7}} {
		p := newBPA[int, int](cfg)
		model := map[int]int{}
		rng := rand.New(rand.NewSource(123))
		for i := 0; i < 3000; i++ {
			k := rng.Intn(p.capacity())
			if rng.Intn(3) == 0 {
				p.del(k)
				delete(model, k)
			} else {
				p.set(k, i)
				model[k] = i
			}
			checkBPA(t, p, model)
		}
	}
}

func checkBPA(t *testing.T, p *bpa[int, int], model map[int]int) {
	t.Helper()
	if p.resolveSize() != len(model) {
		t.Fatalf("size %d != %d", p.size, len(model))
	}
	if p.logN >= p.cfg.LogSize {
		t.Fatal("log has no spare slot")
	}
	for i, n := range p.counts {
		if n >= p.cfg.BlockSize {
			t.Fatalf("block %d has no spare slot", i)
		}
		if i > 0 && cmp.Compare(p.header(i-1).key, p.header(i).key) >= 0 && i < p.headerN {
			t.Fatal("unordered headers")
		}
		if i < p.headerN {
			mirror := p.data[p.cfg.LogSize+p.cfg.NumBlocks+i*p.cfg.BlockSize]
			if mirror != *p.header(i) {
				t.Fatal("stale scan header mirror", i)
			}
			for _, e := range p.block(i) {
				if cmp.Compare(e.key, p.header(i).key) <= 0 || (i+1 < p.headerN && cmp.Compare(e.key, p.header(i+1).key) >= 0) {
					t.Fatal("block outside header bounds")
				}
			}
		}
	}
	for k, want := range model {
		if v, ok := p.get(k); !ok || v != want {
			t.Fatalf("get %d = %d,%v want %d", k, v, ok, want)
		}
	}
	cur := p.cursor(0, false, false)
	var keys []int
	for e, ok := cur.next(); ok; e, ok = cur.next() {
		keys = append(keys, e.key)
		if want, found := model[e.key]; !found || e.value != want {
			t.Fatalf("stale cursor entry %+v", e)
		}
	}
	want := make([]int, 0, len(model))
	for k := range model {
		want = append(want, k)
	}
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("keys %v != %v", keys, want)
	}
}

// Leave the tree unread between batches: inspecting it after every operation
// sorts the blocks and can conceal bugs in fresh flushes and redistribution.
func TestFreshPutMixedBatches(t *testing.T) {
	for _, cfg := range []Config{
		tinyConfig, {},
		{Fanout: 5, LogSize: 16, NumBlocks: 3, BlockSize: 5},
		{Fanout: 5, LogSize: 73, NumBlocks: 67, BlockSize: 71},
	} {
		tr := newTreeCore[int, int](&cfg)
		model := map[int]int{}
		rng := rand.New(rand.NewSource(891))
		for i := 0; i < 30000; i++ {
			k := rng.Intn(12000)
			if rng.Intn(4) == 0 {
				tr.Del(k)
				if _, found := tr.Get2(k); found {
					t.Fatalf("delete retained %d", k)
				}
				delete(model, k)
			} else {
				tr.Put(k, i)
				if got, found := tr.Get2(k); !found || got != i {
					t.Fatalf("put lost %d", k)
				}
				model[k] = i
			}
			if i%2000 == 0 {
				checkTree(t, tr, model)
			}
		}
		checkTree(t, tr, model)
	}
}

func TestPutResurrectsFullLeafTombstone(t *testing.T) {
	cfg := Config{LogSize: 2, NumBlocks: 2, BlockSize: 2}
	tr := newTreeCore[int, int](&cfg)
	for _, k := range []int{10, 20, 30, 40} {
		tr.Put(k, k)
	}
	tr.Del(10)
	tr.root.leaf.flush() // keep the deleted minimum as a header marker
	tr.Put(35, 35)       // fill the leaf without removing that marker
	if tr.root.leaf.size != tr.root.leaf.capacity() || tr.root.leaf.location(10) < 0 {
		t.Fatal("expected a full leaf retaining the deleted header")
	}
	tr.Put(10, 100)
	checkTree(t, tr, map[int]int{10: 100, 20: 20, 30: 30, 35: 35, 40: 40})
}

func TestDistinctFlushRebuildAfterPartialCopy(t *testing.T) {
	p := newBPA[int, int](Config{LogSize: 4, NumBlocks: 3, BlockSize: 4})
	var es []entry[int, int]
	model := map[int]int{}
	for _, k := range []int{0, 10, 20, 30, 40, 50} {
		es = append(es, entry[int, int]{key: k, value: k})
		model[k] = k
	}
	p.load(es)
	// The first block accepts 5; the last block overflows. Redistribution
	// must not count or emit the already-copied 5 twice.
	for _, k := range []int{5, 41, 42, 43} {
		p.size++
		p.appendLog(entry[int, int]{key: k, value: k})
		model[k] = k
	}
	checkBPA(t, p, model)
}

func TestSplitScratchDoesNotRetainValues(t *testing.T) {
	tr := newTreeCore[int, *int](&tinyConfig)
	for i := 0; i < 200; i++ {
		v := i
		tr.Put(i, &v)
		for _, e := range tr.rebuildBuffer[:cap(tr.rebuildBuffer)] {
			if e.value != nil {
				t.Fatal("split or redistribution retained a value in scratch storage")
			}
		}
	}
	for i := 0; i < 200; i++ {
		if v := tr.Get(i); v == nil || *v != i {
			t.Fatal("clearing scratch storage changed a live value")
		}
	}
}
