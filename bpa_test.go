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
	if p.size != len(model) {
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
