package bufftree

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"slices"
	"sort"
	"strconv"
	"testing"
)

// Benchmarks adapt sections 4 and 6 of the paper to a single goroutine, using
// uint64 keys/values, deterministic traces, and an untimed load phase. There
// BUFFTREE_BENCH_N controls load size. External comparisons are separate.
var benchSink uint64

func reportIteration(b *testing.B, keys uint64) {
	if keys == 0 {
		return
	}
	b.ReportMetric(float64(keys)/b.Elapsed().Seconds(), "entries/s")
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(keys), "iter_ns/key")
	b.ReportMetric(float64(keys)/float64(b.N), "keys/op")
}

type benchIndex interface {
	Get(uint64) uint64
	Get2(uint64) (uint64, bool)
	Put(uint64, uint64) (uint64, bool)
	Scan(uint64, int, func(uint64, uint64) bool)
	MapRange(uint64, uint64, func(uint64, uint64) bool)
}
type benchLayout struct {
	name string
	make func() benchIndex
}

func benchLayouts() []benchLayout {
	var out []benchLayout
	for _, pair := range [][2]int{{4, 4}, {8, 8}, {16, 16}, {32, 32}, {32, 64}, {64, 64}} {
		h, block := pair[0], pair[1]
		out = append(out, benchLayout{fmt.Sprintf("BP/h%d-b%d", h, block), func() benchIndex {
			return NewBPTree[uint64, uint64](&Config{Fanout: 64, LogSize: h, NumBlocks: h, BlockSize: block})
		}})
	}
	for _, slots := range []int{16, 32, 64, 256, 1024, 4096} {
		out = append(out, benchLayout{fmt.Sprintf("BPlus/slots%d", slots), func() benchIndex { return &benchSortedTree{slots: slots} }})
	}
	return out
}
func benchLoadSize() int {
	if s := os.Getenv("BUFFTREE_BENCH_N"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 2 {
			panic("BUFFTREE_BENCH_N must be an integer >= 2")
		}
		return n
	}
	return 1 << 16
}

// A bijection on 63-bit integers scrambles Zipfian ranks into key space.
// Even keys are loaded records; odd keys are disjoint insertion/miss keys.
func benchPermute(x uint64) uint64 {
	const mask = uint64(1<<63 - 1)
	x &= mask
	x ^= x >> 30
	x = x * 0x3f58476d1ce4e5b9 & mask
	x ^= x >> 27
	x = x * 0x14d049bb133111eb & mask
	x ^= x >> 31
	return x
}
func benchKey(rank int) uint64 { return benchPermute(uint64(rank)) << 1 }
func benchLoad(layout benchLayout, n int, sequential bool) benchIndex {
	idx := layout.make()
	for i := 0; i < n; i++ {
		k := benchKey(i)
		if sequential {
			k = uint64(i)
		}
		idx.Put(k, uint64(i))
	}
	return idx
}

type benchOp struct {
	key          uint64
	roll, length int
	end          uint64
}

// Inverse-CDF sampling supports the paper's theta=.99 exactly. math/rand.Zipf
// requires an exponent >1, so it cannot directly express this distribution.
func benchTrace(n, count, maxLen int, zipf bool) []benchOp {
	rng := rand.New(rand.NewSource(2023))
	var cdf []float64
	if zipf {
		cdf = make([]float64, n)
		total := 0.0
		for i := range cdf {
			total += math.Pow(float64(i+1), -.99)
			cdf[i] = total
		}
		for i := range cdf {
			cdf[i] /= total
		}
	}
	ops := make([]benchOp, count)
	for i := range ops {
		rank := rng.Intn(n)
		if zipf {
			u := rng.Float64()
			rank = sort.SearchFloat64s(cdf, u)
		}
		ops[i] = benchOp{key: benchKey(rank), roll: rng.Intn(100), length: rng.Intn(maxLen + 1)}
	}
	return ops
}

func BenchmarkTree(b *testing.B) {
	n := benchLoadSize()
	for _, layout := range benchLayouts() {
		b.Run(layout.name, func(b *testing.B) {
			for _, op := range []string{"FindHit", "FindHit2", "FindMiss", "FindMiss2", "Update", "InsertRandom", "InsertSequential", "Scan100", "Scan10000", "Scan100000", "Map100", "Map10000", "Map100000"} {
				b.Run(op, func(b *testing.B) {
					seq := op == "InsertSequential"
					idx := benchLoad(layout, n, seq)
					maxLen := 100
					if op == "Scan10000" || op == "Map10000" {
						maxLen = 10000
					}
					if op == "Scan100000" || op == "Map100000" {
						maxLen = 100000
					}
					ops := benchTrace(n, 8192, maxLen, false)
					isMap := op == "Map100" || op == "Map10000" || op == "Map100000"
					if isMap {
						prepareMapEnds(idx, ops)
					}
					var sum, visited uint64
					visit := func(k, v uint64) bool { sum += v; visited++; return true }
					b.ReportAllocs()
					b.ResetTimer()
					inserted := 0
					for i := 0; i < b.N; i++ {
						o := ops[i%len(ops)]
						switch op {
						case "FindHit":
							v := idx.Get(o.key)
							sum += v
						case "FindMiss":
							v := idx.Get(o.key | 1)
							sum += v
						case "FindHit2":
							if v, ok := idx.Get2(o.key); ok {
								sum += v
							}
						case "FindMiss2":
							if v, ok := idx.Get2(o.key | 1); ok {
								sum += v
							}
						case "Update":
							idx.Put(o.key, uint64(i))
						case "InsertRandom", "InsertSequential":
							if inserted == n {
								b.StopTimer()
								idx = benchLoad(layout, n, seq)
								inserted = 0
								b.StartTimer()
							}
							k := benchPermute(uint64(inserted))<<1 | 1
							if seq {
								k = uint64(n + inserted)
							}
							idx.Put(k, uint64(i))
							inserted++
						case "Scan100", "Scan10000", "Scan100000":
							idx.Scan(o.key, o.length, visit)
						default:
							idx.MapRange(o.key, o.end, visit)
						}
					}
					b.StopTimer()
					benchSink = sum
					reportIteration(b, visited)
				})
			}
		})
	}
}

type ycsbWorkload struct {
	name                          string
	reads, scans, updates, maxLen int
	mapping                       bool
}

var paperWorkloads = []ycsbWorkload{
	{name: "A", reads: 50, updates: 50},
	{name: "B", reads: 95, updates: 5},
	{name: "C", reads: 100},
	{name: "E", scans: 95, maxLen: 100},
	{name: "X", scans: 100, maxLen: 10000},
	{name: "Y", scans: 100, maxLen: 10000, mapping: true},
}

func prepareMapEnds(idx benchIndex, ops []benchOp) {
	for i := range ops {
		o := &ops[i]
		o.end = o.key
		idx.Scan(o.key, o.length, func(k, v uint64) bool { o.end = k + 1; return true })
	}
}

// ycsbStep is shared by benchmarks and the untimed correctness smoke test.
func ycsbStep(idx benchIndex, w ycsbWorkload, o benchOp, serial uint64, visit func(uint64, uint64) bool) (sum uint64, inserted bool) {
	if o.roll < w.reads {
		sum = idx.Get(o.key)
		return
	}
	if o.roll < w.reads+w.scans {
		if w.mapping {
			idx.MapRange(o.key, o.end, visit)
		} else {
			idx.Scan(o.key, o.length, visit)
		}
		return
	}
	if w.updates > 0 {
		idx.Put(o.key, serial)
	} else {
		idx.Put(benchPermute(serial)<<1|1, serial)
		inserted = true
	}
	return
}
func BenchmarkYCSB(b *testing.B) {
	n := benchLoadSize()
	for _, layout := range benchLayouts() {
		for _, zipf := range []bool{false, true} {
			dist := "Uniform"
			if zipf {
				dist = "Zipf99"
			}
			for _, w := range paperWorkloads {
				b.Run(layout.name+"/"+dist+"/"+w.name, func(b *testing.B) {
					idx := benchLoad(layout, n, false)
					ops := benchTrace(n, 8192, max(1, w.maxLen), zipf)
					if w.mapping {
						prepareMapEnds(idx, ops)
					}
					var sum, visited uint64
					visit := func(k, v uint64) bool { sum += v; visited++; return true }
					inserted := 0
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						// Bound tree growth, preserving a load of N to 2N records.
						if inserted == n {
							b.StopTimer()
							idx = benchLoad(layout, n, false)
							inserted = 0
							b.StartTimer()
						}
						v, ins := ycsbStep(idx, w, ops[i%len(ops)], uint64(i), visit)
						sum += v
						if ins {
							inserted++
						}
					}
					b.StopTimer()
					benchSink = sum
					reportIteration(b, visited)
				})
			}
		}
	}
}

func BenchmarkDict(b *testing.B) {
	n := benchLoadSize()
	for _, op := range []string{"Get", "Get2", "Update", "Iterate", "DeleteCurrent"} {
		b.Run(op, func(b *testing.B) {
			fill := func() *Dict[uint64, uint64] {
				d := NewDict[uint64, uint64]()
				for i := 0; i < n; i++ {
					d.Put(benchKey(i), uint64(i))
				}
				return d
			}
			d := fill()
			ops := benchTrace(n, 8192, 1, false)
			var sum uint64
			var visited int64
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				switch op {
				case "Get":
					v := d.Get(ops[i%len(ops)].key)
					sum += v
				case "Get2":
					if v, ok := d.Get2(ops[i%len(ops)].key); ok {
						sum += v
					}
				case "Update":
					d.Put(ops[i%len(ops)].key, uint64(i))
				case "Iterate":
					for _, v := range d.All() {
						sum += v
						visited++
					}
				case "DeleteCurrent":
					if i > 0 {
						b.StopTimer()
						d = fill()
						b.StartTimer()
					}
					for k, v := range d.All() {
						sum += v
						d.Del(k)
						visited++
					}
				}
			}
			b.StopTimer()
			benchSink = sum
			reportIteration(b, uint64(visited))
		})
	}
}

// A deliberately small, benchmark-only B+ tree baseline with sorted leaf
// arrays and the same 64-child fanout. It is independent of the BP-tree code.
type benchKV struct{ key, value uint64 }
type benchSortedNode struct {
	min      uint64
	keys     []uint64
	children []*benchSortedNode
	items    []benchKV
	next     *benchSortedNode
}
type benchSortedTree struct {
	root  *benchSortedNode
	slots int
}

func benchSortedRefresh(n *benchSortedNode) {
	if n.children == nil {
		n.min = n.items[0].key
		return
	}
	n.min = n.children[0].min
	if cap(n.keys) < len(n.children)-1 {
		n.keys = make([]uint64, len(n.children)-1)
	} else {
		n.keys = n.keys[:len(n.children)-1]
	}
	for i := 1; i < len(n.children); i++ {
		n.keys[i-1] = n.children[i].min
	}
}
func benchSortedChild(n *benchSortedNode, k uint64) int {
	return sort.Search(len(n.keys), func(i int) bool { return n.keys[i] > k })
}
func (t *benchSortedTree) leaf(k uint64) *benchSortedNode {
	n := t.root
	for n != nil && n.children != nil {
		n = n.children[benchSortedChild(n, k)]
	}
	return n
}
func (t *benchSortedTree) Get2(k uint64) (uint64, bool) {
	n := t.leaf(k)
	if n == nil {
		return 0, false
	}
	i := sort.Search(len(n.items), func(i int) bool { return n.items[i].key >= k })
	if i < len(n.items) && n.items[i].key == k {
		return n.items[i].value, true
	}
	return 0, false
}

func (t *benchSortedTree) Get(k uint64) uint64 {
	v, _ := t.Get2(k)
	return v
}
func (t *benchSortedTree) Put(k, v uint64) (uint64, bool) {
	if t.root == nil {
		t.root = &benchSortedNode{items: make([]benchKV, 0, t.slots+1)}
	}
	old, found, right := t.insert(t.root, k, v)
	if right != nil {
		t.root = &benchSortedNode{children: []*benchSortedNode{t.root, right}}
		benchSortedRefresh(t.root)
	}
	return old, found
}
func (t *benchSortedTree) insert(n *benchSortedNode, k, v uint64) (uint64, bool, *benchSortedNode) {
	var old uint64
	found := false
	if n.children == nil {
		i := sort.Search(len(n.items), func(i int) bool { return n.items[i].key >= k })
		if i < len(n.items) && n.items[i].key == k {
			old = n.items[i].value
			n.items[i].value = v
			return old, true, nil
		}
		n.items = slices.Insert(n.items, i, benchKV{k, v})
		benchSortedRefresh(n)
		if len(n.items) <= t.slots {
			return 0, false, nil
		}
		mid := len(n.items) / 2
		r := &benchSortedNode{items: make([]benchKV, len(n.items)-mid, t.slots+1), next: n.next}
		copy(r.items, n.items[mid:])
		clear(n.items[mid:])
		n.items = n.items[:mid]
		n.next = r
		benchSortedRefresh(r)
		return 0, false, r
	}
	i := benchSortedChild(n, k)
	var right *benchSortedNode
	old, found, right = t.insert(n.children[i], k, v)
	if right != nil {
		n.children = slices.Insert(n.children, i+1, right)
		benchSortedRefresh(n)
	} else if i == 0 {
		n.min = n.children[0].min
	} else {
		n.keys[i-1] = n.children[i].min
	}
	if len(n.children) <= 64 {
		return old, found, nil
	}
	mid := len(n.children) / 2
	r := &benchSortedNode{children: append([]*benchSortedNode(nil), n.children[mid:]...)}
	clear(n.children[mid:])
	n.children = n.children[:mid]
	benchSortedRefresh(n)
	benchSortedRefresh(r)
	return old, found, r
}

// BenchmarkLeafCopies isolates the leaf layout as in section 4. Insert fills
// copies from half to full, FindMiss probes full copies, and Scan sums one copy
// per operation. Copy order is shuffled outside the timed loop. Unlike the
// paper's 48-thread experiment, these copies are accessed serially.
func BenchmarkLeafCopies(b *testing.B) {
	const copies = 128
	for exp := 2; exp <= 12; exp++ {
		slots := 1 << exp
		h := 1 << (exp / 2)
		cfg := Config{LogSize: h, NumBlocks: h, BlockSize: slots / h}
		for _, buffered := range []bool{false, true} {
			name := "Sorted"
			if buffered {
				name = "BPA"
			}
			for _, op := range []string{"Insert", "FindMiss", "Scan"} {
				b.Run(fmt.Sprintf("%s/slots%d/%s", name, slots, op), func(b *testing.B) {
					var ps []*bpa[uint64, uint64]
					var arrays [][]benchKV
					load := func() {
						fill := slots
						if op == "Insert" {
							fill = slots / 2
						}
						if buffered {
							ps = make([]*bpa[uint64, uint64], copies)
							for i := range ps {
								ps[i] = newBPA[uint64, uint64](cfg)
								for j := 0; j < fill; j++ {
									ps[i].set(benchKey(j), uint64(j))
								}
							}
						} else {
							arrays = make([][]benchKV, copies)
							for i := range arrays {
								arrays[i] = make([]benchKV, fill, slots)
								for j := 0; j < fill; j++ {
									arrays[i][j] = benchKV{benchKey(j), uint64(j)}
								}
								slices.SortFunc(arrays[i], func(a, b benchKV) int {
									if a.key < b.key {
										return -1
									}
									if a.key > b.key {
										return 1
									}
									return 0
								})
							}
						}
					}
					load()
					order := rand.New(rand.NewSource(2023)).Perm(copies)
					var sum, visited uint64
					inserted := 0
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						id := order[i%copies]
						switch op {
						case "Insert":
							if inserted == copies*(slots/2) {
								b.StopTimer()
								load()
								inserted = 0
								b.StartTimer()
							}
							k := benchKey(slots/2 + inserted/copies)
							if buffered {
								ps[id].set(k, k)
							} else {
								a := arrays[id]
								pos := sort.Search(len(a), func(j int) bool { return a[j].key >= k })
								arrays[id] = slices.Insert(a, pos, benchKV{k, k})
							}
							inserted++
						case "FindMiss":
							k := benchKey(i%slots) | 1
							if buffered {
								v, _ := ps[id].get(k)
								sum += v
							} else {
								a := arrays[id]
								pos := sort.Search(len(a), func(j int) bool { return a[j].key >= k })
								if pos < len(a) && a[pos].key == k {
									sum += a[pos].value
								}
							}
						case "Scan":
							if buffered {
								c := ps[id].cursor(0, false, false)
								for e, ok := c.next(); ok; e, ok = c.next() {
									sum += e.value
									visited++
								}
							} else {
								for _, e := range arrays[id] {
									sum += e.value
									visited++
								}
							}
						}
					}
					b.StopTimer()
					benchSink = sum
					reportIteration(b, visited)
				})
			}
		}
	}
}

func TestBenchmarkWorkloadsAgainstBaseline(t *testing.T) {
	// Exercise the actual timed operation dispatch without running a benchmark
	// calibration during go test. Map order is deliberately ignored.
	for _, layout := range benchLayouts()[:6] {
		for _, zipf := range []bool{false, true} {
			for _, w := range paperWorkloads {
				idx := benchLoad(layout, 257, false)
				base := benchLoad(benchLayout{make: func() benchIndex { return &benchSortedTree{slots: 16} }}, 257, false)
				ops := benchTrace(257, 128, max(1, w.maxLen), zipf)
				if w.mapping {
					prepareMapEnds(base, ops)
				}
				for i, o := range ops {
					var got, want []benchKV
					v, ins := ycsbStep(idx, w, o, uint64(i), func(k, v uint64) bool { got = append(got, benchKV{k, v}); return true })
					bv, bins := ycsbStep(base, w, o, uint64(i), func(k, v uint64) bool { want = append(want, benchKV{k, v}); return true })
					byKey := func(a, b benchKV) int {
						if a.key < b.key {
							return -1
						}
						if a.key > b.key {
							return 1
						}
						return 0
					}
					if w.mapping {
						slices.SortFunc(got, byKey)
						slices.SortFunc(want, byKey)
					}
					if v != bv || ins != bins || !slices.Equal(got, want) {
						t.Fatalf("%s zipf=%v workload=%s step=%d differs", layout.name, zipf, w.name, i)
					}
				}
			}
		}
	}
}

func TestBenchmarkBaselineSplits(t *testing.T) {
	base := &benchSortedTree{slots: 16}
	for i := 0; i < 5000; i++ {
		base.Put(benchKey(i), uint64(i))
	}
	for i := 0; i < 5000; i++ {
		base.Put(benchKey(i), uint64(i+1))
		if v, ok := base.Get2(benchKey(i)); !ok || v != uint64(i+1) {
			t.Fatal("baseline lookup")
		}
	}
	count := 0
	var prev uint64
	base.Scan(0, 6000, func(k, v uint64) bool {
		if count > 0 && prev >= k {
			t.Fatal("baseline order")
		}
		prev = k
		count++
		return true
	})
	if count != 5000 {
		t.Fatal("baseline count", count)
	}
}
func (t *benchSortedTree) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	n := t.leaf(start)
	if n == nil || length <= 0 {
		return
	}
	pos := sort.Search(len(n.items), func(i int) bool { return n.items[i].key >= start })
	for n != nil && length > 0 {
		for pos < len(n.items) && length > 0 {
			e := n.items[pos]
			if !visit(e.key, e.value) {
				return
			}
			pos++
			length--
		}
		n = n.next
		pos = 0
	}
}
func (t *benchSortedTree) MapRange(start, end uint64, visit func(uint64, uint64) bool) {
	if start >= end {
		return
	}
	n := t.leaf(start)
	if n == nil {
		return
	}
	pos := sort.Search(len(n.items), func(i int) bool { return n.items[i].key >= start })
	for n != nil {
		for pos < len(n.items) {
			e := n.items[pos]
			if e.key >= end || !visit(e.key, e.value) {
				return
			}
			pos++
		}
		n = n.next
		pos = 0
	}
}
