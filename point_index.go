package bufftree

import (
	"cmp"
	"hash/maphash"
)

// A write-maintained point index: linear probing, optional full hashes, at most
// 50% occupancy, and backward-shift deletion. Metadata zero denotes an empty slot.
// The BP-tree remains the authoritative structure for ordered traversal.
type pointSlot[K cmp.Ordered, V any] struct {
	key   K
	value V
	leaf  *node[K, V]
}
type pointIndex[K cmp.Ordered, V any] struct {
	seed   maphash.Seed
	slots  []pointSlot[K, V]
	tags   []uint8
	hashes []uint64
	size   int
}

func (p *pointIndex[K, V]) init(keepHashes bool) {
	if len(p.slots) == 0 {
		p.seed = maphash.MakeSeed()
		p.slots = make([]pointSlot[K, V], 16)
		if keepHashes {
			p.hashes = make([]uint64, 16)
		} else {
			p.tags = make([]uint8, 16)
		}
	}
}
func (p *pointIndex[K, V]) hash(k K) uint64 {
	// Go's hasher randomizes NaNs. All NaNs are one key here, so give them a
	// canonical hash. maphash already hashes positive/negative zero equally.
	if k != k {
		return 0x9e3779b97f4a7c15
	}
	h := maphash.Comparable(p.seed, k)
	if h == 0 {
		h = 1
	}
	return h
}
func (p *pointIndex[K, V]) find(k K, h uint64) (int, bool) {
	mask := len(p.slots) - 1
	if len(p.hashes) != 0 {
		for i := int(h) & mask; ; i = (i + 1) & mask {
			stored := p.hashes[i]
			if stored == 0 {
				return i, false
			}
			if stored == h && equalKey(p.slots[i].key, k) {
				return i, true
			}
		}
	}
	tag := hashTag(h)
	for i := int(h) & mask; ; i = (i + 1) & mask {
		stored := p.tags[i]
		if stored == 0 {
			return i, false
		}
		if stored == tag && equalKey(p.slots[i].key, k) {
			return i, true
		}
	}
}

// Use seven high bits for the fingerprint; reserve zero for an empty slot.
func hashTag(h uint64) uint8 { return uint8(h>>57) + 1 }

func (p *pointIndex[K, V]) occupied(i int) bool {
	if len(p.hashes) != 0 {
		return p.hashes[i] != 0
	}
	return p.tags[i] != 0
}

func (p *pointIndex[K, V]) storeHash(i int, h uint64) {
	if len(p.hashes) != 0 {
		p.hashes[i] = h
	} else {
		p.tags[i] = hashTag(h)
	}
}
func (p *pointIndex[K, V]) get(k K) (V, bool) {
	if len(p.slots) != 0 {
		if i, ok := p.find(k, p.hash(k)); ok {
			return p.slots[i].value, true
		}
	}
	var zero V
	return zero, false
}
func (p *pointIndex[K, V]) grow() {
	old := p.slots
	oldTags, oldHashes := p.tags, p.hashes
	p.slots = make([]pointSlot[K, V], len(old)*2)
	if len(oldHashes) != 0 {
		p.hashes = make([]uint64, len(p.slots))
	} else {
		p.tags = make([]uint8, len(p.slots))
	}
	mask := len(p.slots) - 1
	for pos, s := range old {
		var h uint64
		if len(oldHashes) != 0 {
			h = oldHashes[pos]
			if h == 0 {
				continue
			}
		} else {
			if oldTags[pos] == 0 {
				continue
			}
			h = p.hash(s.key)
		}
		i := int(h) & mask
		for p.occupied(i) {
			i = (i + 1) & mask
		}
		p.slots[i] = s
		p.storeHash(i, h)
	}
}
func (p *pointIndex[K, V]) put(k K, v V, h uint64, i int, found bool) int {
	if !found {
		if (p.size+1)*2 > len(p.slots) {
			p.grow()
			i, _ = p.find(k, h)
		}
		p.size++
	}
	p.slots[i] = pointSlot[K, V]{key: k, value: v}
	p.storeHash(i, h)
	return i
}
func (p *pointIndex[K, V]) removeAt(i int) {
	mask := len(p.slots) - 1
	hole := i
	for j := (i + 1) & mask; p.occupied(j); j = (j + 1) & mask {
		var h uint64
		if len(p.hashes) != 0 {
			h = p.hashes[j]
		} else {
			h = p.hash(p.slots[j].key)
		}
		home := int(h) & mask
		if (j-home)&mask >= (j-hole)&mask {
			p.slots[hole] = p.slots[j]
			if len(p.hashes) != 0 {
				p.hashes[hole] = p.hashes[j]
			} else {
				p.tags[hole] = p.tags[j]
			}
			hole = j
		}
	}
	clear(p.slots[hole : hole+1])
	if len(p.hashes) != 0 {
		p.hashes[hole] = 0
	} else {
		p.tags[hole] = 0
	}
	p.size--
}
func (p *pointIndex[K, V]) clear() {
	clear(p.slots)
	clear(p.tags)
	clear(p.hashes)
	p.size = 0
}
