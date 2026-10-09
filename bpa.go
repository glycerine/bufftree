package bufftree

import (
	"cmp"
	"slices"
)

type entry[K cmp.Ordered, V any] struct {
	key   K
	value V
}

// All records reside in one allocation: log, header, then fixed-size blocks.
// Header records also serve as partition markers, including when tombstoned.
// Each block's reserved first slot mirrors its header, making the live header
// and sorted block one contiguous scan run without increasing the allocation.
type bpa[K cmp.Ordered, V any] struct {
	cfg           Config
	data          []entry[K, V]
	dead          []uint64 // log and header only; block deletions compact in place
	logN          int
	headerN       int
	size          int
	baseSize      int
	countedLog    int // log prefix whose membership has already been counted
	accounted     int // contribution to the owning tree's cached length
	dirty         bool
	scanReady     bool // a previous scan has visited this leaf since redistribution
	ordered       bool // entire leaf is sorted; guarded by the owning tree's lock
	nextDirty     *bpa[K, V]
	prevDirty     *bpa[K, V]
	counts        []int
	sorted        []bool
	logSorted     bool
	rebuildBuffer *[]entry[K, V]
}

func newBPA[K cmp.Ordered, V any](cfg Config, shared ...*[]entry[K, V]) *bpa[K, V] {
	cfg = cfg.normalized()
	p := &bpa[K, V]{cfg: cfg, data: make([]entry[K, V], cfg.LogSize+cfg.NumBlocks+cfg.NumBlocks*cfg.BlockSize), counts: make([]int, cfg.NumBlocks), sorted: make([]bool, cfg.NumBlocks), logSorted: true}
	p.dead = make([]uint64, (cfg.LogSize+cfg.NumBlocks+63)/64)
	if len(shared) != 0 {
		p.rebuildBuffer = shared[0]
	}
	return p
}
func (p *bpa[K, V]) isDead(i int) bool {
	return i < p.cfg.LogSize+p.cfg.NumBlocks && p.dead[i/64]&(uint64(1)<<uint(i%64)) != 0
}
func (p *bpa[K, V]) setDead(i int, dead bool) {
	mask := uint64(1) << uint(i%64)
	if dead {
		p.dead[i/64] |= mask
	} else {
		p.dead[i/64] &^= mask
	}
}
func (p *bpa[K, V]) clearLog() {
	clear(p.log())
	n := p.logN
	clear(p.dead[:n/64])
	if n%64 != 0 {
		p.dead[n/64] &^= (uint64(1) << uint(n%64)) - 1
	}
	p.logN, p.countedLog, p.size, p.logSorted = 0, 0, p.baseSize, true
}
func (p *bpa[K, V]) capacity() int             { return p.cfg.NumBlocks * p.cfg.BlockSize }
func (p *bpa[K, V]) log() []entry[K, V]        { return p.data[:p.logN] }
func (p *bpa[K, V]) header(i int) *entry[K, V] { return &p.data[p.cfg.LogSize+i] }
func (p *bpa[K, V]) block(i int) []entry[K, V] {
	start := p.cfg.LogSize + p.cfg.NumBlocks + i*p.cfg.BlockSize + 1
	return p.data[start : start+p.counts[i]]
}
func (p *bpa[K, V]) mirrorHeader(i int) {
	p.data[p.cfg.LogSize+p.cfg.NumBlocks+i*p.cfg.BlockSize] = *p.header(i)
}

// All in-place base-value updates must keep header mirrors synchronized, both
// for scan correctness and to avoid retaining an obsolete pointer-valued V.
func (p *bpa[K, V]) updateValue(i int, v V) {
	p.data[i].value = v
	if i >= p.cfg.LogSize && i < p.cfg.LogSize+p.cfg.NumBlocks {
		p.mirrorHeader(i - p.cfg.LogSize)
	}
}
func (p *bpa[K, V]) scanBlock(i int) []entry[K, V] {
	p.sortBlock(i)
	start := p.cfg.LogSize + p.cfg.NumBlocks + i*p.cfg.BlockSize
	end := start + 1 + p.counts[i]
	if p.isDead(p.cfg.LogSize + i) {
		start++
	}
	return p.data[start:end]
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
	start := p.cfg.LogSize + p.cfg.NumBlocks + i*p.cfg.BlockSize + 1
	for j, e := range p.block(i) {
		if equalKey(e.key, k) {
			return start + j
		}
	}
	return -1
}
func (p *bpa[K, V]) get(k K) (V, bool) {
	for j, e := range p.log() {
		if equalKey(e.key, k) {
			return e.value, !p.isDead(j)
		}
	}
	if i := p.baseLocation(k); i >= 0 && !p.isDead(i) {
		return p.data[i].value, true
	}
	var zero V
	return zero, false
}

// location includes tombstones and gives deletion the physical slot to modify.
func (p *bpa[K, V]) location(k K) int {
	for i := 0; i < p.logN; i++ {
		if equalKey(p.data[i].key, k) {
			return i
		}
	}
	return p.baseLocation(k)
}

// trySet checks only the insertion log. size is an upper bound until a flush
// or resolveSize removes the contribution of buffered duplicates.
func (p *bpa[K, V]) trySet(k K, v V) bool {
	log := p.log()
	for i := range log {
		if equalKey(log[i].key, k) {
			if p.isDead(i) {
				if p.size >= p.capacity() {
					return false
				}
				p.size++
				p.setDead(i, false)
			}
			log[i].value = v
			return true
		}
	}
	if p.size >= p.capacity() {
		return false
	}
	p.size++
	p.appendLog(entry[K, V]{key: k, value: v})
	return true
}

func (p *bpa[K, V]) set(k K, v V) {
	if p.trySet(k, v) {
		return
	}
	p.flush()
	if p.size == p.capacity() {
		if i := p.baseLocation(k); i >= 0 && !p.isDead(i) {
			p.updateValue(i, v)
			return
		}
		panic("bufftree: full BPA")
	}
	p.trySet(k, v)
}

func (p *bpa[K, V]) resolveSize() int {
	// New log entries optimistically count as new keys. Subtract each base
	// duplicate once. Deleting/resurrecting log records adjusts size directly,
	// so this correction is the same for live entries and tombstones.
	for j := p.countedLog; j < p.logN; j++ {
		if i := p.baseLocation(p.data[j].key); i >= 0 && !p.isDead(i) {
			p.size--
		}
	}
	p.countedLog = p.logN
	return p.size
}

func (p *bpa[K, V]) del(k K) bool {
	i := p.location(k)
	if i < 0 || p.isDead(i) {
		return false
	}
	p.size--
	if i < p.cfg.LogSize {
		p.setDead(i, true)
		var zero V
		p.data[i].value = zero
	} else if i < p.cfg.LogSize+p.cfg.NumBlocks {
		p.setDead(i, true)
		var zero V
		p.data[i].value = zero
		p.mirrorHeader(i - p.cfg.LogSize)
		p.baseSize--
	} else {
		b := (i - p.cfg.LogSize - p.cfg.NumBlocks) / p.cfg.BlockSize
		block := p.block(b)
		p.data[i] = block[len(block)-1]
		clear(block[len(block)-1:])
		p.counts[b]--
		p.sorted[b] = false
		p.ordered = false
		p.baseSize--
	}
	p.resolveSize()
	return true
}

// appendLog requires a live entry whose key is not already in the log.
func (p *bpa[K, V]) appendLog(e entry[K, V]) {
	p.ordered = false
	// Unused log slots have zero bits, established by newBPA/clearLog/load.
	p.data[p.logN] = e
	p.logN++
	p.logSorted = false
	if p.logN == p.cfg.LogSize {
		p.flush()
	}
}
func (p *bpa[K, V]) sortLog() {
	if !p.logSorted {
		// Sorting an entirely counted or uncounted log preserves the prefix.
		// Settle a mixed prefix before records change positions.
		if p.countedLog > 0 && p.countedLog < p.logN {
			p.resolveSize()
		}
		if !p.logHasDead() {
			sortEntries(p.log())
		} else {
			p.sortDeadLog()
		}
		p.logSorted = true
	}
}

func (p *bpa[K, V]) logHasDead() bool {
	for _, word := range p.dead[:p.logN/64] {
		if word != 0 {
			return true
		}
	}
	n := uint(p.logN % 64)
	return n != 0 && p.dead[p.logN/64]&((uint64(1)<<n)-1) != 0
}

// Heapsort carries bitmap bits with their records, including for large custom
// logs. The ordinary, tombstone-free write path uses the faster small sorter.
func (p *bpa[K, V]) sortDeadLog() {
	swap := func(i, j int) {
		a, b := p.isDead(i), p.isDead(j)
		p.data[i], p.data[j] = p.data[j], p.data[i]
		p.setDead(i, b)
		p.setDead(j, a)
	}
	down := func(root, end int) {
		for child := root*2 + 1; child < end; child = root*2 + 1 {
			if child+1 < end && lessKey(p.data[child].key, p.data[child+1].key) {
				child++
			}
			if !lessKey(p.data[root].key, p.data[child].key) {
				break
			}
			swap(root, child)
			root = child
		}
	}
	for i := p.logN/2 - 1; i >= 0; i-- {
		down(i, p.logN)
	}
	for end := p.logN - 1; end > 0; end-- {
		swap(0, end)
		down(0, end)
	}
}
func (p *bpa[K, V]) sortBlock(i int) {
	if !p.sorted[i] {
		sortEntries(p.block(i))
		p.sorted[i] = true
	}
}

// Logs and blocks are small and often almost sorted after a flush. Shift a
// whole record per step and compare keys directly, avoiding comparator calls
// and repeated swaps. Keep O(n log n) sorting for large custom configurations.
func sortEntries[K cmp.Ordered, V any](es []entry[K, V]) {
	if len(es) > 64 {
		slices.SortFunc(es, compareEntry[K, V])
		return
	}
	for i := 1; i < len(es); i++ {
		e := es[i]
		j := i
		for j > 0 && lessKey(e.key, es[j-1].key) {
			es[j] = es[j-1]
			j--
		}
		es[j] = e
	}
}
func (p *bpa[K, V]) flush() {
	if p.logN == 0 {
		return
	}
	p.ordered = false
	p.sortLog()
	log := p.log()
	if p.headerN == 0 || lessKey(log[0].key, p.header(0).key) {
		p.rebuild()
		return
	}
	// Route the sorted batch in one pass over the header. Search each target
	// block only once per buffered key, when the block is already cache-hot.
	i := 0
	for j, e := range log {
		for i+1 < p.headerN && !lessKey(e.key, p.header(i+1).key) {
			i++
		}
		dead := p.isDead(j)
		h := p.cfg.LogSize + i
		if equalKey(e.key, p.data[h].key) {
			if p.isDead(h) && !dead {
				p.baseSize++
			}
			if !p.isDead(h) && dead {
				p.baseSize--
			}
			p.data[h] = e
			p.mirrorHeader(i)
			p.setDead(h, dead)
			continue
		}
		start := p.cfg.LogSize + p.cfg.NumBlocks + i*p.cfg.BlockSize + 1
		block := p.block(i)
		loc := -1
		for k := range block {
			if equalKey(block[k].key, e.key) {
				loc = k
				break
			}
		}
		if loc >= 0 {
			if dead {
				block[loc] = block[len(block)-1]
				clear(block[len(block)-1:])
				p.counts[i]--
				p.baseSize--
				p.sorted[i] = false
			} else {
				block[loc] = e
			}
		} else if !dead {
			if len(block)+1 >= p.cfg.BlockSize {
				// Earlier records may already be copied. collectInto merges
				// away those shadows, including any applied tombstones.
				p.rebuild()
				return
			}
			p.data[start+len(block)] = e
			p.counts[i]++
			p.baseSize++
			p.sorted[i] = false
		}
	}
	p.clearLog()
}

func (p *bpa[K, V]) rebuild() {
	if p.rebuildBuffer == nil {
		p.load(p.collect())
		return
	}
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

// load redistributes sorted, unique, live entries across all available blocks.
func (p *bpa[K, V]) load(es []entry[K, V]) {
	if len(es) > p.capacity() {
		panic("bufftree: BPA redistribution overflow")
	}
	clear(p.data)
	clear(p.dead)
	clear(p.counts)
	clear(p.sorted)
	p.logN, p.size, p.headerN = 0, len(es), min(len(es), p.cfg.NumBlocks)
	p.logSorted = true
	p.scanReady = false
	p.ordered = true
	p.baseSize, p.countedLog = len(es), 0
	pos := 0
	for i := 0; i < p.headerN; i++ {
		n := (len(es) - pos) / (p.headerN - i)
		*p.header(i) = es[pos]
		p.mirrorHeader(i)
		p.counts[i] = n - 1
		copy(p.block(i), es[pos+1:pos+n])
		p.sorted[i] = true
		pos += n
	}
}

// prepareOrdered runs exclusively before publishing a leaf to shared scans.
// Cursor methods may still call sortLog/sortBlock, but those calls become
// read-only once all their sorted flags are set.
func (p *bpa[K, V]) prepareOrdered() {
	if !p.ordered {
		p.sortLog()
		for i := 0; i < p.headerN; i++ {
			p.sortBlock(i)
		}
		p.ordered = true
	}
}
func (p *bpa[K, V]) collect() []entry[K, V] {
	return p.collectInto(make([]entry[K, V], 0, p.size))
}
func (p *bpa[K, V]) collectInto(es []entry[K, V]) []entry[K, V] {
	p.sortLog()
	log := p.log()
	pos := 0
	for i := 0; i < p.headerN; i++ {
		h := *p.header(i)
		hDead := p.isDead(p.cfg.LogSize + i)
		for pos < len(log) && !lessKey(h.key, log[pos].key) {
			e := log[pos]
			dead := p.isDead(pos)
			pos++
			if equalKey(e.key, h.key) {
				hDead = true // the log version wins, including tombstones
			}
			if !dead {
				es = append(es, e)
			}
		}
		if !hDead {
			es = append(es, h)
		}
		p.sortBlock(i)
		block := p.block(i)
		for len(block) > 0 && pos < len(log) {
			// Copy a sorted run at once instead of driving the public cursor
			// state machine once per record during every redistribution.
			j := lower(block, log[pos].key, false)
			es = append(es, block[:j]...)
			block = block[j:]
			if len(block) == 0 {
				break
			}
			e := log[pos]
			dead := p.isDead(pos)
			pos++
			if equalKey(e.key, block[0].key) {
				block = block[1:]
			}
			if !dead {
				es = append(es, e)
			}
		}
		es = append(es, block...)
	}
	for j, e := range log[pos:] {
		if !p.isDead(pos + j) {
			es = append(es, e)
		}
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
	baseDead                     bool
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
			c.baseDead = p.isDead(p.cfg.LogSize + c.blockIndex)
			c.headerPending = false
			return
		}
		if c.blockPos < len(c.block) {
			c.base, c.hasBase = c.block[c.blockPos], true
			c.baseDead = false
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
		var dead bool
		if c.logPos < c.p.logN && (!c.hasBase || !lessKey(c.base.key, c.p.data[c.logPos].key)) {
			e = c.p.data[c.logPos]
			dead = c.p.isDead(c.logPos)
			c.logPos++
			if c.hasBase && equalKey(e.key, c.base.key) {
				c.advanceBase()
			}
		} else {
			e = c.base
			dead = c.baseDead
			c.advanceBase()
		}
		if !dead {
			return e, true
		}
	}
	return entry[K, V]{}, false
}

// A scanCursor merges contiguous runs instead of advancing one base record at
// a time. It borrows leaf storage; callback mutations require a fresh cursor.
type scanCursor[K cmp.Ordered, V any] struct {
	p                  *bpa[K, V]
	base               []entry[K, V]
	logPos, blockIndex int
}

func (p *bpa[K, V]) scanCursor(start K, bounded, strict bool) scanCursor[K, V] {
	p.sortLog()
	c := scanCursor[K, V]{p: p}
	if bounded {
		c.logPos = lower(p.log(), start, strict)
		c.blockIndex = max(0, p.partition(start))
		if c.blockIndex < p.headerN {
			block := p.scanBlock(c.blockIndex)
			c.base = block[lower(block, start, strict):]
			c.blockIndex++
		}
	}
	return c
}

func (c *scanCursor[K, V]) nextRun() []entry[K, V] {
	p := c.p
	if p.logN == 0 {
		if len(c.base) != 0 {
			run := c.base
			c.base = nil
			return run
		}
		for c.blockIndex < p.headerN {
			run := p.scanBlock(c.blockIndex)
			c.blockIndex++
			if len(run) != 0 {
				return run
			}
		}
		return nil
	}
	for {
		for len(c.base) == 0 && c.blockIndex < p.headerN {
			c.base = p.scanBlock(c.blockIndex)
			c.blockIndex++
		}
		if c.logPos < p.logN && (len(c.base) == 0 || !lessKey(c.base[0].key, p.data[c.logPos].key)) {
			i := c.logPos
			c.logPos++
			if len(c.base) != 0 && equalKey(p.data[i].key, c.base[0].key) {
				c.base = c.base[1:]
			}
			if !p.isDead(i) {
				return p.data[i : i+1]
			}
			continue
		}
		run := c.base
		if len(run) != 0 && c.logPos < p.logN && !lessKey(run[len(run)-1].key, p.data[c.logPos].key) {
			run = run[:lower(run, p.data[c.logPos].key, false)]
		}
		c.base = c.base[len(run):]
		return run
	}
}

// mapEntries filters the unsorted block stream using sorted log membership.
func (p *bpa[K, V]) mapEntries(start, end K, out []entry[K, V]) []entry[K, V] {
	p.sortLog()
	for j, e := range p.log() {
		if !p.isDead(j) && !lessKey(e.key, start) && lessKey(e.key, end) {
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
		if !p.isDead(p.cfg.LogSize+i) && !checkLow && (len(shadow) == 0 || !equalKey(shadow[0].key, header.key)) {
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
