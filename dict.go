package bufftree

import (
	"cmp"
	"iter"
)

// Dict uses a BP-tree for point lookups and a linked sequence for insertion-
// ordered traversal. Updating a value preserves its position; deleting and
// reinserting a key appends it. Its zero value is ready for use. Do not copy a
// Dict after its first use. Like Tree, it requires external synchronization.
type Dict[K cmp.Ordered, V any] struct {
	index      Tree[K, *dictItem[K, V]]
	head, tail *dictItem[K, V]
	end        *dictItem[K, V]
	epoch      uint64
}
type dictItem[K cmp.Ordered, V any] struct {
	key        K
	value      V
	prev, next *dictItem[K, V]
	live       bool
}

// NewDict creates an insertion-ordered dictionary. A nil cfg selects the defaults.
// A non-nil cfg is copied; later changes to the caller's config do not affect
// the dictionary. Zero numeric fields select defaults. Invalid configurations panic.
func NewDict[K cmp.Ordered, V any](cfg *Config) *Dict[K, V] {
	var internal Config
	if cfg != nil {
		internal = *cfg
	}
	return &Dict[K, V]{index: Tree[K, *dictItem[K, V]]{cfg: internal.normalized()}}
}
func (d *Dict[K, V]) Len() int { return d.index.Len() }

// Get returns the value for k, or the zero value of V if k is absent.
// Use Get2 to distinguish an absent key from a stored zero value.
func (d *Dict[K, V]) Get(k K) V {
	v, _ := d.Get2(k)
	return v
}

// Get2 returns the value for k and whether the key exists.
func (d *Dict[K, V]) Get2(k K) (V, bool) {
	if e, ok := d.index.Get2(k); ok {
		return e.value, true
	}
	var zero V
	return zero, false
}

// Put inserts or replaces a value.
// Replacing a value preserves its position; a new key is appended at the end.
func (d *Dict[K, V]) Put(k K, v V) {
	if e, ok := d.index.Get2(k); ok {
		e.value = v
		return
	}
	// Promote the current end marker into the new entry. Iterators waiting at
	// that marker will see the append, even if their previous entry was deleted.
	if d.end == nil {
		d.end = &dictItem[K, V]{}
	}
	e := d.end
	end := &dictItem[K, V]{}
	d.index.Put(k, e)
	*e = dictItem[K, V]{key: k, value: v, prev: d.tail, next: end, live: true}
	if d.tail != nil {
		d.tail.next = e
	} else {
		d.head = e
	}
	d.tail = e
	d.end = end
}

// Del removes k. Deleting an absent key has no effect.
func (d *Dict[K, V]) Del(k K) {
	e, ok := d.index.Get2(k)
	if !ok {
		return
	}
	d.index.Del(k)
	if e.prev != nil {
		e.prev.next = e.next
	} else if e.next == d.end {
		d.head = nil
	} else {
		d.head = e.next
	}
	if e.next != d.end {
		e.next.prev = e.prev
	} else {
		d.tail = e.prev
	}
	var zero V
	e.value = zero
	e.prev, e.live = nil, false
	// Keep the removed node's successor so an iterator already pointing to it
	// can skip it. Live nodes do not retain removed nodes.
}
func (d *Dict[K, V]) Clear() {
	d.index.Clear()
	d.head, d.tail, d.end = nil, nil, nil
	d.epoch++
}

// DictIterator visits surviving entries in insertion order. Deletions, including
// deleting current or upcoming entries, are supported. Entries appended before
// the iterator reaches its end are visited, including after deleting the tail.
// Updates to upcoming values are visible. Once Next returns false, the iterator
// remains exhausted. Clear ends existing iterators. Call Next before Key/Value.
type DictIterator[K cmp.Ordered, V any] struct {
	dict        *Dict[K, V]
	next        *dictItem[K, V]
	key         K
	value       V
	epoch       uint64
	started     bool
	valid, done bool
}

func (d *Dict[K, V]) Iter() *DictIterator[K, V] {
	return &DictIterator[K, V]{dict: d, epoch: d.epoch}
}
func (it *DictIterator[K, V]) Next() bool {
	if it.done {
		return false
	}
	if !it.started {
		it.next = it.dict.head
		it.started = true
	}
	if it.epoch == it.dict.epoch {
		for it.next != nil {
			e := it.next
			it.next = e.next
			if e.live {
				it.key, it.value, it.valid = e.key, e.value, true
				return true
			}
		}
	}
	it.valid, it.done = false, true
	it.next = nil
	var zero V
	it.value = zero
	return false
}
func (it *DictIterator[K, V]) Valid() bool { return it.valid }
func (it *DictIterator[K, V]) Key() K      { return it.key }
func (it *DictIterator[K, V]) Value() V    { return it.value }

// Del deletes the current key. An unpositioned iterator has no effect.
func (it *DictIterator[K, V]) Del() {
	if it.valid {
		it.dict.Del(it.key)
	}
}
func (d *Dict[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		it := d.Iter()
		for it.Next() {
			if !yield(it.Key(), it.Value()) {
				return
			}
		}
	}
}
