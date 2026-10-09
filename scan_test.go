package bufftree

import "testing"

func TestFirstScanDefersLogFlush(t *testing.T) {
	tr := NewBPTree[int, int](nil)
	for i := 0; i < 261; i++ {
		tr.Put(i, i)
	}
	p := tr.root.leaf
	if p.logN != 5 || p.scanReady {
		t.Fatal("unexpected cold-scan fixture")
	}
	version := tr.version
	for pass := 0; pass < 2; pass++ {
		count := 0
		tr.Scan(0, 1000, func(k, v int) bool {
			if k != count || v != k {
				t.Fatal("cold/prepared scan mismatch")
			}
			count++
			return true
		})
		if count != 261 {
			t.Fatal("cold/prepared scan count")
		}
		if pass == 0 && (p.logN != 5 || tr.version != version) {
			t.Fatal("first scan eagerly flushed its log")
		}
	}
	if p.logN != 0 || tr.version == version {
		t.Fatal("repeat long scan did not prepare its leaf")
	}
}

func TestHeaderMirrorsOverwriteAndDelete(t *testing.T) {
	cfg := Config{Fanout: 3, LogSize: 7, NumBlocks: 3, BlockSize: 5}
	tr := NewBPTree[int, *int](&cfg)
	a, b := 11, 22
	for i := 0; i < 15; i++ {
		tr.Put(i, &a)
	}
	tr.root.leaf.flush()
	// Full leaves use direct base overwrites. Include every header, as well
	// as ordinary block records, then check both physical copies of pointers.
	for i := 0; i < 15; i++ {
		tr.Put(i, &b)
	}
	p := tr.root.leaf
	for i := 0; i < p.headerN; i++ {
		if p.data[p.cfg.LogSize+p.cfg.NumBlocks+i*p.cfg.BlockSize] != *p.header(i) {
			t.Fatal("full-leaf overwrite retained a stale header mirror")
		}
	}
	tr.Del(0)
	if p.header(0).value != nil || p.data[p.cfg.LogSize+p.cfg.NumBlocks].value != nil {
		t.Fatal("deleting a header retained its value through the mirror")
	}
	tr.Put(0, &a)
	count := 0
	tr.Scan(0, 100, func(k int, v *int) bool {
		want := &b
		if k == 0 {
			want = &a
		}
		if v != want {
			t.Fatal("scan saw stale mirrored value", k)
		}
		count++
		return true
	})
	if count != 15 || tr.Len() != 15 {
		t.Fatal("header resurrection count")
	}
}

func TestScanPreparationPreservesLiveCursors(t *testing.T) {
	tr := NewBPTree[int, int](nil)
	for i := 0; i < 4096; i++ {
		tr.Put((i*129)%4096, i)
	}
	for range tr.All() {
	}
	for i := 0; i < 20; i++ {
		tr.Put(i, -i)
	}
	it := tr.Iter()
	if !it.Next() || it.Key() != 0 {
		t.Fatal("initial iterator position")
	}
	version := tr.version
	count := 0
	tr.Scan(0, 10000, func(k, v int) bool {
		if k != count {
			t.Fatal("nested scan skipped/repeated key", k, count)
		}
		count++
		if k%100 == 0 {
			tr.Scan(k, 10000, func(ik, iv int) bool { return ik < k+10 })
		}
		if tr.Len() != 4096 {
			t.Fatal("scan preparation changed Len")
		}
		return true
	})
	if count != 4096 || tr.version == version {
		t.Fatal("fixture did not exercise scan preparation")
	}
	for k := 1; k < 4096; k++ {
		if !it.Next() || it.Key() != k || it.Value() != tr.Get(k) {
			t.Fatal("prepared scan invalidated an existing iterator", k)
		}
	}
	if it.Next() {
		t.Fatal("iterator duplicated a key")
	}
}

func TestScanRunMutationAcrossLeaves(t *testing.T) {
	cfg := Config{Fanout: 4, LogSize: 8, NumBlocks: 4, BlockSize: 8}
	tr := NewBPTree[int, int](&cfg)
	for i := 0; i < 300; i++ {
		tr.Put(i*2, i*2)
	}
	count := 0
	tr.Scan(0, 1000, func(k, v int) bool {
		if k != count || v != k {
			t.Fatal("mutation lost ordered progress", k, v, count)
		}
		count++
		tr.Del(k)
		if k%2 == 0 {
			tr.Put(k+1, k+1)
		}
		return true
	})
	if count != 600 || tr.Len() != 0 {
		t.Fatal("mutation count or root collapse", count, tr.Len())
	}
	for i := 0; i < 100; i++ {
		tr.Put(i, i)
	}
	count = 0
	tr.Range(0, 200, func(k, v int) bool {
		count++
		if k == 0 {
			tr.Clear()
			tr.Put(150, 150)
		} else if k != 150 {
			t.Fatal("range retained cleared records")
		}
		return true
	})
	if count != 2 {
		t.Fatal("range did not observe append after Clear")
	}
}

func TestTinyFirstScanDoesNotAllocate(t *testing.T) {
	trees := make([]*Tree[int, int], 101)
	for i := range trees {
		tr := NewBPTree[int, int](nil)
		tr.Put(3, 3)
		tr.Put(1, 1)
		tr.Put(2, 2)
		trees[i] = tr
	}
	i, total := 0, 0
	visit := func(k, v int) bool { total += v; return true }
	if allocs := testing.AllocsPerRun(100, func() {
		trees[i].Scan(0, 1000, visit)
		i++
	}); allocs != 0 {
		t.Fatalf("first scan allocated %g times", allocs)
	}
	if total != 101*6 {
		t.Fatal("first scans lost buffered records")
	}
}
