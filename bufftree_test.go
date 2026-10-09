package bufftree

import (
	"cmp"
	"math"
	"math/rand"
	"slices"
	"testing"
)

var tinyConfig = Config{Fanout: 3, LogSize: 3, NumBlocks: 3, BlockSize: 3}

func TestSeparatorRefreshClearsRemovedKeys(t *testing.T) {
	n := &node[string, int]{children: []*node[string, int]{{min: "a"}, {min: "b"}, {min: "c"}, {min: "d"}}}
	refresh(n)
	if n.min != "a" || !slices.Equal(n.keys, []string{"b", "c", "d"}) {
		t.Fatal("incorrect initial separators")
	}
	storage := n.keys[:cap(n.keys)]
	n.children = n.children[:2]
	n.children[1].min = "updated"
	refresh(n)
	if !slices.Equal(n.keys, []string{"updated"}) {
		t.Fatal("incorrect refreshed separator")
	}
	for _, key := range storage[1:] {
		if key != "" {
			t.Fatal("removed separator retains string storage")
		}
	}
	n.children = append(n.children, &node[string, int]{min: "z"})
	refresh(n)
	if !slices.Equal(n.keys, []string{"updated", "z"}) {
		t.Fatal("incorrect separators after regrowth")
	}
}

func TestNewBPTreeNilConfig(t *testing.T) {
	tr := newTreeCore[int, int](nil)
	model := map[int]int{3: 30, 1: 10, 2: 20}
	for k, v := range model {
		tr.Put(k, v)
	}
	checkTree(t, tr, model)
	if tr.cfg.Fanout != 256 || tr.cfg.LogSize != 42 || tr.cfg.NumBlocks != 32 || tr.cfg.BlockSize != 34 {
		t.Fatal("nil config must use the defaults")
	}
}

func TestNewBPTreeCopiesConfig(t *testing.T) {
	for _, cfg := range []Config{{}, tinyConfig} {
		original := cfg
		tr := newTreeCore[int, int](&cfg)
		if cfg != original {
			t.Fatal("constructor must not normalize the caller's config in place")
		}
		// An invalid replacement would fail on first insertion if the tree
		// retained the caller's pointer instead of its own config snapshot.
		cfg = Config{Fanout: 1, LogSize: 1, NumBlocks: 1, BlockSize: 1}
		model := make(map[int]int)
		for i := 0; i < 128; i++ {
			tr.Put(i, i)
			model[i] = i
		}
		checkTree(t, tr, model)
		tr.Clear()
		clear(model)
		for i := 0; i < 16; i++ {
			tr.Put(i, -i)
			model[i] = -i
		}
		checkTree(t, tr, model)
	}
}

func TestGetAndGet2(t *testing.T) {
	var tr treeCore[int, string]
	for _, idx := range []interface {
		Get(int) string
		Get2(int) (string, bool)
		Put(int, string)
		Del(int)
	}{&tr} {
		if idx.Get(7) != "" {
			t.Fatal("Get on empty container")
		}
		if v, ok := idx.Get2(7); ok || v != "" {
			t.Fatal("Get2 on empty container")
		}
		idx.Put(7, "")
		if idx.Get(7) != "" {
			t.Fatal("stored zero value")
		}
		if v, ok := idx.Get2(7); !ok || v != "" {
			t.Fatal("Get2 must distinguish stored zero")
		}
		idx.Put(7, "value")
		if idx.Get(7) != "value" {
			t.Fatal("Get present key")
		}
		if v, ok := idx.Get2(7); !ok || v != "value" {
			t.Fatal("Get2 present key")
		}
		idx.Del(7)
		if idx.Get(7) != "" {
			t.Fatal("Get deleted key")
		}
		if v, ok := idx.Get2(7); ok || v != "" {
			t.Fatal("Get2 deleted key")
		}
	}
	var nilTree treeCore[string, []int]
	nilTree.Put("nil", nil)
	if nilTree.Get("nil") != nil || nilTree.Get("absent") != nil {
		t.Fatal("nil value")
	}
	if _, ok := nilTree.Get2("nil"); !ok {
		t.Fatal("stored nil")
	}
	if _, ok := nilTree.Get2("absent"); ok {
		t.Fatal("missing nil")
	}
}

func TestTreeRandomAgainstMap(t *testing.T) {
	for seed := int64(0); seed < 5; seed++ {
		t.Run(string(rune('A'+seed)), func(t *testing.T) {
			cfg := tinyConfig
			tr := newTreeCore[int, int](&cfg)
			model := map[int]int{}
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 10000; i++ {
				k := rng.Intn(500) - 250
				want, exists := model[k]
				switch rng.Intn(3) {
				case 0:
					tr.Del(k)
					if _, ok := tr.Get2(k); ok {
						t.Fatalf("delete retained %d", k)
					}
					delete(model, k)
				case 1:
					tr.Put(k, i)
					if got, ok := tr.Get2(k); !ok || got != i {
						t.Fatalf("put lost %d", k)
					}
					model[k] = i
				case 2:
					v, ok := tr.Get2(k)
					if ok != exists || v != want {
						t.Fatalf("get %d", k)
					}
				}
				if i%37 == 0 {
					checkTree(t, tr, model)
				}
			}
			checkTree(t, tr, model)
		})
	}
}

func checkTree(t *testing.T, tr *treeCore[int, int], model map[int]int) {
	t.Helper()
	if tr.Len() != len(model) {
		t.Fatalf("length %d != %d", tr.Len(), len(model))
	}
	var want []int
	for k, v := range model {
		want = append(want, k)
		if got, ok := tr.Get2(k); !ok || got != v {
			t.Fatalf("get %d", k)
		}
	}
	slices.Sort(want)
	var got []int
	for k, v := range tr.All() {
		got = append(got, k)
		if v != model[k] {
			t.Fatalf("iteration value %d", k)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("keys %v != %v", got, want)
	}
	if tr.root == nil {
		if len(model) != 0 {
			t.Fatal("missing root")
		}
		return
	}
	var leaves []*node[int, int]
	depth := -1
	var visit func(*node[int, int], int)
	visit = func(n *node[int, int], d int) {
		if n.leaf != nil {
			if depth < 0 {
				depth = d
			} else if depth != d {
				t.Fatal("unbalanced height")
			}
			es := n.leaf.collect()
			for _, e := range es {
				if v, ok := n.leaf.get(e.key); !ok || v != model[e.key] {
					t.Fatal("BPA value differs from map model")
				}
			}
			if len(es) == 0 {
				t.Fatal("empty leaf")
			}
			if n.min != es[0].key {
				t.Fatal("stale leaf minimum")
			}
			if n != tr.root && len(es) < n.leaf.capacity()/2 {
				t.Fatal("underfull leaf")
			}
			leaves = append(leaves, n)
			return
		}
		if len(n.children) > tr.cfg.Fanout || (n != tr.root && len(n.children) < (tr.cfg.Fanout+1)/2) || (n == tr.root && len(n.children) < 2) {
			t.Fatal("bad internal occupancy")
		}
		if len(n.keys) != len(n.children)-1 || n.min != n.children[0].min {
			t.Fatal("bad separators")
		}
		for i, c := range n.children {
			if c.parent != n {
				t.Fatal("bad parent")
			}
			if i > 0 && (n.keys[i-1] != c.min || cmp.Compare(n.children[i-1].min, c.min) >= 0) {
				t.Fatal("stale separator")
			}
			visit(c, d+1)
		}
	}
	if tr.root.parent != nil {
		t.Fatal("root parent")
	}
	visit(tr.root, 0)
	for i, n := range leaves {
		if i == 0 && n.prev != nil || i > 0 && n.prev != leaves[i-1] {
			t.Fatal("bad previous leaf")
		}
		if i == len(leaves)-1 && n.next != nil || i+1 < len(leaves) && n.next != leaves[i+1] {
			t.Fatal("bad next leaf")
		}
	}
}

func TestTreeDeleteDuringIteration(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		tr := newTreeCore[int, int](&tinyConfig)
		for i := 0; i < 1000; i++ {
			k := i
			if reverse {
				k = 999 - i
			}
			tr.Put(k, k)
		}
		it := tr.Iter()
		for i := 0; i < 1000; i++ {
			if !it.Next() || it.Key() != i {
				t.Fatalf("iteration at %d", i)
			}
			if i%2 == 0 {
				it.Del()
			} else {
				tr.Del(i)
			}
		}
		if it.Next() || tr.Len() != 0 || tr.root != nil {
			t.Fatal("tree not empty")
		}
		if it.Next() {
			t.Fatal("exhausted iterator revived")
		}
		tr.Put(5, 9)
		if it.Next() {
			t.Fatal("exhausted iterator revived after insert")
		}
		if !it.Seek(5) || it.Value() != 9 {
			t.Fatal("seek after exhaustion")
		}
	}
}

func TestTreeRangeAndMutation(t *testing.T) {
	tr := newTreeCore[int, int](&tinyConfig)
	for i := 0; i < 100; i++ {
		tr.Put(i*2, i)
	}
	var got []int
	tr.Range(11, 21, func(k, v int) bool { got = append(got, k); tr.Del(k); return true })
	if !slices.Equal(got, []int{12, 14, 16, 18, 20}) {
		t.Fatal(got)
	}
	got = nil
	tr.Scan(21, 3, func(k, v int) bool { got = append(got, k); return true })
	if !slices.Equal(got, []int{22, 24, 26}) {
		t.Fatal(got)
	}
	it := tr.IterFrom(191)
	if !it.Next() || it.Key() != 192 {
		t.Fatal("lower bound")
	}
	tr.Put(193, 8) // Structural changes must also preserve the iterator position.
	if !it.Next() || it.Key() != 193 {
		t.Fatal("insert ahead")
	}
	tr.Clear()
	if it.Next() {
		t.Fatal("clear iterator")
	}
	tr.Range(10, 10, func(k, v int) bool { t.Fatal("empty range"); return false })
}

func TestTreeZeroValueAndOrderedTypes(t *testing.T) {
	var tr treeCore[string, []int]
	tr.Put("z", []int{1})
	tr.Put("a", nil)
	if v, ok := tr.Get2("a"); !ok || v != nil {
		t.Fatal("nil value")
	}
	for k := range tr.All() {
		tr.Del(k)
	}
	if tr.Len() != 0 {
		t.Fatal("zero value iteration")
	}
	ft := newTreeCore[float64, int](&tinyConfig)
	for _, k := range []float64{math.NaN(), math.Inf(-1), -1, 0, 1, math.Inf(1)} {
		ft.Put(k, 7)
	}
	ft.Put(math.NaN(), 8)
	if ft.Len() != 6 {
		t.Fatal("NaNs should compare equal")
	}
	if v, ok := ft.Get2(math.NaN()); !ok || v != 8 {
		t.Fatal("NaN lookup")
	}
	prev := math.Inf(-1)
	for k := range ft.All() {
		if k == k && (prev != prev || prev > k) {
			t.Fatal("float order")
		}
		prev = k
		ft.Del(k)
	}
	if ft.Len() != 0 {
		t.Fatal("float deletes")
	}
	type key uint16
	var named treeCore[key, struct{ X int }]
	named.Put(1, struct{ X int }{3})
}

func TestNaNKeysSortLast(t *testing.T) {
	for _, cfg := range []Config{tinyConfig, {}} {
		tr := newTreeCore[float64, int](&cfg)
		tr.Put(math.NaN(), 1)
		tr.Put(math.Inf(-1), -100)
		tr.Put(math.Inf(1), 100)
		for i := -50; i <= 50; i++ {
			tr.Put(float64(i), i)
		}
		otherNaN := math.Float64frombits(0xfff8000000000123)
		tr.Put(otherNaN, 999)
		if tr.Len() != 104 || tr.Get(math.NaN()) != 999 {
			t.Fatal("NaN equivalence")
		}
		seenNaN := false
		previous := math.Inf(-1)
		for k := range tr.All() {
			if seenNaN {
				t.Fatal("key after NaN")
			}
			if k != k {
				seenNaN = true
			} else if k < previous {
				t.Fatal("finite key order")
			}
			previous = k
		}
		if !seenNaN {
			t.Fatal("missing NaN")
		}
		it := tr.IterFrom(math.NaN())
		if !it.Next() || it.Key() == it.Key() || it.Value() != 999 || it.Next() {
			t.Fatal("seek NaN")
		}
		count := 0
		tr.Range(math.Inf(-1), math.NaN(), func(k float64, v int) bool {
			if k != k {
				t.Fatal("half-open NaN bound")
			}
			count++
			return true
		})
		if count != 103 {
			t.Fatal("range to NaN", count)
		}
		count = 0
		tr.MapRange(math.Inf(-1), math.NaN(), func(k float64, v int) bool {
			if k != k {
				t.Fatal("map NaN bound")
			}
			count++
			return true
		})
		if count != 103 {
			t.Fatal("map to NaN", count)
		}
		tr.Scan(math.NaN(), 2, func(k float64, v int) bool {
			if k == k {
				t.Fatal("scan starting at NaN")
			}
			tr.Del(k)
			return true
		})
		if _, ok := tr.Get2(otherNaN); ok {
			t.Fatal("NaN removal")
		}
	}
	type namedFloat float32
	var tr treeCore[namedFloat, []int]
	tr.Put(namedFloat(math.NaN()), []int{1})
	tr.Put(0, nil)
	it := tr.Iter()
	if !it.Next() || it.Key() != 0 || !it.Next() || it.Key() == it.Key() || it.Next() {
		t.Fatal("named float32 ordering")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, cfg := range []Config{{Fanout: 1}, {LogSize: 1}, {NumBlocks: -1}, {BlockSize: 1}, {NumBlocks: int(^uint(0) >> 1)}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid config accepted")
				}
			}()
			newTreeCore[int, int](&cfg)
		}()
	}
}

func TestTreeBulkSplitAndCollapse(t *testing.T) {
	for _, cfg := range []Config{
		{Fanout: 4, LogSize: 2, NumBlocks: 2, BlockSize: 2},
		{Fanout: 5, LogSize: 16, NumBlocks: 3, BlockSize: 5},
		{Fanout: 6, LogSize: 3, NumBlocks: 5, BlockSize: 7},
		{Fanout: 4, LogSize: 4, NumBlocks: 4, BlockSize: 4},
		{},
	} {
		n := 5000
		if cfg == (Config{}) {
			n = 65536
		}
		tr := newTreeCore[int, int](&cfg)
		model := make(map[int]int, n)
		rng := rand.New(rand.NewSource(2023))
		for _, k := range rng.Perm(n) {
			tr.Put(k, k)
			model[k] = k
		}
		checkTree(t, tr, model)
		for i, k := range rng.Perm(n) {
			tr.Del(k)
			if _, ok := tr.Get2(k); ok {
				t.Fatal("bulk delete")
			}
			delete(model, k)
			if i%4096 == 0 {
				checkTree(t, tr, model)
			}
		}
		checkTree(t, tr, model)
		if tr.root != nil {
			t.Fatal("root did not collapse")
		}
	}
}

func TestTreeIteratorsAndEarlyStop(t *testing.T) {
	tr := newTreeCore[int, int](&tinyConfig)
	for i := 0; i < 100; i++ {
		tr.Put(i, i)
	}
	a, b := tr.Iter(), tr.Iter()
	for i := 0; i < 100; i++ {
		if !a.Next() || a.Key() != i || !b.Next() || b.Key() != i {
			t.Fatal("interleaved iterators")
		}
		if i%3 == 0 {
			tr.Del(i)
		}
	}
	if a.Next() || b.Next() {
		t.Fatal("iterator tail")
	}
	for _, visit := range []func(func(int, int) bool){
		func(f func(int, int) bool) { tr.Range(0, 100, f) },
		func(f func(int, int) bool) { tr.Scan(0, 100, f) },
		func(f func(int, int) bool) { tr.MapRange(0, 100, f) },
	} {
		calls := 0
		visit(func(k, v int) bool { calls++; return false })
		if calls != 1 {
			t.Fatal("visitor did not stop")
		}
	}
	it := tr.Iter()
	if !it.Seek(-10) || it.Key() != 1 {
		t.Fatal("seek below minimum")
	}
	if it.Seek(100) || it.Valid() {
		t.Fatal("seek past end")
	}
	if !it.Seek(49) || it.Key() != 49 {
		t.Fatal("seek reset")
	}
	tr.Scan(0, 0, func(k, v int) bool { t.Fatal("zero length scan"); return false })
	tr.Scan(0, -1, func(k, v int) bool { t.Fatal("negative length scan"); return false })
}
