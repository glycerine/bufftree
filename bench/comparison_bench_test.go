package bench

import (
	"cmp"
	"fmt"
	"slices"
	"testing"

	"github.com/glycerine/bufftree"
	"github.com/glycerine/rbtree"
	"github.com/tidwall/btree"
)

type benchUint64Points interface {
	Get(uint64) uint64
	Put(uint64, uint64)
}

type benchUint64Map map[uint64]uint64

func (m benchUint64Map) Get(k uint64) uint64 { return m[k] }
func (m benchUint64Map) Len() int            { return len(m) }
func (m benchUint64Map) Put(k, v uint64) {
	m[k] = v
}

// Use tidwall's generic ordered Map with its default degree (32), without
// copies or path hints. Discard Set's results to match the void Put contract.
type benchTidwallMap struct{ tree btree.Map[uint64, uint64] }

func (m *benchTidwallMap) Get(k uint64) uint64 {
	v, _ := m.tree.Get(k)
	return v
}
func (m *benchTidwallMap) Put(k, v uint64) { m.tree.Set(k, v) }
func (m *benchTidwallMap) Len() int        { return m.tree.Len() }
func (m *benchTidwallMap) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	if length <= 0 {
		return
	}
	m.tree.Ascend(start, func(k, v uint64) bool {
		length--
		return visit(k, v) && length > 0
	})
}

type benchKV struct{ key, value uint64 }

// Reuse a pointer-shaped query for rbtree's interface API, avoiding boxing
// allocations on reads and updates. An inserted item gets its own pointer.
type benchRBTree struct {
	tree  *rbtree.Tree
	probe *benchKV
}

func newBenchRBTree() *benchRBTree {
	return &benchRBTree{
		tree: rbtree.NewTree(func(a, b rbtree.Item) int {
			return cmp.Compare(a.(*benchKV).key, b.(*benchKV).key)
		}),
		probe: &benchKV{},
	}
}
func (m *benchRBTree) Get(k uint64) uint64 {
	m.probe.key = k
	if item := m.tree.Get(m.probe); item != nil {
		return item.(*benchKV).value
	}
	return 0
}
func (m *benchRBTree) Len() int { return m.tree.Len() }
func (m *benchRBTree) Put(k, v uint64) {
	*m.probe = benchKV{key: k, value: v}
	added, it := m.tree.InsertGetIt(m.probe)
	if added {
		m.probe = &benchKV{}
		return
	}
	item := it.Item().(*benchKV)
	item.value = v
}
func (m *benchRBTree) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	if length <= 0 {
		return
	}
	m.probe.key = start
	for it := m.tree.FindGE(m.probe); !it.Limit() && length > 0; it = it.Next() {
		item := it.Item().(*benchKV)
		length--
		if !visit(item.key, item.value) {
			return
		}
	}
}

// Compare identical uint64 data, uniform lookup/update traces, and unique
// scrambled insertions. All point operations use the same interface dispatch.
// Map writes use assignment alone, matching Put's void return contract.
func BenchmarkComparePoints(b *testing.B) {
	for _, bc := range comparisonPointCases(benchLoadSize()) {
		b.Run(bc.name, bc.run)
	}
}

type comparisonBenchmark struct {
	name string
	run  func(*testing.B)
}

type benchPointLayout struct {
	name string
	make func() benchUint64Points
}

// Share constructors with the standalone heap test so it measures exactly
// the same configurations, capacity hints, and adapters as the benchmarks.
func comparisonPointLayouts(n int) []benchPointLayout {
	return []benchPointLayout{
		{"Tree", func() benchUint64Points { return bufftree.NewBPTree[uint64, uint64](nil) }},
		{"GoMap", func() benchUint64Points { return make(benchUint64Map, n) }},
		{"Tidwall", func() benchUint64Points { return &benchTidwallMap{} }},
		{"RBTree", func() benchUint64Points { return newBenchRBTree() }},
	}
}

func loadComparisonPoints(layout benchPointLayout, n int) benchUint64Points {
	idx := layout.make()
	for i := 0; i < n; i++ {
		idx.Put(benchKey(i), uint64(i))
	}
	return idx
}

func comparisonPointCases(n int) []comparisonBenchmark {
	var cases []comparisonBenchmark
	ops := benchTrace(n, 8192, 1)
	freshKeys := make([]uint64, n)
	for i := range freshKeys {
		freshKeys[i] = benchKey(i) | 1
	}
	for _, layout := range comparisonPointLayouts(n) {
		for _, op := range []string{"GetHit", "GetMiss", "Update"} {
			cases = append(cases, comparisonBenchmark{layout.name + "/" + op, func(b *testing.B) {
				idx := loadComparisonPoints(layout, n)
				var sum uint64
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					key := ops[i%len(ops)].key
					switch op {
					case "GetHit":
						sum += idx.Get(key)
					case "GetMiss":
						sum += idx.Get(key | 1)
					case "Update":
						idx.Put(key, uint64(i))
					}
				}
				b.StopTimer()
				benchSink = sum
			}})
		}
		cases = append(cases, comparisonBenchmark{layout.name + "/FreshPut", func(b *testing.B) {
			benchmarkFreshPut(b, layout, n, freshKeys, false)
		}})
	}
	return cases
}

type benchOrderedScan interface {
	Put(uint64, uint64)
	Scan(uint64, int, func(uint64, uint64) bool)
}

type benchScanLayout struct {
	name string
	make func() benchOrderedScan
}

// Scans compare ordered traversal. Full traversals compare the cost of visiting
// every value: Go map uses unspecified order, and
// tidwall/btree and rbtree use key order. All include the same sum and key counter.
func BenchmarkCompareIteration(b *testing.B) {
	for _, bc := range comparisonIterationCases(benchLoadSize()) {
		b.Run(bc.name, bc.run)
	}
}

func comparisonIterationCases(n int) []comparisonBenchmark {
	var cases []comparisonBenchmark
	layouts := []benchScanLayout{
		{"Tree", func() benchOrderedScan { return bufftree.NewBPTree[uint64, uint64](nil) }},
		{"Tidwall", func() benchOrderedScan { return &benchTidwallMap{} }},
		{"RBTree", func() benchOrderedScan { return newBenchRBTree() }},
	}
	for _, layout := range layouts {
		for _, maximum := range []int{10000, 100000} {
			cases = append(cases, comparisonBenchmark{fmt.Sprintf("%s/Scan%d", layout.name, maximum), func(b *testing.B) {
				tr := layout.make()
				for i := 0; i < n; i++ {
					tr.Put(benchKey(i), uint64(i))
				}
				ops := benchTrace(n, 8192, maximum)
				var sum, visited uint64
				visit := func(k, v uint64) bool { sum += v; visited++; return true }
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					o := ops[i%len(ops)]
					tr.Scan(o.key, o.length, visit)
				}
				b.StopTimer()
				benchSink = sum
				reportIteration(b, visited)
			}})
		}
	}
	cases = append(cases, comparisonBenchmark{"GoMap/Iterate", func(b *testing.B) {
		m := make(benchUint64Map, n)
		for i := 0; i < n; i++ {
			m[benchKey(i)] = uint64(i)
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, v := range m {
				sum += v
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"Tidwall/Iterate", func(b *testing.B) {
		var m btree.Map[uint64, uint64]
		for i := 0; i < n; i++ {
			m.Set(benchKey(i), uint64(i))
		}
		var sum, visited uint64
		visit := func(k, v uint64) bool { sum += v; visited++; return true }
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Scan(visit)
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"RBTree/Iterate", func(b *testing.B) {
		m := newBenchRBTree()
		for i := 0; i < n; i++ {
			m.Put(benchKey(i), uint64(i))
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for it := m.tree.Min(); !it.Limit(); it = it.Next() {
				sum += it.Item().(*benchKV).value
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	return cases
}

func TestComparisonOrderedScans(t *testing.T) {
	for _, layout := range []benchScanLayout{
		{"Tidwall", func() benchOrderedScan { return &benchTidwallMap{} }},
		{"RBTree", func() benchOrderedScan { return newBenchRBTree() }},
	} {
		t.Run(layout.name, func(t *testing.T) {
			m := layout.make()
			keys := make([]uint64, 1000)
			values := make(map[uint64]uint64)
			for i := range keys {
				k := benchKey(i)
				keys[i] = k
				values[k] = uint64(i)
				m.Put(k, uint64(i))
			}
			slices.Sort(keys)
			for _, maximum := range []int{0, 1, 100, 10000} {
				for _, o := range benchTrace(len(keys), 100, max(1, maximum)) {
					length := o.length
					if maximum == 0 {
						length = 0
					}
					pos, _ := slices.BinarySearch(keys, o.key)
					want := keys[pos : pos+min(length, len(keys)-pos)]
					var got []uint64
					m.Scan(o.key, length, func(k, v uint64) bool {
						if values[k] != v {
							t.Fatal("scan value")
						}
						got = append(got, k)
						return true
					})
					if !slices.Equal(got, want) {
						t.Fatal("scan start or length")
					}
				}
			}
			count := 0
			m.Scan(0, len(keys), func(k, v uint64) bool { count++; return false })
			if count != 1 {
				t.Fatal("scan early stop")
			}
		})
	}
}

func TestComparisonPointAdapters(t *testing.T) {
	for _, idx := range []benchUint64Points{&benchTidwallMap{}, newBenchRBTree()} {
		if v := idx.Get(1); v != 0 {
			t.Fatal("missing key")
		}
		for i := uint64(0); i < 100; i++ {
			idx.Put(i, i+1)
			if idx.Get(i) != i+1 {
				t.Fatal("new key")
			}
		}
		for i := uint64(0); i < 100; i++ {
			idx.Put(i, i+100)
			if idx.Get(i) != i+100 {
				t.Fatal("update")
			}
			if idx.Get(i) != i+100 || idx.Get(i+1000) != 0 {
				t.Fatal("lookup")
			}
		}
		if n := testing.AllocsPerRun(100, func() { idx.Get(50); idx.Get(500) }); n != 0 {
			t.Fatal("comparison lookup allocates", n)
		}
	}
}

func TestComparisonFreshPuts(t *testing.T) {
	const n = 257
	for _, layout := range comparisonPointLayouts(n) {
		t.Run(layout.name, func(t *testing.T) {
			idx := layout.make()
			for i := 0; i < n; i++ {
				idx.Put(benchKey(i), uint64(i))
			}
			for i := 0; i < n; i++ {
				key := benchKey(i) | 1
				idx.Put(key, uint64(n+i))
			}
			if idx.(interface{ Len() int }).Len() != 2*n {
				t.Fatal("fresh puts must grow the container")
			}
			for i := 0; i < n; i++ {
				if idx.Get(benchKey(i)) != uint64(i) || idx.Get(benchKey(i)|1) != uint64(n+i) {
					t.Fatalf("fresh Put changed an existing key or lost a new key at rank %d", i)
				}
			}
		})
	}
}
