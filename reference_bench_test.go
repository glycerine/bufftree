package bufftree

import (
	"encoding/binary"
	"testing"
)

type stringBenchIndex interface {
	Get(string) uint64
	Get2(string) (uint64, bool)
	Put(string, uint64) (uint64, bool)
	Clear()
}

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
		{"Dict", func() stringBenchIndex { return NewDict[string, uint64](nil) }},
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
	d := NewDict[namedString, []byte](nil)
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
}
