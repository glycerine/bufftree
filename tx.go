package bufftree

import (
	"cmp"
	"errors"
	"sync"
)

var (
	ErrTxClosed       = errors.New("bufftree: transaction is closed")
	ErrSearchModifier = errors.New("bufftree: unknown search modifier")
	ErrMergeAction    = errors.New("bufftree: merge cannot both write and delete")
)

// Tree owns an in-memory BP-tree. All access goes through a transaction.
// Do not copy a Tree after first use. Its zero value is an empty database.
type Tree[K cmp.Ordered, V any] struct {
	mu         sync.RWMutex
	core       treeCore[K, V]
	undoBuffer []undoRecord[K, V]
}

// NewBPTree copies and validates cfg. Nil and zero fields select defaults.
func NewBPTree[K cmp.Ordered, V any](cfg *Config) *Tree[K, V] {
	var c Config
	if cfg != nil {
		c = *cfg
	}
	return &Tree[K, V]{core: treeCore[K, V]{cfg: c.normalized()}}
}

// ReadOnlyTx holds shared access to a stable committed state. Close manual
// transactions promptly. Use each transaction sequentially in one goroutine.
type ReadOnlyTx[K cmp.Ordered, V any] struct {
	txRead[K, V]
	state txState[K, V]
}

// WriteTx holds exclusive access and sees its own writes. Values containing
// references must be treated as immutable; rollback restores bindings, not
// mutations made through pointers or slices. Defer Rollback after BeginUpdate.
type WriteTx[K cmp.Ordered, V any] struct {
	txRead[K, V]
	state     txState[K, V]
	firstUndo [1]undoRecord[K, V]
}

type txRead[K cmp.Ordered, V any] struct{ s *txState[K, V] }
type txState[K cmp.Ordered, V any] struct {
	db                     *Tree[K, V]
	write, managed, closed bool
	epoch                  uint64
	wrap                   *epochWrap
	iters                  map[*Iter[K, V]]struct{}
	results                map[*KVcloser[K, V]]struct{}
	undo                   []undoRecord[K, V]
	clears                 [][]entry[K, V]
}
type epochWrap struct{ marker byte }

type undoKind uint8

const (
	undoAbsent undoKind = iota
	undoPresent
	undoClear
)

// key is the previous stored representative when present, otherwise the input
// key. Both identify the same comparator equivalence class during undo.
// Clear images live separately so ordinary scalar journals contain no pointers.
type undoRecord[K cmp.Ordered, V any] struct {
	key   K
	value V
	kind  undoKind
}

// BeginView acquires shared access. Never begin a nested transaction on the
// same Tree, even another read transaction; pass the existing handle instead.
func (db *Tree[K, V]) BeginView() *ReadOnlyTx[K, V] {
	db.mu.RLock()
	tx := &ReadOnlyTx[K, V]{}
	tx.s = &tx.state
	tx.s.db = db
	return tx
}

// BeginUpdate acquires exclusive access. End with Commit or Rollback.
func (db *Tree[K, V]) BeginUpdate() (*WriteTx[K, V], error) {
	db.mu.Lock()
	tx := &WriteTx[K, V]{}
	tx.s = &tx.state
	tx.s.db, tx.s.write = db, true
	tx.s.undo = db.undoBuffer
	db.undoBuffer = nil
	if tx.s.undo == nil {
		tx.s.undo = tx.firstUndo[:0]
	}
	return tx, nil
}

// View holds shared access for the entire callback, including after an explicit
// Close. It releases ownership on return, panic, and runtime.Goexit.
func (db *Tree[K, V]) View(fn func(*ReadOnlyTx[K, V]) error) error {
	tx := db.BeginView()
	tx.s.managed = true
	defer func() { tx.Close(); db.mu.RUnlock() }()
	return fn(tx)
}

// Update commits on nil and rolls back on error, panic, or runtime.Goexit.
// An explicit terminal action wins, but ownership lasts until callback unwind.
func (db *Tree[K, V]) Update(fn func(*WriteTx[K, V]) error) (err error) {
	tx, _ := db.BeginUpdate()
	tx.s.managed = true
	// Do not unlock if rollback itself fails: partial restoration must not publish.
	defer func() {
		if !tx.s.closed {
			_ = tx.Rollback()
		}
		db.mu.Unlock()
	}()
	err = fn(tx)
	if err == nil {
		err = tx.Commit()
	}
	return
}
func (tx *ReadOnlyTx[K, V]) Close() {
	if tx.s.closed {
		return
	}
	tx.s.closeResources()
	tx.s.closed = true
	if !tx.s.managed {
		tx.s.db.mu.RUnlock()
	}
}

// Commit prepares changed leaves for pure reads before publishing. Idempotent.
func (tx *WriteTx[K, V]) Commit() error { return tx.finish(false) }

// Rollback restores the entry logical contents before releasing ownership.
// It has no effect after either terminal action has succeeded.
func (tx *WriteTx[K, V]) Rollback() error { return tx.finish(true) }
func (tx *WriteTx[K, V]) finish(rollback bool) error {
	s := tx.s
	if s.closed {
		return nil
	}
	s.closeResources()
	c := &s.db.core
	if rollback {
		for i := len(s.undo) - 1; i >= 0; i-- {
			u := s.undo[i]
			if u.kind == undoClear {
				last := len(s.clears) - 1
				image := s.clears[last]
				s.clears[last] = nil
				s.clears = s.clears[:last]
				c.Clear()
				for _, e := range image {
					c.Put(e.key, e.value)
				}
			} else {
				c.Del(u.key)
				if u.kind == undoPresent {
					c.Put(u.key, u.value)
					c.restoreKey(u.key)
				}
			}
		}
	}
	c.publish()
	clear(s.undo)
	clear(tx.firstUndo[:])
	// Retain bounded, cleared scratch, never a live transaction or its inline slot.
	if cap(s.undo) > 1 && cap(s.undo) <= 4096 {
		s.db.undoBuffer = s.undo[:0]
	}
	s.undo = nil
	clear(s.clears)
	s.clears = nil
	s.closed = true
	if !s.managed {
		s.db.mu.Unlock()
	}
	return nil
}
func (s *txState[K, V]) closeResources() {
	for it := range s.iters {
		it.Close()
	}
	for r := range s.results {
		r.Close()
	}
}
func (s *txState[K, V]) check() error {
	if s.closed {
		return ErrTxClosed
	}
	return nil
}
func (r *txRead[K, V]) mustOpen() {
	if err := r.s.check(); err != nil {
		panic(err)
	}
}
func (s *txState[K, V]) invalidate() {
	s.epoch++
	if s.epoch == 0 { // No ancient cursor may accidentally match a wrapped epoch.
		s.wrap = new(epochWrap)
		for it := range s.iters {
			it.stale = true
		}
	}
}
func (s *txState[K, V]) prepare(p *bpa[K, V]) {
	if s.write && p.needsPrep {
		s.invalidate()
		p.prepareRead()
	}
}
func (p *bpa[K, V]) prepareRead() {
	p.sortLog()
	for i := 0; i < p.headerN; i++ {
		p.sortBlock(i)
	}
}
func (c *treeCore[K, V]) discardPreparation() {
	for c.preparing != nil {
		p := c.preparing
		c.preparing = p.nextPrep
		p.nextPrep = nil
		p.needsPrep = false
	}
}
func (c *treeCore[K, V]) publish() {
	// Separate from pending: Len can reconcile counts before publication.
	// Retired leaves may remain here until finalization; preparing them is harmless.
	for p := c.preparing; p != nil; p = p.nextPrep {
		p.prepareRead()
	}
	c.Len()
	c.discardPreparation()
}

// Get returns a value and presence bit. Returned reference-valued data is
// borrowed and must not be modified; use Put with a replacement value.
func (r *txRead[K, V]) Get(key K) (V, bool, error) {
	var zero V
	if err := r.s.check(); err != nil {
		return zero, false, err
	}
	v, ok := r.s.db.core.Get2(key)
	if !ok {
		v = zero
	}
	return v, ok, nil
}
func (r *txRead[K, V]) Len() int64 {
	r.mustOpen()
	if r.s.write {
		return int64(r.s.db.core.Len())
	}
	return int64(r.s.db.core.length)
}
func (s *txState[K, V]) preimage(key K) undoRecord[K, V] {
	u := undoRecord[K, V]{key: key}
	if n := s.db.core.findLeaf(key); n != nil {
		p := n.leaf
		if i := p.location(key); i >= 0 && !p.isDead(i) {
			u.key, u.value, u.kind = p.data[i].key, p.data[i].value, undoPresent
		}
	}
	return u
}
func (tx *WriteTx[K, V]) Put(key K, value V) error {
	s := tx.s
	if err := s.check(); err != nil {
		return err
	}
	c := &s.db.core
	n := c.findLeaf(key)
	if n == nil {
		s.undo = append(s.undo, undoRecord[K, V]{key: key})
		s.invalidate()
		c.Put(key, value)
		return nil
	}
	p := n.leaf
	hash := membershipHash(key)
	loc := -1
	if p.mayContain(hash) {
		loc = p.location(key)
	}
	u := undoRecord[K, V]{key: key}
	if loc >= 0 && !p.isDead(loc) {
		e := p.data[loc]
		u = undoRecord[K, V]{key: e.key, value: e.value, kind: undoPresent}
	}
	// Journal before mutation. The same search supplies both undo and membership.
	s.undo = append(s.undo, u)
	s.invalidate()
	if u.kind == undoPresent {
		p.updateValue(loc, value)
		c.version++
		return nil
	}
	// Raw split/rollback helpers may leave a count prefix pending. Ordinary
	// transaction inserts below keep it exact, avoiding publication re-searches.
	if p.countedLog != p.logN {
		p.resolveSize()
	}
	if p.size >= p.capacity() {
		c.putAt(n, key, value)
		return nil
	}
	c.markDirty(p)
	p.remember(hash)
	p.size++
	if loc >= 0 && loc < p.cfg.LogSize { // resurrect an already-counted tombstone
		p.data[loc].value = value
		p.setDead(loc, false)
	} else {
		if p.logSorted {
			p.logPrefix = p.logN
		}
		p.data[p.logN] = entry[K, V]{key: key, value: value}
		p.logN++
		p.countedLog = p.logN
		p.logSorted = false
		if p.logN == p.cfg.LogSize {
			p.flush()
		}
	}
	if lessKey(key, n.min) {
		n.min = key
		refreshUp(n.parent)
	}
	c.version++
	return nil
}
func (tx *WriteTx[K, V]) Delete(key K) error {
	if err := tx.s.check(); err != nil {
		return err
	}
	u := tx.s.preimage(key)
	if u.kind != undoPresent {
		return nil
	}
	tx.s.undo = append(tx.s.undo, u)
	tx.s.invalidate()
	tx.s.db.core.Del(key)
	return nil
}
func (tx *WriteTx[K, V]) Del(key K) error { return tx.Delete(key) }
func (tx *WriteTx[K, V]) Clear() (bool, error) {
	if err := tx.s.check(); err != nil {
		return false, err
	}
	var image []entry[K, V]
	it := tx.NewIter()
	for it.SeekFirst(); it.Valid(); it.Next() {
		image = append(image, entry[K, V]{it.Key(), it.Value()})
	}
	it.Close()
	tx.s.clears = append(tx.s.clears, image)
	tx.s.undo = append(tx.s.undo, undoRecord[K, V]{kind: undoClear})
	tx.s.invalidate()
	tx.s.db.core.Clear()
	return true, nil
}
func (tx *WriteTx[K, V]) DeleteRange(beg, end K, begInclusive, endInclusive bool) (n int64, allGone bool, err error) {
	if err = tx.s.check(); err != nil {
		return
	}
	if compareKey(beg, end) > 0 {
		return 0, tx.Len() == 0, nil
	}
	it := tx.NewIter()
	defer it.Close()
	it.seek(beg, true, !begInclusive, false)
	for it.Valid() {
		c := compareKey(it.Key(), end)
		if c > 0 || (c == 0 && !endInclusive) {
			break
		}
		if err = tx.Delete(it.Key()); err != nil {
			return
		}
		n++
		it.Next()
	}
	return n, tx.Len() == 0, nil
}
func (tx *WriteTx[K, V]) Merge(key K, fn func(V, bool) (V, bool, bool)) error {
	old, ok, err := tx.Get(key)
	if err != nil {
		return err
	}
	v, write, del := fn(old, ok)
	if err = tx.s.check(); err != nil {
		return err
	}
	if write && del {
		return ErrMergeAction
	}
	if write {
		return tx.Put(key, v)
	}
	if del {
		return tx.Delete(key)
	}
	return nil
}

// Core Put may resurrect a tombstoned log slot without changing its key bits.
// Undo must also restore the representative of NaN/signed-zero equivalence.
func (c *treeCore[K, V]) restoreKey(k K) {
	n := c.findLeaf(k)
	p := n.leaf
	i := p.location(k)
	p.data[i].key = k
	if i >= p.cfg.LogSize && i < p.cfg.LogSize+p.cfg.NumBlocks {
		p.mirrorHeader(i - p.cfg.LogSize)
	}
	if equalKey(n.min, k) {
		n.min = k
		refreshUp(n.parent)
	}
}
