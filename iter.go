package bufftree

import (
	"cmp"
	"iter"
)

// SearchModifier selects comparator-relative positioning. NaNs compare equal
// to each other and after all other keys; signed zeros compare equal.
type SearchModifier int

const (
	Exact SearchModifier = iota
	GTE
	LTE
	GT
	LT
)

type KV[K cmp.Ordered, V any] struct {
	Key   K
	Value V
}

// KVcloser owns a single copied result, valid until Close or transaction end.
// It does not own a database lock. Do not copy resource handles.
type KVcloser[K cmp.Ordered, V any] struct {
	KV[K, V]
	s *txState[K, V]
}

func (r *KVcloser[K, V]) Close() {
	if r == nil || r.s == nil {
		return
	}
	delete(r.s.results, r)
	r.s = nil
	r.KV = KV[K, V]{}
}

// readCursor merges sorted log and base streams in either direction, without
// writing any leaf storage. Only writer-side preparation may sort records.
type readCursor[K cmp.Ordered, V any] struct {
	p          *bpa[K, V]
	base       []entry[K, V]
	block, log int
	reverse    bool
}

func (p *bpa[K, V]) readBlock(i int) []entry[K, V] {
	start := p.cfg.LogSize + p.cfg.NumBlocks + i*p.cfg.BlockSize
	end := start + 1 + p.counts[i]
	if p.isDead(p.cfg.LogSize + i) {
		start++
	}
	return p.data[start:end]
}
func (p *bpa[K, V]) readCursor(k K, bounded, strict, reverse bool) readCursor[K, V] {
	c := readCursor[K, V]{p: p, reverse: reverse}
	if reverse {
		c.log = p.logN - 1
		c.block = p.headerN - 1
		if bounded {
			c.log = lower(p.log(), k, !strict) - 1
			c.block = p.partition(k)
		}
		if c.block >= 0 {
			c.base = p.readBlock(c.block)
			c.block--
			if bounded {
				c.base = c.base[:lower(c.base, k, !strict)]
			}
		}
	} else if bounded {
		c.log = lower(p.log(), k, strict)
		c.block = max(0, p.partition(k))
		if c.block < p.headerN {
			c.base = p.readBlock(c.block)
			c.block++
			c.base = c.base[lower(c.base, k, strict):]
		}
	}
	return c
}

// nextRun returns an ascending slice. Reverse consumers read it back to front.
func (c *readCursor[K, V]) nextRun() []entry[K, V] {
	p := c.p
	for {
		for len(c.base) == 0 && c.block >= 0 && c.block < p.headerN {
			c.base = p.readBlock(c.block)
			if c.reverse {
				c.block--
			} else {
				c.block++
			}
		}
		hasLog := c.log >= 0 && c.log < p.logN
		basePos := 0
		if c.reverse {
			basePos = len(c.base) - 1
		}
		takeLog := hasLog && len(c.base) == 0
		if hasLog && len(c.base) > 0 {
			cmp := compareKey(p.data[c.log].key, c.base[basePos].key)
			takeLog = cmp <= 0
			if c.reverse {
				takeLog = cmp >= 0
			}
		}
		if takeLog {
			i := c.log
			if c.reverse {
				c.log--
			} else {
				c.log++
			}
			if len(c.base) > 0 && equalKey(p.data[i].key, c.base[basePos].key) {
				if c.reverse {
					c.base = c.base[:basePos]
				} else {
					c.base = c.base[1:]
				}
			}
			if !p.isDead(i) {
				return p.data[i : i+1]
			}
			continue
		}
		if len(c.base) == 0 {
			return nil
		}
		run := c.base
		if c.reverse {
			if hasLog {
				run = run[lower(run, p.data[c.log].key, true):]
			}
			c.base = c.base[:len(c.base)-len(run)]
		} else {
			if hasLog {
				run = run[:lower(run, p.data[c.log].key, false)]
			}
			c.base = c.base[len(run):]
		}
		return run
	}
}

// Iter streams within its transaction. New iterators are unpositioned; Seek
// positions immediately. Next/Prev on an invalid iterator do nothing. Its
// current KV is a saved observation, even if a WriteTx subsequently deletes it.
// Do not copy Iter or share it between concurrent goroutines.
type Iter[K cmp.Ordered, V any] struct {
	s                     *txState[K, V]
	leaf                  *node[K, V]
	cursor                readCursor[K, V]
	run                   []entry[K, V]
	current               KV[K, V]
	anchor                K
	epoch                 uint64
	reverse, valid, stale bool
}

func (r *txRead[K, V]) NewIter() *Iter[K, V] {
	r.mustOpen()
	it := &Iter[K, V]{s: r.s}
	if r.s.iters == nil {
		r.s.iters = make(map[*Iter[K, V]]struct{})
	}
	r.s.iters[it] = struct{}{}
	return it
}
func (it *Iter[K, V]) Close() {
	if it == nil || it.s == nil {
		return
	}
	delete(it.s.iters, it)
	*it = Iter[K, V]{}
}
func (it *Iter[K, V]) Valid() bool { return it != nil && it.s != nil && it.valid }
func (it *Iter[K, V]) KV() *KV[K, V] {
	if !it.Valid() {
		return nil
	}
	return &it.current
}
func (it *Iter[K, V]) Key() K {
	if it.Valid() {
		return it.current.Key
	}
	var z K
	return z
}
func (it *Iter[K, V]) Value() V {
	if it.Valid() {
		return it.current.Value
	}
	var z V
	return z
}
func (it *Iter[K, V]) Seek(k K)   { it.seek(k, true, false, false) }
func (it *Iter[K, V]) SeekFirst() { var k K; it.seek(k, false, false, false) }
func (it *Iter[K, V]) SeekLast()  { var k K; it.seek(k, false, false, true) }
func (it *Iter[K, V]) seek(k K, bounded, strict, reverse bool) {
	if it == nil || it.s == nil {
		return
	}
	c := &it.s.db.core
	it.reverse = reverse
	it.run = nil
	it.stale = false
	if bounded {
		it.leaf = c.findLeaf(k)
	} else {
		it.leaf = c.root
		for it.leaf != nil && it.leaf.leaf == nil {
			i := 0
			if reverse {
				i = len(it.leaf.children) - 1
			}
			it.leaf = it.leaf.children[i]
		}
	}
	if it.leaf != nil {
		it.s.prepare(it.leaf.leaf)
		it.cursor = it.leaf.leaf.readCursor(k, bounded, strict, reverse)
	}
	it.epoch = it.s.epoch
	it.advance()
}
func (it *Iter[K, V]) Next() { it.move(false) }
func (it *Iter[K, V]) Prev() { it.move(true) }
func (it *Iter[K, V]) move(reverse bool) {
	if !it.Valid() {
		return
	}
	// Check BEFORE dereferencing the old node, cursor, or borrowed run.
	if it.stale || it.epoch != it.s.epoch || it.reverse != reverse {
		it.seek(it.anchor, true, true, reverse)
		return
	}
	it.advance()
}
func (it *Iter[K, V]) advance() {
	for it.leaf != nil {
		if len(it.run) == 0 {
			it.run = it.cursor.nextRun()
		}
		if len(it.run) > 0 {
			var e entry[K, V]
			if it.reverse {
				i := len(it.run) - 1
				e = it.run[i]
				it.run = it.run[:i]
			} else {
				e = it.run[0]
				it.run = it.run[1:]
			}
			it.current = KV[K, V]{e.key, e.value}
			it.anchor = e.key
			it.valid = true
			return
		}
		if it.reverse {
			it.leaf = it.leaf.prev
		} else {
			it.leaf = it.leaf.next
		}
		if it.leaf != nil {
			it.s.prepare(it.leaf.leaf)
			var k K
			it.cursor = it.leaf.leaf.readCursor(k, false, false, it.reverse)
			it.epoch = it.s.epoch
		}
	}
	it.valid = false
	it.current = KV[K, V]{}
	it.cursor = readCursor[K, V]{}
	it.run = nil
}
func (r *txRead[K, V]) FindIt(mod SearchModifier, k K) (*KVcloser[K, V], bool, error, *Iter[K, V]) {
	if err := r.s.check(); err != nil {
		return nil, false, err, nil
	}
	if mod < Exact || mod > LT {
		return nil, false, ErrSearchModifier, nil
	}
	it := r.NewIter()
	it.seek(k, true, mod == GT || mod == LT, mod == LTE || mod == LT)
	exact := it.Valid() && equalKey(it.Key(), k)
	if mod == Exact && !exact {
		it.valid = false
		it.current = KV[K, V]{}
		it.run = nil
		it.leaf = nil
		it.cursor = readCursor[K, V]{}
	}
	if !it.Valid() {
		return nil, false, nil, it
	}
	kv := &KVcloser[K, V]{KV: it.current, s: r.s}
	if r.s.results == nil {
		r.s.results = make(map[*KVcloser[K, V]]struct{})
	}
	r.s.results[kv] = struct{}{}
	return kv, exact, nil, it
}
func (r *txRead[K, V]) Find(mod SearchModifier, k K) (*KVcloser[K, V], bool, error) {
	kv, exact, err, it := r.FindIt(mod, k)
	if it != nil {
		it.Close()
	}
	return kv, exact, err
}
func (r *txRead[K, V]) GetKV(k K) (*KVcloser[K, V], error) { v, _, e := r.Find(Exact, k); return v, e }

// walk consumes contiguous runs without materializing a range. The local
// cursor cannot escape to callers. Lifetime, epoch and wrap checks precede
// every reuse after a callback, including when the callback ends ownership.
func (r *txRead[K, V]) walk(start, end K, boundedStart, boundedEnd, reverse bool, limit int, visit func(K, V) bool) {
	r.mustOpen()
	if limit <= 0 {
		return
	}
	s := r.s
	core := &s.db.core
	var n *node[K, V]
	if boundedStart {
		n = core.findLeaf(start)
	} else {
		n = core.root
		for n != nil && n.leaf == nil {
			i := 0
			if reverse {
				i = len(n.children) - 1
			}
			n = n.children[i]
		}
	}
	strict := false
	seek := boundedStart
scan:
	for n != nil && limit > 0 {
		s.prepare(n.leaf)
		c := n.leaf.readCursor(start, seek, strict, reverse)
		epoch, wrap := s.epoch, s.wrap
		for {
			run := c.nextRun()
			if len(run) == 0 {
				break
			}
			for j := 0; j < len(run) && limit > 0; j++ {
				i := j
				if reverse {
					i = len(run) - 1 - j
				}
				e := run[i]
				if boundedEnd {
					cmp := compareKey(e.key, end)
					if (!reverse && cmp >= 0) || (reverse && cmp <= 0) {
						return
					}
				}
				if !visit(e.key, e.value) || s.closed {
					return
				}
				limit--
				if limit == 0 {
					return
				}
				if epoch != s.epoch || wrap != s.wrap {
					start, seek, strict = e.key, true, true
					n = core.findLeaf(start)
					continue scan
				}
			}
		}
		if reverse {
			n = n.prev
		} else {
			n = n.next
		}
		seek, strict = false, false
	}
}
func (r *txRead[K, V]) Ascend(k K, visit func(K, V) bool) {
	var z K
	r.walk(k, z, true, false, false, int(^uint(0)>>1), visit)
}
func (r *txRead[K, V]) Descend(k K, visit func(K, V) bool) {
	var z K
	r.walk(k, z, true, false, true, int(^uint(0)>>1), visit)
}
func (r *txRead[K, V]) AscendRange(lo, hi K, visit func(K, V) bool) {
	r.walk(lo, hi, true, true, false, int(^uint(0)>>1), visit)
}
func (r *txRead[K, V]) DescendRange(hi, lo K, visit func(K, V) bool) {
	r.walk(hi, lo, true, true, true, int(^uint(0)>>1), visit)
}
func (r *txRead[K, V]) Range(lo, hi K, visit func(K, V) bool)    { r.AscendRange(lo, hi, visit) }
func (r *txRead[K, V]) MapRange(lo, hi K, visit func(K, V) bool) { r.AscendRange(lo, hi, visit) }
func (r *txRead[K, V]) Scan(start K, n int, visit func(K, V) bool) {
	var z K
	r.walk(start, z, true, false, false, n, visit)
}
func (r *txRead[K, V]) All() iter.Seq2[K, V] {
	r.mustOpen()
	return func(yield func(K, V) bool) { var z K; r.walk(z, z, false, false, false, int(^uint(0)>>1), yield) }
}
