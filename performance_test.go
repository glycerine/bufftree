package bufftree

import (
	"math/rand"
	"slices"
	"testing"
)

func TestRedistributionBufferDoesNotRetainValues(t *testing.T) {
	tr := NewBPTree[int, *int](nil)
	for i := 0; i < 4096; i++ {
		v := i
		tr.Put(int(benchKey(i)>>1), &v)
	}
	if cap(tr.rebuildBuffer) == 0 {
		t.Fatal("fixture did not exercise pooled redistribution")
	}
	check := func() {
		t.Helper()
		if len(tr.rebuildBuffer) != 0 {
			t.Fatal("completed redistribution retained records")
		}
		for _, e := range tr.rebuildBuffer[:cap(tr.rebuildBuffer)] {
			if e.value != nil {
				t.Fatal("redistribution scratch retains a value pointer")
			}
		}
	}
	check()
	tr.Clear()
	check()
}

func TestQueriesDoNotAllocate(t *testing.T) {
	testQueriesDoNotAllocate(t, Config{})
}

func testQueriesDoNotAllocate(t *testing.T, cfg Config) {
	tr := NewBPTree[int, int](&cfg)
	d := NewDictWithConfig[int, int](cfg)
	for i := 0; i < 4096; i++ {
		tr.Put(i, i)
		d.Put(i, i)
	}
	sum := 0
	visit := func(k, v int) bool { sum += v; return true }
	for _, tc := range []struct {
		name  string
		query func()
	}{
		{"Tree/GetHit", func() { sum += tr.Get(55) }},
		{"Tree/GetMiss", func() { sum += tr.Get(-1) }},
		{"Tree/Get2", func() { v, _ := tr.Get2(55); sum += v }},
		{"Tree/Scan", func() { tr.Scan(35, 100, visit) }},
		{"Tree/Range", func() { tr.Range(35, 150, visit) }},
		{"Tree/MapRange", func() { tr.MapRange(35, 4000, visit) }},
		{"Tree/All", func() {
			for _, v := range tr.All() {
				sum += v
			}
		}},
		{"Dict/GetHit", func() { sum += d.Get(55) }},
		{"Dict/GetMiss", func() { sum += d.Get(-1) }},
		{"Dict/Get2", func() { v, _ := d.Get2(55); sum += v }},
		{"Dict/All", func() {
			for _, v := range d.All() {
				sum += v
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := testing.AllocsPerRun(100, tc.query); n != 0 {
				t.Fatalf("query allocates %.1f times", n)
			}
		})
	}
	if sum == 0 {
		t.Fatal("queries did not observe values")
	}
}

func TestMapRangeReentrant(t *testing.T) {
	tr := NewBPTree[int, int](&tinyConfig)
	for i := 0; i < 100; i++ {
		tr.Put(i, i)
	}
	seen := make(map[int]bool)
	tr.MapRange(0, 100, func(k, v int) bool {
		if seen[k] {
			t.Fatal("duplicate outer key")
		}
		seen[k] = true
		count := 0
		tr.MapRange(k, k+1, func(ik, iv int) bool {
			if ik != k || iv != v {
				t.Fatal("nested query")
			}
			count++
			return true
		})
		if count != 1 {
			t.Fatal("nested query lost outer key")
		}
		tr.Del(k)
		return true
	})
	if len(seen) != 100 || tr.Len() != 0 {
		t.Fatal("reentrant map traversal")
	}
}

func TestScansAgainstSortedModel(t *testing.T) {
	for _, cfg := range []Config{tinyConfig, {}} {
		tr := NewBPTree[int, int](&cfg)
		model := map[int]int{}
		rng := rand.New(rand.NewSource(44))
		for step := 0; step < 2000; step++ {
			key := rng.Intn(300)
			if rng.Intn(3) == 0 {
				tr.Del(key)
				delete(model, key)
			} else {
				tr.Put(key, step)
				model[key] = step
			}
			start, end := rng.Intn(350)-20, rng.Intn(350)-20
			var keys []int
			for k := range model {
				if k >= start {
					keys = append(keys, k)
				}
			}
			slices.Sort(keys)
			limit := rng.Intn(50)
			var got []int
			tr.Scan(start, limit, func(k, v int) bool {
				if v != model[k] {
					t.Fatal("scan value")
				}
				got = append(got, k)
				return true
			})
			if !slices.Equal(got, keys[:min(limit, len(keys))]) {
				t.Fatal("scan keys")
			}
			got = nil
			tr.Range(start, end, func(k, v int) bool {
				if v != model[k] {
					t.Fatal("range value")
				}
				got = append(got, k)
				return true
			})
			var want []int
			for _, k := range keys {
				if k < end {
					want = append(want, k)
				}
			}
			if !slices.Equal(got, want) {
				t.Fatal("range keys")
			}
		}
	}
}

func TestScanMutationWithinBlock(t *testing.T) {
	tr := NewBPTree[int, int](nil)
	for i := 0; i < 200; i++ {
		tr.Put(2*i, i)
	}
	var got []int
	tr.Scan(5, 100, func(k, v int) bool {
		got = append(got, k)
		tr.Del(k)
		if k%2 == 0 {
			tr.Put(k+1, v)
		}
		return true
	})
	for i, k := range got {
		if k != i+6 {
			t.Fatal("mutation scan skipped/repeated key", i, k)
		}
	}
	if len(got) != 100 {
		t.Fatal("mutation scan length")
	}
}

func TestDel2OnContainersAndIterators(t *testing.T) {
	tr := NewBPTree[int, string](nil)
	d := NewDict[int, string]()
	tr.Put(1, "one")
	d.Put(1, "one")
	tit, dit := tr.Iter(), d.Iter()
	if v, ok := tit.Del2(); ok || v != "" {
		t.Fatal("unpositioned tree iterator")
	}
	if v, ok := dit.Del2(); ok || v != "" {
		t.Fatal("unpositioned dictionary iterator")
	}
	tit.Next()
	dit.Next()
	if v, ok := tit.Del2(); !ok || v != "one" {
		t.Fatal("tree iterator Del2")
	}
	if v, ok := dit.Del2(); !ok || v != "one" {
		t.Fatal("dictionary iterator Del2")
	}
	if v, ok := tr.Del2(1); ok || v != "" {
		t.Fatal("absent tree Del2")
	}
	if v, ok := d.Del2(1); ok || v != "" {
		t.Fatal("absent dictionary Del2")
	}
	d.Put(2, "two")
	if !dit.Next() || dit.Key() != 2 {
		t.Fatal("append after iterator Del2")
	}
	if v, ok := d.Del2(2); !ok || v != "two" {
		t.Fatal("dictionary Del2")
	}
}
