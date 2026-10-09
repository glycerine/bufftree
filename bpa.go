package bufftree

import (
	"cmp"
	"slices"
)

type entry[K cmp.Ordered, V any] struct {
	key   K
	value V
	dead  bool
}

// All records reside in one allocation: log, header, then fixed-size blocks.
// Header records also serve as partition markers, including when tombstoned.
type bpa[K cmp.Ordered, V any] struct {
	cfg           Config
	data          []entry[K, V]
	logN          int
	headerN       int
	size          int
	counts        []int
	sorted        []bool
	logSorted     bool
	rebuildBuffer *[]entry[K, V]
}

func newBPA[K cmp.Ordered, V any](cfg Config, shared ...*[]entry[K, V]) *bpa[K, V] {
	cfg = cfg.normalized()
	p := &bpa[K, V]{cfg: cfg, data: make([]entry[K, V], cfg.LogSize+cfg.NumBlocks+cfg.NumBlocks*cfg.BlockSize), counts: make([]int, cfg.NumBlocks), sorted: make([]bool, cfg.NumBlocks), logSorted: true}
	if len(shared) != 0 {
		p.rebuildBuffer = shared[0]
	}
	return p
}
func (p *bpa[K, V]) capacity() int             { return p.cfg.NumBlocks * p.cfg.BlockSize }
func (p *bpa[K, V]) log() []entry[K, V]        { return p.data[:p.logN] }
func (p *bpa[K, V]) header(i int) *entry[K, V] { return &p.data[p.cfg.LogSize+i] }
func (p *bpa[K, V]) block(i int) []entry[K, V] {
	start := p.cfg.LogSize + p.cfg.NumBlocks + i*p.cfg.BlockSize
	return p.data[start : start+p.counts[i]]
}
func compareEntry[K cmp.Ordered, V any](a, b entry[K, V]) int { return compareKey(a.key, b.key) }
func lower[K cmp.Ordered, V any](es []entry[K, V], k K, strict bool) int {
	lo, hi := 0, len(es)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if lessKey(es[mid].key, k) || (strict && equalKey(es[mid].key, k)) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
func (p *bpa[K, V]) partition(k K) int {
	lo, hi := 0, p.headerN
	for lo < hi {
		mid := lo + (hi-lo)/2
		if !lessKey(k, p.header(mid).key) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo - 1
}

// baseLocation returns an actual array index, or -1.
func (p *bpa[K, V]) baseLocation(k K) int {
	i := p.partition(k)
	if i < 0 {
		return -1
	}
	if equalKey(p.header(i).key, k) {
		return p.cfg.LogSize + i
	}
	start := p.cfg.LogSize + p.cfg.NumBlocks + i*p.cfg.BlockSize
	for j, e := range p.block(i) {
		if equalKey(e.key, k) {
			return start + j
		}
	}
	return -1
}
func (p *bpa[K, V]) get(k K) (V, bool) {
	for _, e := range p.log() {
		if equalKey(e.key, k) {
			return e.value, !e.dead
		}
	}
	if i := p.baseLocation(k); i >= 0 && !p.data[i].dead {
		return p.data[i].value, true
	}
	var zero V
	return zero, false
}
func (p *bpa[K, V]) set(k K, v V) (V, bool) {
	old, found := p.get(k)
	if !found {
		if p.size == p.capacity() {
			panic("bufftree: full BPA")
		}
		p.size++
	}
	p.writeLog(entry[K, V]{key: k, value: v})
	return old, found
}

// Updating an existing record cannot change occupancy or partition order.
// Avoid introducing a duplicate log record and a later flush for overwrites.
func (p *bpa[K, V]) overwrite(k K, v V) {
	for i, e := range p.log() {
		if equalKey(e.key, k) {
			p.data[i] = entry[K, V]{key: k, value: v}
			return
		}
	}
	i := p.baseLocation(k)
	if i < 0 || p.data[i].dead {
		panic("bufftree: cannot overwrite an absent record")
	}
	p.data[i] = entry[K, V]{key: k, value: v}
}
func (p *bpa[K, V]) del(k K) (V, bool) {
	old, found := p.get(k)
	if !found {
		return old, false
	}
	p.size--
	p.writeLog(entry[K, V]{key: k, dead: true})
	return old, true
}
func (p *bpa[K, V]) writeLog(e entry[K, V]) {
	for i, old := range p.log() {
		if equalKey(old.key, e.key) {
			p.data[i] = e
			return
		}
	}
	p.data[p.logN] = e
	p.logN++
	p.logSorted = false
	if p.logN == p.cfg.LogSize {
		p.flush()
	}
}
func (p *bpa[K, V]) sortLog() {
	if !p.logSorted {
		slices.SortFunc(p.log(), compareEntry[K, V])
		p.logSorted = true
	}
}
func (p *bpa[K, V]) sortBlock(i int) {
	if !p.sorted[i] {
		slices.SortFunc(p.block(i), compareEntry[K, V])
		p.sorted[i] = true
	}
}
func (p *bpa[K, V]) flush() {
	if p.logN == 0 {
		return
	}
	p.sortLog()
	// Preflight net occupancy. Updates and deletions do not consume slots.
	var local [64]int
	var dest []int
	if p.cfg.NumBlocks <= len(local) {
		dest = local[:p.cfg.NumBlocks]
	} else {
		dest = make([]int, p.cfg.NumBlocks)
	}
	copy(dest, p.counts)
	rebuild := p.headerN == 0
	for _, e := range p.log() {
		i := p.partition(e.key)
		loc := p.baseLocation(e.key)
		if loc >= p.cfg.LogSize+p.cfg.NumBlocks {
			if e.dead {
				dest[i]--
			}
		} else if loc < 0 && !e.dead {
			if i < 0 {
				rebuild = true
			} else {
				dest[i]++
			}
		}
	}
	for _, n := range dest {
		if n >= p.cfg.BlockSize {
			rebuild = true
		}
	}
	if rebuild {
		if p.rebuildBuffer == nil {
			p.load(p.collect())
		} else {
			// Rebuilds are synchronous and have no callbacks. Leaves of a tree
			// can share one buffer without retaining a spare array per leaf.
			buf := *p.rebuildBuffer
			if cap(buf) < p.size {
				buf = make([]entry[K, V], 0, p.capacity())
			}
			es := p.collectInto(buf[:0])
			p.load(es)
			clear(es)
			*p.rebuildBuffer = es[:0]
		}
		return
	}
	// Apply deletions first so a temporarily full block cannot overrun storage.
	for _, e := range p.log() {
		if !e.dead {
			continue
		}
		i := p.partition(e.key)
		loc := p.baseLocation(e.key)
		if loc < 0 {
			continue
		}
		if loc < p.cfg.LogSize+p.cfg.NumBlocks {
			p.data[loc] = e
		} else {
			block := p.block(i)
			p.data[loc] = block[len(block)-1]
			clear(block[len(block)-1:])
			p.counts[i]--
			p.sorted[i] = false
		}
	}
	for _, e := range p.log() {
		if e.dead {
			continue
		}
		if loc := p.baseLocation(e.key); loc >= 0 {
			p.data[loc] = e
		} else {
			i := p.partition(e.key)
			start := p.cfg.LogSize + p.cfg.NumBlocks + i*p.cfg.BlockSize
			p.data[start+p.counts[i]] = e
			p.counts[i]++
			p.sorted[i] = false
		}
	}
	clear(p.log())
	p.logN = 0
	p.logSorted = true
}

// load redistributes sorted, unique, live entries across all available blocks.
func (p *bpa[K, V]) load(es []entry[K, V]) {
	if len(es) > p.capacity() {
		panic("bufftree: BPA redistribution overflow")
	}
	clear(p.data)
	clear(p.counts)
	clear(p.sorted)
	p.logN, p.size, p.headerN = 0, len(es), min(len(es), p.cfg.NumBlocks)
	p.logSorted = true
	pos := 0
	for i := 0; i < p.headerN; i++ {
		n := (len(es) - pos) / (p.headerN - i)
		*p.header(i) = es[pos]
		p.counts[i] = n - 1
		copy(p.block(i), es[pos+1:pos+n])
		p.sorted[i] = true
		pos += n
	}
}
func (p *bpa[K, V]) collect() []entry[K, V] {
	return p.collectInto(make([]entry[K, V], 0, p.size))
}
func (p *bpa[K, V]) collectInto(es []entry[K, V]) []entry[K, V] {
	c := p.cursor(*new(K), false, false)
	for e, ok := c.next(); ok; e, ok = c.next() {
		es = append(es, e)
	}
	return es
}

// A cursor merges the log with the ordered header/block stream. Blocks are
// sorted lazily as they are reached; a log version always wins a duplicate.
type bpaCursor[K cmp.Ordered, V any] struct {
	p                            *bpa[K, V]
	logPos, blockIndex, blockPos int
	headerPending                bool
	base                         entry[K, V]
	hasBase                      bool
	block                        []entry[K, V]
}

func (p *bpa[K, V]) cursor(k K, bounded, strict bool) bpaCursor[K, V] {
	p.sortLog()
	c := bpaCursor[K, V]{p: p, headerPending: true}
	if bounded {
		c.logPos = lower(p.log(), k, strict)
		c.blockIndex = max(0, p.partition(k))
	}
	if c.blockIndex < p.headerN {
		p.sortBlock(c.blockIndex)
		c.block = p.block(c.blockIndex)
		if bounded {
			cmpKey := compareKey(p.header(c.blockIndex).key, k)
			c.headerPending = cmpKey > 0 || (cmpKey == 0 && !strict)
			c.blockPos = lower(c.block, k, strict)
		}
	}
	c.advanceBase()
	return c
}
func (c *bpaCursor[K, V]) advanceBase() {
	p := c.p
	for c.blockIndex < p.headerN {
		if c.headerPending {
			c.base, c.hasBase = *p.header(c.blockIndex), true
			c.headerPending = false
			return
		}
		if c.blockPos < len(c.block) {
			c.base, c.hasBase = c.block[c.blockPos], true
			c.blockPos++
			return
		}
		c.blockIndex++
		c.blockPos = 0
		c.headerPending = true
		if c.blockIndex < p.headerN {
			p.sortBlock(c.blockIndex)
			c.block = p.block(c.blockIndex)
		}
	}
	c.hasBase = false
}
func (c *bpaCursor[K, V]) next() (entry[K, V], bool) {
	for c.logPos < c.p.logN || c.hasBase {
		var e entry[K, V]
		if c.logPos < c.p.logN && (!c.hasBase || !lessKey(c.base.key, c.p.data[c.logPos].key)) {
			e = c.p.data[c.logPos]
			c.logPos++
			if c.hasBase && equalKey(e.key, c.base.key) {
				c.advanceBase()
			}
		} else {
			e = c.base
			c.advanceBase()
		}
		if !e.dead {
			return e, true
		}
	}
	return entry[K, V]{}, false
}

// mapEntries filters the unsorted block stream using sorted log membership.
func (p *bpa[K, V]) mapEntries(start, end K, out []entry[K, V]) []entry[K, V] {
	p.sortLog()
	for _, e := range p.log() {
		if !e.dead && !lessKey(e.key, start) && lessKey(e.key, end) {
			out = append(out, e)
		}
	}
	for i := max(0, p.partition(start)); i < p.headerN && lessKey(p.header(i).key, end); i++ {
		header := *p.header(i)
		logLo := lower(p.log(), header.key, false)
		logHi := p.logN
		if i+1 < p.headerN {
			logHi = lower(p.log(), p.header(i+1).key, false)
		}
		shadow := p.log()[logLo:logHi]
		checkLow := lessKey(header.key, start)
		checkHigh := i+1 == p.headerN || lessKey(end, p.header(i+1).key)
		if !header.dead && !checkLow && (len(shadow) == 0 || !equalKey(shadow[0].key, header.key)) {
			out = append(out, header)
		}
		block := p.block(i)
		// An interior block with no buffered shadows can be copied in bulk.
		if !checkLow && !checkHigh && len(shadow) == 0 {
			out = append(out, block...)
			continue
		}
		for _, e := range block {
			if (checkLow && lessKey(e.key, start)) || (checkHigh && !lessKey(e.key, end)) {
				continue
			}
			if len(shadow) != 0 {
				j := lower(shadow, e.key, false)
				if j < len(shadow) && equalKey(shadow[j].key, e.key) {
					continue
				}
			}
			out = append(out, e)
		}
	}
	return out
}
