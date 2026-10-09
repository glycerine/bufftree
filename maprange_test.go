package bufftree

import "testing"

func TestMapRangeDeletionAndLogShadowing(t *testing.T) {
	for _, cfg := range []Config{tinyConfig, {}} {
		tr := newTreeCore[int, int](&cfg)
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
