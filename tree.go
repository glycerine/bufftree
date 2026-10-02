package bufftree

import (
	"cmp"
	"iter"
	"slices"
)

// Tree is a BP-tree ordered by key. Floating-point NaNs compare equal to one
// another and sort after all other keys. The zero value is ready
// for use. Do not copy a Tree after its first use.
type Tree[K cmp.Ordered, V any] struct {
	root          *node[K, V]
	cfg           Config
	length        int
	version       uint64
	mapBuffers    [][]entry[K, V]
	rebuildBuffer []entry[K, V]
}
type node[K cmp.Ordered, V any] struct {
	min        K
	parent     *node[K, V]
	keys       []K
	children   []*node[K, V]
	leaf       *bpa[K, V]
	prev, next *node[K, V]
}

// NewBPTree creates a key-ordered BP-tree. A nil cfg selects the defaults.
// A non-nil cfg is copied; later changes to the caller's config do not affect
// the tree. Zero numeric fields select defaults. Invalid configurations panic.
func NewBPTree[K cmp.Ordered, V any](cfg *Config) *Tree[K, V] {
	var internal Config
	if cfg != nil {
		internal = *cfg
	}
	return &Tree[K, V]{cfg: internal.normalized()}
}
func (t *Tree[K, V]) Len() int { return t.length }
func (t *Tree[K, V]) Clear()   { t.root = nil; t.length = 0; t.version++ }
func (t *Tree[K, V]) findLeaf(k K) *node[K, V] {
	n := t.root
	for n != nil && n.leaf == nil {
		lo, hi := 0, len(n.keys)
		for lo < hi {
			mid := lo + (hi-lo)/2
			if !lessKey(k, n.keys[mid]) {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		n = n.children[lo]
	}
	return n
}
func (t *Tree[K, V]) firstLeaf() *node[K, V] {
	n := t.root
	for n != nil && n.leaf == nil {
		n = n.children[0]
	}
	return n
}

// Get returns the value for k, or the zero value of V if k is absent.
// Use Get2 to distinguish an absent key from a stored zero value.
func (t *Tree[K, V]) Get(k K) V {
	v, _ := t.Get2(k)
	return v
}

// Get2 returns the value for k and whether the key exists.
func (t *Tree[K, V]) Get2(k K) (V, bool) {
	if n := t.findLeaf(k); n != nil {
		return n.leaf.get(k)
	}
	var zero V
	return zero, false
}

// Put inserts or replaces a value, returning the old value and whether it existed.
// Replacing an existing value does not increase Len.
func (t *Tree[K, V]) Put(k K, v V) (V, bool) {
	if t.root == nil {
		t.cfg = t.cfg.normalized()
		t.root = &node[K, V]{min: k, leaf: newBPA[K, V](t.cfg, &t.rebuildBuffer)}
		if len(t.mapBuffers) == 0 {
			t.mapBuffers = [][]entry[K, V]{make([]entry[K, V], 0, t.root.leaf.capacity())}
		}
	}
	n := t.findLeaf(k)
	old, found := n.leaf.get(k)
	if found {
		n.leaf.overwrite(k, v)
		t.version++
		return old, true
	}
	if n.leaf.size == n.leaf.capacity() {
		right := t.splitLeaf(n)
		if compareKey(k, right.min) >= 0 {
			n = right
		}
	}
	n.leaf.size++
	t.length++
	n.leaf.writeLog(entry[K, V]{key: k, value: v})
	if lessKey(k, n.min) {
		n.min = k
		refreshUp(n.parent)
	}
	t.version++
	return old, found
}
func refresh[K cmp.Ordered, V any](n *node[K, V]) {
	if n.leaf != nil {
		return
	}
	n.min = n.children[0].min
	count := len(n.children) - 1
	if cap(n.keys) < count {
		n.keys = make([]K, count)
	} else {
		clear(n.keys)
		n.keys = n.keys[:count]
	}
	for i := 1; i < len(n.children); i++ {
		n.keys[i-1] = n.children[i].min
	}
}
func refreshUp[K cmp.Ordered, V any](n *node[K, V]) {
	for n != nil {
		refresh(n)
		n = n.parent
	}
}
func childIndex[K cmp.Ordered, V any](p, n *node[K, V]) int {
	for i, c := range p.children {
		if c == n {
			return i
		}
	}
	panic("bufftree: broken parent link")
}
func (t *Tree[K, V]) splitLeaf(n *node[K, V]) *node[K, V] {
	es := n.leaf.collect()
	mid := len(es) / 2
	right := &node[K, V]{min: es[mid].key, leaf: newBPA[K, V](t.cfg, &t.rebuildBuffer), prev: n, next: n.next}
	n.leaf.load(es[:mid])
	n.min = es[0].key
	right.leaf.load(es[mid:])
	if n.next != nil {
		n.next.prev = right
	}
	n.next = right
	t.insertSibling(n, right)
	return right
}

func (t *Tree[K, V]) insertSibling(left, right *node[K, V]) {
	for {
		p := left.parent
		if p == nil {
			p = &node[K, V]{children: []*node[K, V]{left, right}}
			left.parent, right.parent = p, p
			refresh(p)
			t.root = p
			return
		}
		i := childIndex(p, left) + 1
		p.children = slices.Insert(p.children, i, right)
		right.parent = p
		refresh(p)
		if len(p.children) <= t.cfg.Fanout {
			refreshUp(p.parent)
			return
		}
		mid := len(p.children) / 2
		r := &node[K, V]{children: append([]*node[K, V](nil), p.children[mid:]...)}
		clear(p.children[mid:])
		p.children = p.children[:mid]
		for _, c := range r.children {
			c.parent = r
		}
		refresh(p)
		refresh(r)
		left, right = p, r
	}
}

// Del2 removes a key and returns its previous value and whether it existed.
func (t *Tree[K, V]) Del2(k K) (V, bool) {
	n := t.findLeaf(k)
	if n == nil {
		var zero V
		return zero, false
	}
	old, found := n.leaf.del(k)
	if !found {
		return old, false
	}
	t.length--
	t.version++
	if n == t.root && n.leaf.size == 0 {
		t.root = nil
		return old, true
	}
	changedMin := n.leaf.size > 0 && equalKey(k, n.min)
	if changedMin {
		cur := n.leaf.cursor(k, true, true)
		e, _ := cur.next()
		n.min = e.key
	}
	if n != t.root && t.underfull(n) {
		t.rebalance(n)
	} else if changedMin {
		refreshUp(n.parent)
	}
	return old, true
}

// Del is the boolean-only form of Del2.
func (t *Tree[K, V]) Del(k K) bool { _, ok := t.Del2(k); return ok }
func (t *Tree[K, V]) underfull(n *node[K, V]) bool {
	if n.leaf != nil {
		return n.leaf.size < n.leaf.capacity()/2
	}
	return len(n.children) < (t.cfg.Fanout+1)/2
}
func (t *Tree[K, V]) rebalance(n *node[K, V]) {
	for n != t.root && t.underfull(n) {
		p := n.parent
		i := childIndex(p, n)
		var left, right *node[K, V]
		if i > 0 {
			left, right = p.children[i-1], n
		} else {
			left, right = n, p.children[1]
		}
		merge := false
		if n.leaf != nil {
			es := append(left.leaf.collect(), right.leaf.collect()...)
			merge = len(es) <= left.leaf.capacity()
			if merge {
				left.leaf.load(es)
				left.min = es[0].key
				left.next = right.next
				if right.next != nil {
					right.next.prev = left
				}
				right.prev, right.next = nil, nil
			} else {
				mid := len(es) / 2
				left.leaf.load(es[:mid])
				right.leaf.load(es[mid:])
				left.min, right.min = es[0].key, es[mid].key
			}
		} else {
			cs := append(append([]*node[K, V](nil), left.children...), right.children...)
			merge = len(cs) <= t.cfg.Fanout
			if merge {
				left.children = cs
				for _, c := range cs {
					c.parent = left
				}
				refresh(left)
			} else {
				mid := len(cs) / 2
				left.children, right.children = cs[:mid:mid], cs[mid:]
				for _, c := range left.children {
					c.parent = left
				}
				for _, c := range right.children {
					c.parent = right
				}
				refresh(left)
				refresh(right)
			}
		}
		if !merge {
			refreshUp(p)
			return
		}
		ri := childIndex(p, right)
		p.children = slices.Delete(p.children, ri, ri+1)
		right.parent = nil
		refresh(p)
		n = p
	}
	if t.root.leaf == nil && len(t.root.children) == 1 {
		t.root = t.root.children[0]
		t.root.parent = nil
	}
	refreshUp(n.parent)
}

// Iterator holds one current key/value. Call Next before reading Key or Value.
// Deleting the current key (through the tree or iterator) is supported. Any
// mutation triggers a seek strictly beyond the last returned key; newly inserted
// keys ahead of it may be visited. An exhausted iterator stays exhausted until
// Seek is called. Iteration is live, not a snapshot.
type Iterator[K cmp.Ordered, V any] struct {
	tree                          *Tree[K, V]
	leaf                          *node[K, V]
	cursor                        bpaCursor[K, V]
	key                           K
	value                         V
	start                         K
	bounded, started, valid, done bool
	version                       uint64
}

func (t *Tree[K, V]) Iter() *Iterator[K, V] { return &Iterator[K, V]{tree: t} }
func (t *Tree[K, V]) IterFrom(start K) *Iterator[K, V] {
	return &Iterator[K, V]{tree: t, start: start, bounded: true}
}
func (it *Iterator[K, V]) position(k K, bounded, strict bool) {
	if bounded {
		it.leaf = it.tree.findLeaf(k)
	} else {
		it.leaf = it.tree.firstLeaf()
	}
	if it.leaf != nil {
		it.cursor = it.leaf.leaf.cursor(k, bounded, strict)
	}
	it.version = it.tree.version
}
func (it *Iterator[K, V]) Next() bool {
	if it.done {
		return false
	}
	if !it.started {
		it.position(it.start, it.bounded, false)
		it.started = true
	} else if it.version != it.tree.version {
		it.position(it.key, true, true)
	}
	for it.leaf != nil {
		if e, ok := it.cursor.next(); ok {
			it.key, it.value, it.valid = e.key, e.value, true
			return true
		}
		it.leaf = it.leaf.next
		if it.leaf != nil {
			it.cursor = it.leaf.leaf.cursor(it.start, false, false)
		}
	}
	it.valid, it.done = false, true
	var zero V
	it.value = zero
	it.cursor = bpaCursor[K, V]{}
	return false
}

// Seek positions the iterator at the first key >= start, returning its validity.
// Read Key/Value immediately after a successful Seek; Next then advances.
func (it *Iterator[K, V]) Seek(start K) bool {
	it.start, it.bounded, it.started, it.valid, it.done = start, true, false, false, false
	return it.Next()
}
func (it *Iterator[K, V]) Valid() bool { return it.valid }
func (it *Iterator[K, V]) Key() K      { return it.key }
func (it *Iterator[K, V]) Value() V    { return it.value }
func (it *Iterator[K, V]) Del() bool   { _, ok := it.Del2(); return ok }

// Del2 deletes the current key, returning its old value and whether it existed.
func (it *Iterator[K, V]) Del2() (V, bool) {
	if it.valid {
		return it.tree.Del2(it.key)
	}
	var zero V
	return zero, false
}

// All can be used with Go's range-over-function syntax. Deleting yielded keys
// is supported; breaking the loop stops traversal immediately.
func (t *Tree[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		it := t.Iter()
		for it.Next() {
			if !yield(it.Key(), it.Value()) {
				return
			}
		}
	}
}

// Range visits keys in [start,end) in key order. Return false to stop.
func (t *Tree[K, V]) Range(start, end K, visit func(K, V) bool) {
	if !lessKey(start, end) {
		return
	}
	t.walk(start, end, true, int(^uint(0)>>1), visit)
}

// Scan visits at most length entries with keys >= start in key order.
func (t *Tree[K, V]) Scan(start K, length int, visit func(K, V) bool) {
	if length <= 0 {
		return
	}
	var end K
	t.walk(start, end, false, length, visit)
}

// Keep the common scan path local; only reconstruct its position when the
// callback mutates the tree. Explicit iterators retain their own state.
func (t *Tree[K, V]) walk(start, end K, bounded bool, length int, visit func(K, V) bool) {
	n := t.findLeaf(start)
	if n == nil {
		return
	}
	c := n.leaf.cursor(start, true, false)
	version := t.version
scan:
	for length > 0 {
		// Consume a contiguous part of a sorted block directly when no log
		// version can shadow it. This avoids cursor calls for every record.
		if c.hasBase && (c.logPos == c.p.logN || lessKey(c.base.key, c.p.data[c.logPos].key)) {
			e := c.base
			if bounded && !lessKey(e.key, end) {
				return
			}
			if !e.dead {
				length--
				if !visit(e.key, e.value) || length == 0 {
					return
				}
				if version != t.version {
					n = t.findLeaf(e.key)
					if n == nil {
						return
					}
					c = n.leaf.cursor(e.key, true, true)
					version = t.version
					continue scan
				}
			}
			block := c.block[c.blockPos:]
			count := min(length, len(block))
			if c.logPos < c.p.logN {
				count = min(count, lower(block, c.p.data[c.logPos].key, false))
			}
			if bounded {
				count = min(count, lower(block, end, false))
			}
			for _, e := range block[:count] {
				length--
				if !visit(e.key, e.value) || length == 0 {
					return
				}
				if version != t.version {
					n = t.findLeaf(e.key)
					if n == nil {
						return
					}
					c = n.leaf.cursor(e.key, true, true)
					version = t.version
					continue scan
				}
			}
			c.blockPos += count
			c.advanceBase()
			continue scan
		}
		e, ok := c.next()
		if !ok {
			n = n.next
			if n == nil {
				return
			}
			c = n.leaf.cursor(start, false, false)
			continue
		}
		if bounded && !lessKey(e.key, end) {
			return
		}
		length--
		if !visit(e.key, e.value) {
			return
		}
		if version != t.version {
			n = t.findLeaf(e.key)
			if n == nil {
				return
			}
			c = n.leaf.cursor(e.key, true, true)
			version = t.version
		}
	}
}

// MapRange visits [start,end) in unspecified order without sorting the blocks.
// The visitor may delete its current key. Other mutations during MapRange are
// unsupported; use Range for general live iteration. A per-leaf snapshot of
// matching entries preserves traversal even if deleting a key merges leaves.
func (t *Tree[K, V]) MapRange(start, end K, visit func(K, V) bool) {
	if compareKey(start, end) >= 0 {
		return
	}
	n := t.findLeaf(start)
	if n == nil {
		return
	}
	var scratch []entry[K, V]
	if count := len(t.mapBuffers); count != 0 {
		scratch = t.mapBuffers[count-1]
		t.mapBuffers[count-1] = nil
		t.mapBuffers = t.mapBuffers[:count-1]
	} else {
		scratch = make([]entry[K, V], 0, n.leaf.capacity())
	}
	used := 0
	defer func() { clear(scratch[:used]); t.mapBuffers = append(t.mapBuffers, scratch[:0]) }()
	for n != nil {
		var next K
		hasNext := n.next != nil
		if hasNext {
			next = n.next.min
		}
		scratch = n.leaf.mapEntries(start, end, scratch[:0])
		used = max(used, len(scratch))
		for _, e := range scratch {
			if !visit(e.key, e.value) {
				return
			}
		}
		if !hasNext || compareKey(next, end) >= 0 {
			return
		}
		start = next
		n = t.findLeaf(start)
	}
}
