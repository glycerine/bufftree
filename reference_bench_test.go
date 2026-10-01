package bufftree

import (
	"encoding/binary"
	"hash/maphash"
	"testing"
)

type stringBenchIndex interface {
	Get(string) uint64
	Get2(string) (uint64, bool)
	Put(string, uint64) (uint64, bool)
	Clear()
}

// Port the stable-slot and bucket-chain layout from reference/keystable.go,
// using maphash instead of its external xxhash dependency. All variants here
// use identical 16-byte keys, uint64 values, and no internal locking.
type benchStableRecord struct {
	key         string
	value, hash uint64
	next        int
}
type benchStableHash struct {
	seed    maphash.Seed
	records []benchStableRecord
	heads   []int
}

func newBenchStableHash(n int) *benchStableHash {
	buckets := 16
	for buckets < 4*n {
		buckets <<= 1
	}
	return &benchStableHash{seed: maphash.MakeSeed(), records: make([]benchStableRecord, 0, n), heads: make([]int, buckets)}
}
func (s *benchStableHash) Get2(k string) (uint64, bool) {
	h := maphash.String(s.seed, k)
	for i := s.heads[int(h)&(len(s.heads)-1)] - 1; i >= 0; i = s.records[i].next {
		r := &s.records[i]
		if r.hash == h && r.key == k {
			return r.value, true
		}
	}
	return 0, false
}
func (s *benchStableHash) Get(k string) uint64 { v, _ := s.Get2(k); return v }
func (s *benchStableHash) Put(k string, v uint64) (uint64, bool) {
	h := maphash.String(s.seed, k)
	bucket := int(h) & (len(s.heads) - 1)
	for i := s.heads[bucket] - 1; i >= 0; i = s.records[i].next {
		r := &s.records[i]
		if r.hash == h && r.key == k {
			old := r.value
			r.value = v
			return old, true
		}
	}
	s.records = append(s.records, benchStableRecord{key: k, value: v, hash: h, next: s.heads[bucket] - 1})
	s.heads[bucket] = len(s.records)
	return 0, false
}
func (s *benchStableHash) Clear() { clear(s.records); s.records = s.records[:0]; clear(s.heads) }

type benchStringMap map[string]uint64

func (m benchStringMap) Get(k string) uint64          { return m[k] }
func (m benchStringMap) Get2(k string) (uint64, bool) { v, ok := m[k]; return v, ok }
func (m benchStringMap) Put(k string, v uint64) (uint64, bool) {
	old, ok := m[k]
	m[k] = v
	return old, ok
}
func (m benchStringMap) Clear() { clear(m) }

func referenceStringKeys(n int) []string {
	keys := make([]string, n)
	var buf [16]byte
	for i := range keys {
		binary.LittleEndian.PutUint64(buf[:8], uint64(i)*0x9e3779b97f4a7c15)
		binary.LittleEndian.PutUint64(buf[8:], uint64(i))
		keys[i] = string(buf[:])
	}
	return keys
}
func BenchmarkReferencePoints(b *testing.B) {
	n := benchLoadSize()
	keys := referenceStringKeys(n)
	misses := make([]string, n)
	for i, k := range keys {
		misses[i] = k + "!"
	}
	for _, layout := range []struct {
		name string
		make func() stringBenchIndex
	}{
		{"Tree", func() stringBenchIndex { return NewBPTree[string, uint64](nil) }},
		{"TreeOnly", func() stringBenchIndex { return NewBPTree[string, uint64](&Config{DisablePointIndex: true}) }},
		{"Dict", func() stringBenchIndex { return NewDict[string, uint64]() }},
		{"KeyStableHash", func() stringBenchIndex { return newBenchStableHash(n) }},
		{"GoMap", func() stringBenchIndex { return make(benchStringMap, n) }},
	} {
		for _, op := range []string{"Get", "Get2", "GetMiss", "Update", "Insert"} {
			b.Run(layout.name+"/"+op, func(b *testing.B) {
				idx := layout.make()
				if op != "Insert" {
					for i, k := range keys {
						idx.Put(k, uint64(i+1))
					}
				}
				var sum uint64
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					k := keys[i%n]
					switch op {
					case "Get":
						sum += idx.Get(k)
					case "Get2":
						if v, ok := idx.Get2(k); ok {
							sum += v
						}
					case "GetMiss":
						sum += idx.Get(misses[i%n])
					case "Update":
						idx.Put(k, uint64(i))
					case "Insert":
						if i > 0 && i%n == 0 {
							idx.Clear()
						}
						idx.Put(k, uint64(i))
					}
				}
				b.StopTimer()
				benchSink = sum
			})
		}
	}
}

func TestReferenceStringPointQueries(t *testing.T) {
	type namedString string
	tr := NewBPTree[namedString, []byte](nil)
	d := NewDict[namedString, []byte]()
	keys := referenceStringKeys(1000)
	value := []byte("value")
	for _, key := range keys {
		tr.Put(namedString(key), value)
		d.Put(namedString(key), value)
	}
	if n := testing.AllocsPerRun(100, func() {
		for _, key := range keys {
			tr.Get(namedString(key))
			d.Get2(namedString(key))
		}
	}); n != 0 {
		t.Fatal("string query allocates", n)
	}
	base := newBenchStableHash(1000)
	for i, key := range keys {
		base.Put(key, uint64(i+1))
	}
	for i, key := range keys {
		if v, ok := base.Get2(key); !ok || v != uint64(i+1) {
			t.Fatal("reference hash lookup")
		}
	}
	base.Clear()
	if v, ok := base.Get2(keys[0]); ok || v != 0 {
		t.Fatal("reference hash clear")
	}
}
