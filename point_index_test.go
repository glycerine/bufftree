package bufftree

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func TestPointIndexRandomAndCollisions(t *testing.T) {
	for _, noCache := range []bool{false, true} {
		for _, collide := range []bool{false, true} {
			t.Run(fmt.Sprintf("HashNoCache=%v/Collide=%v", noCache, collide), func(t *testing.T) {
				var p pointIndex[int, int]
				p.init(!noCache)
				if (len(p.hashes) == 0) != noCache {
					t.Fatal("full hash storage must follow HashNoCache")
				}
				// Actual hashes sharing their low eight bits collide and wrap at every
				// table size this test reaches, including after growth. This exercises
				// deletion that recomputes hashes when full hashes are not retained.
				keys := make([]int, 0, 100)
				for k := 0; len(keys) < 100; k++ {
					if !collide || p.hash(k)&255 == 255 {
						keys = append(keys, k)
					}
				}
				model := map[int]int{}
				rng := rand.New(rand.NewSource(2023))
				for step := 0; step < 10000; step++ {
					k := keys[rng.Intn(len(keys))]
					h := p.hash(k)
					i, found := p.find(k, h)
					want, exists := model[k]
					if found != exists || (found && p.slots[i].value != want) {
						t.Fatal("point index lookup")
					}
					if rng.Intn(3) == 0 {
						if found {
							p.removeAt(i)
							delete(model, k)
						}
					} else {
						p.put(k, step, h, i, found)
						model[k] = step
					}
					if p.size != len(model) {
						t.Fatal("point index size")
					}
					for key, value := range model {
						kh := p.hash(key)
						j, ok := p.find(key, kh)
						if !ok || p.slots[j].value != value {
							t.Fatal("collision-chain deletion")
						}
					}
				}
				p.clear()
				if _, ok := p.get(0); ok || p.size != 0 {
					t.Fatal("clear")
				}
			})
		}
	}
}

func TestPointIndexFloatingKeys(t *testing.T) {
	for _, noCache := range []bool{false, true} {
		t.Run(fmt.Sprintf("HashNoCache=%v", noCache), func(t *testing.T) {
			var p pointIndex[float64, int]
			p.init(!noCache)
			for i, k := range []float64{0, math.Copysign(0, -1), math.NaN(), math.Float64frombits(0xfff8000000000011)} {
				h := p.hash(k)
				pos, ok := p.find(k, h)
				p.put(k, i, h, pos, ok)
			}
			if p.size != 2 {
				t.Fatal("canonical float keys")
			}
			if v, ok := p.get(0); !ok || v != 1 {
				t.Fatal("signed zero")
			}
			if v, ok := p.get(math.NaN()); !ok || v != 3 {
				t.Fatal("NaN lookup")
			}
			if n := testing.AllocsPerRun(100, func() { p.get(math.NaN()); p.get(0) }); n != 0 {
				t.Fatal("hash lookup allocations", n)
			}
		})
	}
}

func TestHashNoCacheConfig(t *testing.T) {
	for _, noCache := range []bool{false, true} {
		cfg := Config{HashNoCache: noCache}
		tr := NewBPTree[int, int](&cfg)
		d := NewDictWithConfig[int, int](cfg)
		for round := 0; round < 2; round++ {
			for i := 0; i < 1000; i++ {
				tr.Put(i, i)
				d.Put(i, i)
			}
			if (len(tr.points.hashes) == 0) != noCache {
				t.Fatal("Tree must honor HashNoCache after growth and Clear")
			}
			if (len(d.index.points.hashes) == 0) != noCache {
				t.Fatal("Dict must honor HashNoCache after growth and Clear")
			}
			tr.Clear()
			d.Clear()
		}
	}
}
