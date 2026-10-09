package bufftree

import (
	"math"
	"math/rand"
	"slices"
	"testing"
)

func TestNewDictNilConfig(t *testing.T) {
	d := NewDict[int, int](nil)
	if d.index.cfg != (Config{Fanout: 64, LogSize: 32, NumBlocks: 32, BlockSize: 32}) {
		t.Fatal("nil config must store the defaults before returning")
	}
	for _, k := range []int{3, 1, 2} {
		d.Put(k, 10*k)
	}
	var keys []int
	for k, v := range d.All() {
		keys = append(keys, k)
		if v != 10*k {
			t.Fatal("unexpected value")
		}
	}
	if !slices.Equal(keys, []int{3, 1, 2}) || d.Len() != 3 {
		t.Fatal("default dictionary must preserve insertion order")
	}
}

func TestNewDictCopiesConfig(t *testing.T) {
	for _, cfg := range []Config{{}, tinyConfig} {
		original := cfg
		d := NewDict[int, int](&cfg)
		if cfg != original {
			t.Fatal("constructor must not normalize the caller's config in place")
		}
		if d.index.cfg != original.normalized() {
			t.Fatal("constructor must store its config snapshot before returning")
		}
		// This would panic on insertion if the constructor kept the caller's
		// pointer, including if it consulted that pointer after Clear.
		cfg = Config{Fanout: 1, LogSize: 1, NumBlocks: 1, BlockSize: 1}
		for round := 0; round < 2; round++ {
			for k := 127; k >= 0; k-- {
				d.Put(k, k+round)
			}
			count := 0
			for k, v := range d.All() {
				if k != 127-count || v != k+round {
					t.Fatal("configured dictionary lost insertion order or values")
				}
				if got, ok := d.Get2(k); !ok || got != v {
					t.Fatal("configured dictionary lookup")
				}
				count++
			}
			if count != 128 || d.Len() != 128 {
				t.Fatal("configured dictionary length")
			}
			d.Clear()
		}
	}
}

func TestDictInsertionOrderAndDeletion(t *testing.T) {
	d := NewDict[int, string](&tinyConfig)
	d.Put(8, "eight")
	d.Put(2, "two")
	d.Put(6, "six")
	d.Put(2, "TWO")
	if got := d.Get(2); got != "TWO" {
		t.Fatal("replace")
	}
	var keys []int
	for k := range d.All() {
		keys = append(keys, k)
	}
	if !slices.Equal(keys, []int{8, 2, 6}) {
		t.Fatal(keys)
	}
	d.Del(2)
	d.Put(2, "new")
	keys = nil
	for k, v := range d.All() {
		keys = append(keys, k)
		if got, ok := d.Get2(k); !ok || got != v {
			t.Fatal("get")
		}
		d.Del(k)
	}
	if !slices.Equal(keys, []int{8, 6, 2}) || d.Len() != 0 {
		t.Fatal(keys)
	}
}

func TestDictRandomAgainstModel(t *testing.T) {
	d := NewDict[int, int](&tinyConfig)
	model := map[int]int{}
	var order []int
	rng := rand.New(rand.NewSource(23))
	for i := 0; i < 20000; i++ {
		k := rng.Intn(500)
		_, exists := model[k]
		if rng.Intn(3) == 0 {
			d.Del(k)
			if _, ok := d.Get2(k); ok {
				t.Fatal("delete")
			}
			if exists {
				delete(model, k)
				j := slices.Index(order, k)
				order = slices.Delete(order, j, j+1)
			}
		} else {
			d.Put(k, i)
			if got, ok := d.Get2(k); !ok || got != i {
				t.Fatal("set")
			}
			if !exists {
				order = append(order, k)
			}
			model[k] = i
		}
		if i%97 == 0 {
			var got []int
			for key, v := range d.All() {
				got = append(got, key)
				if v != model[key] {
					t.Fatal("value")
				}
			}
			if !slices.Equal(got, order) || d.Len() != len(model) {
				t.Fatal("order/length")
			}
		}
	}
}

func TestDictIteratorDeletesAheadAndSeesNewInsertions(t *testing.T) {
	var d Dict[int, int]
	for i := 0; i < 10; i++ {
		d.Put(i, i)
	}
	it := d.Iter()
	if !it.Next() || it.Key() != 0 {
		t.Fatal("first")
	}
	d.Del(1)
	d.Del(2)
	d.Del(9)
	d.Put(1, 100)
	d.Put(10, 10) // Reinsertions and later appends are visited at the end.
	var got []int
	for it.Next() {
		got = append(got, it.Key())
		it.Del()
		if _, ok := d.Get2(it.Key()); ok {
			t.Fatal("iterator delete")
		}
	}
	if !slices.Equal(got, []int{3, 4, 5, 6, 7, 8, 1, 10}) {
		t.Fatal(got)
	}
	if it.Next() {
		t.Fatal("exhaustion")
	}
	it = d.Iter()
	d.Clear()
	d.Put(55, 55)
	if it.Next() {
		t.Fatal("iterator crossed clear")
	}
	var empty Dict[float64, []int]
	empty.Put(math.NaN(), nil)
	empty.Put(math.NaN(), []int{7})
	if empty.Len() != 1 {
		t.Fatal("NaN duplicate")
	}
	for k := range empty.All() {
		empty.Del(k)
		if _, ok := empty.Get2(k); ok {
			t.Fatal("NaN delete")
		}
	}
}

func TestDictIterationSeesAppendsAfterDeletingTail(t *testing.T) {
	for _, deleteCurrent := range []bool{false, true} {
		var d Dict[int, int]
		d.Put(0, 0)
		it := d.Iter()
		for i := 0; i < 50; i++ {
			if !it.Next() || it.Key() != i {
				t.Fatalf("live append at %d", i)
			}
			if deleteCurrent {
				it.Del()
			}
			if i < 49 {
				d.Put(i+1, i+1)
			}
		}
		if it.Next() {
			t.Fatal("unexpected extra entry")
		}
		d.Put(50, 50)
		if it.Next() {
			t.Fatal("exhausted iterator revived")
		}
	}
}

func TestDictAllVisitsInsertsDuringRange(t *testing.T) {
	var d Dict[string, int]
	d.Put("first", 1)
	var got []string
	for k := range d.All() {
		got = append(got, k)
		d.Del(k)
		if k == "first" {
			d.Put("second", 2)
		}
	}
	if !slices.Equal(got, []string{"first", "second"}) {
		t.Fatal(got)
	}
	it := d.Iter() // Created empty, but not advanced to exhaustion.
	d.Put("third", 3)
	if !it.Next() || it.Key() != "third" {
		t.Fatal("append before first advance")
	}
}

func TestMapRangeDeletionAndLogShadowing(t *testing.T) {
	for _, cfg := range []Config{tinyConfig, {}} {
		tr := NewBPTree[int, int](&cfg)
		for i := 0; i < 2000; i++ {
			tr.Put(i, i)
		}
		for i := 0; i < 2000; i += 3 {
			tr.Put(i, -i)
		}
		for i := 0; i < 2000; i += 7 {
			tr.Del(i)
		}
		seen := map[int]bool{}
		tr.MapRange(23, 1990, func(k, v int) bool {
			if k < 23 || k >= 1990 || k%7 == 0 || seen[k] {
				t.Fatalf("invalid map key %d", k)
			}
			want := k
			if k%3 == 0 {
				want = -k
			}
			if v != want {
				t.Fatal("stale map value")
			}
			seen[k] = true
			tr.Del(k)
			return true
		})
		for k := 23; k < 1990; k++ {
			if seen[k] != (k%7 != 0) {
				t.Fatalf("missing key %d", k)
			}
		}
		tr.MapRange(-1, 3000, func(k, v int) bool { tr.Del(k); return true })
		if tr.Len() != 0 {
			t.Fatal("map deletion did not empty tree")
		}
	}
}

func TestLazyBlockSorting(t *testing.T) {
	p := newBPA[int, int](Config{LogSize: 4, NumBlocks: 4, BlockSize: 8})
	var es []entry[int, int]
	for i := 0; i < 16; i++ {
		es = append(es, entry[int, int]{key: i * 10, value: i})
	}
	p.load(es)
	for _, k := range []int{15, 55, 95, 135} {
		p.set(k, k)
	}
	for _, sorted := range p.sorted {
		if sorted {
			t.Fatal("flush should leave appended blocks unsorted")
		}
	}
	p.mapEntries(0, 200, nil)
	for _, sorted := range p.sorted {
		if sorted {
			t.Fatal("unordered map sorted a block")
		}
	}
	c := p.cursor(0, true, false)
	c.next()
	if !p.sorted[0] || p.sorted[1] || p.sorted[2] || p.sorted[3] {
		t.Fatal("short scan sorted untouched blocks")
	}
}
