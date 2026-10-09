package bufftree

import (
	"cmp"
	"iter"
	"slices"
	"sync/atomic"
)

// Tree is a BP-tree ordered by key. Floating-point NaNs compare equal to one
// another and sort after all other keys. The zero value is ready
// for use. Do not copy a Tree after its first use.
// Any goroutine may read or write; concurrent writes wait for exclusive access.
// Iteration is live, not a snapshot. Callbacks run without tree locks and may
// delete entries or call other tree methods.
// Values are copied, not deep-cloned; callers synchronize mutable referenced data.
type Tree[K cmp.Ordered, V any] struct {
	mu            SingleWriterRWMutex
	root          *node[K, V]
	cfg           Config
	length        int
	pending       *bpa[K, V]
	version       atomic.Uint64
	mapBuffers    [][]entry[K, V]
	rebuildBuffer []entry[K, V]
}
type node[K cmp.Ordered, V any] struct {
	// Keep the fields used by internal descent together, ahead of metadata
	// needed only during mutation or leaf-to-leaf traversal.
	leaf       *bpa[K, V]
	keys       []K
	children   []*node[K, V]
	min        K
	parent     *node[K, V]
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

// Len returns the exact number of keys. The first call after writes reconciles
// buffered membership in changed leaves; subsequent calls take constant time.
// Reconciliation does not move records or invalidate live iterator positions.
func (t *Tree[K, V]) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	for t.pending != nil {
		t.reconcile(t.pending)
	}
	return t.length
}
func (t *Tree[K, V]) reconcile(p *bpa[K, V]) {
	n := p.resolveSize()
	t.length += n - p.accounted
	p.accounted = n
	if !p.dirty {
		return
	}
	if p.prevDirty != nil {
		p.prevDirty.nextDirty = p.nextDirty
	} else {
		t.pending = p.nextDirty
	}
	if p.nextDirty != nil {
		p.nextDirty.prevDirty = p.prevDirty
	}
	p.dirty, p.prevDirty, p.nextDirty = false, nil, nil
}
func (t *Tree[K, V]) markDirty(p *bpa[K, V]) {
	if !p.dirty {
		if t.pending != nil {
			t.pending.prevDirty = p
		}
		p.dirty, p.nextDirty, t.pending = true, t.pending, p
	}
}
func (t *Tree[K, V]) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.root, t.pending = nil, nil
	t.length = 0
	t.version.Add(1)
}

func (t *Tree[K, V]) findLeaf(k K) *node[K, V] {
	n := t.root
	for n != nil && n.leaf == nil {
		lo, width := 0, len(n.keys)
		for width > 1 {
			half := width / 2
			if !lessKey(k, n.keys[lo+half-1]) {
				lo += half
			}
			width -= half
		}
		if width > 0 && !lessKey(k, n.keys[lo]) {
			lo++
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
	t.mu.RLock()
	defer t.mu.RUnlock()
	if n := t.findLeaf(k); n != nil {
		return n.leaf.get(k)
	}
	var zero V
	return zero, false
}

// Put inserts or replaces a value. Writes are buffered without looking up the
// previous value in the leaf's blocks.
func (t *Tree[K, V]) Put(k K, v V) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.root == nil {
		t.cfg = t.cfg.normalized()
		t.root = &node[K, V]{min: k, leaf: newBPA[K, V](t.cfg, &t.rebuildBuffer)}
		if len(t.mapBuffers) == 0 {
			t.mapBuffers = [][]entry[K, V]{make([]entry[K, V], 0, t.root.leaf.capacity())}
		}
	}
	n := t.findLeaf(k)
	t.markDirty(n.leaf)
	if !n.leaf.trySet(k, v) {
		// Buffered duplicates can overestimate occupancy by at most logN.
		// Resolve them before deciding whether a real split is necessary.
		n.leaf.flush()
		if n.leaf.size == n.leaf.capacity() {
			if loc := n.leaf.baseLocation(k); loc >= 0 && !n.leaf.isDead(loc) {
				n.leaf.updateValue(loc, v)
				t.version.Add(1)
				return
			}
			right := t.splitLeaf(n)
			if !lessKey(k, right.min) {
				n = right
			}
		}
		n.leaf.trySet(k, v)
	}
	if lessKey(k, n.min) {
		n.min = k
		refreshUp(n.parent)
	}
	t.version.Add(1)
}
func refresh[K cmp.Ordered, V any](n *node[K, V]) {
	if n.leaf != nil {
		return
	}
	n.min = n.children[0].min
	count := len(n.children) - 1
	if count < len(n.keys) {
		clear(n.keys[count:])
	} else {
		n.keys = slices.Grow(n.keys, count-len(n.keys))
	}
	n.keys = n.keys[:count]
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
	// Like redistribution, splitting has no callbacks and can borrow the
	// tree's scratch space. Both loads copy their input before it is cleared.
	es := n.leaf.collectInto(t.rebuildBuffer[:0])
	mid := len(es) / 2
	right := &node[K, V]{min: es[mid].key, leaf: newBPA[K, V](t.cfg, &t.rebuildBuffer), prev: n, next: n.next}
	n.leaf.load(es[:mid])
	n.min = es[0].key
	right.leaf.load(es[mid:])
	t.markDirty(right.leaf)
	clear(es)
	t.rebuildBuffer = es[:0]
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

// Del removes k. Deleting an absent key has no effect.
func (t *Tree[K, V]) Del(k K) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.findLeaf(k)
	if n == nil {
		return
	}
	if !n.leaf.del(k) {
		return
	}
	t.markDirty(n.leaf)
	t.version.Add(1)
	if n == t.root && n.leaf.size == 0 {
		n.leaf.load(nil)
		t.reconcile(n.leaf)
		t.root = nil
		return
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
}
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
			t.markDirty(left.leaf)
			t.markDirty(right.leaf)
			es := append(left.leaf.collect(), right.leaf.collect()...)
			merge = len(es) <= left.leaf.capacity()
			if merge {
				left.leaf.load(es)
				right.leaf.load(nil)
				t.reconcile(right.leaf)
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
// Distinct iterators may run concurrently; an individual iterator must not be
// used concurrently. No tree lock is retained between calls to Next or Seek.
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
		it.leaf.leaf.prepareOrdered()
		it.cursor = it.leaf.leaf.cursor(k, bounded, strict)
	}
	it.version = it.tree.version.Load()
}
func (it *Iterator[K, V]) Next() bool {
	if it.done {
		return false
	}
	it.tree.mu.Lock()
	defer it.tree.mu.Unlock()
	if !it.started {
		it.position(it.start, it.bounded, false)
		it.started = true
	} else if it.version != it.tree.version.Load() {
		it.position(it.key, true, true)
	}
	for it.leaf != nil {
		if e, ok := it.cursor.next(); ok {
			it.key, it.value, it.valid = e.key, e.value, true
			return true
		}
		it.leaf = it.leaf.next
		if it.leaf != nil {
			it.leaf.leaf.prepareOrdered()
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

// Del deletes the current key. An unpositioned iterator has no effect.
func (it *Iterator[K, V]) Del() {
	if it.valid {
		it.tree.Del(it.key)
	}
}

// All can be used with Go's range-over-function syntax. Deleting yielded keys
// is supported; breaking the loop stops traversal immediately.
func (t *Tree[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		t.mu.RLock()
		n := t.firstLeaf()
		var start K
		if n != nil {
			start = n.min
		}
		t.mu.RUnlock()
		if n != nil {
			var end K
			t.walk(start, end, false, int(^uint(0)>>1), yield)
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

// Long scans settle a leaf's log once instead of repeatedly merging its runs.
// This can move records, so other live cursors must notice the new version.
// Only compact a previously scanned leaf: its first scan should not pay both
// initial sorting and eager log redistribution. Avoid preparing a narrow Range
// or allocating scratch for a tiny new tree.
func (t *Tree[K, V]) prepareScan(n *node[K, V], start, end K, seek, strict, bounded bool, length int) scanCursor[K, V] {
	if t.shouldFlushScan(n, end, bounded, length) {
		n.leaf.flush()
		t.version.Add(1)
	}
	n.leaf.scanReady = true
	n.leaf.prepareOrdered()
	return n.leaf.scanCursor(start, seek, strict)
}

func (t *Tree[K, V]) shouldFlushScan(n *node[K, V], end K, bounded bool, length int) bool {
	return n.leaf.scanReady && n.leaf.logN > 0 && length >= n.leaf.size && cap(t.rebuildBuffer) >= n.leaf.size &&
		(!bounded || (n.next != nil && !lessKey(end, n.next.min)))
}

// scanBatchState borrows tree storage only while the tree lock is held. A
// version mismatch invalidates every borrowed slice before it can be accessed.
type scanBatchState[K cmp.Ordered, V any] struct {
	n       *node[K, V]
	c       scanCursor[K, V]
	run     []entry[K, V]
	version uint64
	ready   bool
	started bool
}

// Copy bounded batches so no lock crosses user code. In particular, deletion,
// nested scans, early return, and panicking callbacks cannot strand a read hold.
// These are shallow key/value copies, not COW leaves or a whole-tree snapshot.
func (t *Tree[K, V]) walk(start, end K, bounded bool, length int, visit func(K, V) bool) {
	var state scanBatchState[K, V]
	var storage [128]entry[K, V]
	strict := false
	for length > 0 {
		batch := t.scanBatch(&state, start, end, bounded, strict, length, storage[:0])
		if len(batch) == 0 {
			return
		}
		for _, e := range batch {
			if !visit(e.key, e.value) {
				return
			}
			length--
			start, strict = e.key, true
			if state.version != t.version.Load() {
				// Do not deliver copied entries invalidated by a callback.
				// The next batch re-seeks strictly beyond the last visit.
				break
			}
		}
	}
}

func (t *Tree[K, V]) scanBatch(s *scanBatchState[K, V], start, end K, bounded, strict bool, length int, out []entry[K, V]) []entry[K, V] {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !s.started || s.version != t.version.Load() {
		s.n, s.run, s.ready = t.findLeaf(start), nil, false
		s.started, s.version = true, t.version.Load()
	}
	limit := min(length, cap(out))
	for s.n != nil {
		if !s.ready {
			n := s.n
			if !n.leaf.ordered || !n.leaf.scanReady || t.shouldFlushScan(n, end, bounded, length) {
				version := t.version.Load()
				t.mu.RUnlock()
				t.mu.Lock()
				// No upgrade: another writer may have changed topology during
				// the unlocked interval. Discard the old node in that case.
				if version != t.version.Load() {
					s.n = t.findLeaf(start)
				}
				if s.n != nil {
					s.c = t.prepareScan(s.n, start, end, true, strict, bounded, length)
				}
				t.mu.Downgrade()
				if s.n == nil {
					return out
				}
			} else {
				s.c = n.leaf.scanCursor(start, true, strict)
			}
			s.ready, s.version = true, t.version.Load()
		}
		if len(s.run) == 0 {
			s.run = s.c.nextRun()
			if len(s.run) == 0 {
				s.n, s.ready = s.n.next, false
				// Do not carry a partly copied batch across an unlocked
				// preparation of the next leaf.
				if len(out) != 0 {
					return out
				}
				continue
			}
		}
		count := min(limit-len(out), len(s.run))
		if bounded && !lessKey(s.run[count-1].key, end) {
			count = lower(s.run[:count], end, false)
			out = append(out, s.run[:count]...)
			s.n, s.run = nil, nil
			return out
		}
		out = append(out, s.run[:count]...)
		s.run = s.run[count:]
		if len(out) == limit {
			return out
		}
	}
	return out
}

// MapRange visits [start,end) in unspecified order without sorting the blocks.
// The visitor may delete its current key. Other mutations during MapRange are
// unsupported; use Range for general live iteration. A per-leaf snapshot of
// matching entries preserves traversal even if deleting a key merges leaves.
// Concurrent external writes may cause entries to be missed or values to be
// stale, but memory access is synchronized. Callbacks run without tree locks.
func (t *Tree[K, V]) MapRange(start, end K, visit func(K, V) bool) {
	if compareKey(start, end) >= 0 {
		return
	}
	t.mu.Lock()
	n := t.findLeaf(start)
	if n == nil {
		t.mu.Unlock()
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
	t.mu.Unlock()
	used := 0
	defer func() {
		clear(scratch[:used])
		t.mu.Lock()
		t.mapBuffers = append(t.mapBuffers, scratch[:0])
		t.mu.Unlock()
	}()
	for {
		t.mu.Lock()
		n = t.findLeaf(start)
		if n == nil {
			t.mu.Unlock()
			return
		}
		var next K
		hasNext := n.next != nil
		if hasNext {
			next = n.next.min
		}
		scratch = n.leaf.mapEntries(start, end, scratch[:0])
		used = max(used, len(scratch))
		t.mu.Unlock()
		for _, e := range scratch {
			if !visit(e.key, e.value) {
				return
			}
		}
		if !hasNext || compareKey(next, end) >= 0 {
			return
		}
		start = next
	}
}
