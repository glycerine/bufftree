package bufftree

import (
	"cmp"
	"hash/maphash"
)

// All leaves share one immutable process-local seed. Comparable respects named
// types and signed-zero equivalence. NaNs must bypass its randomized NaN hash:
// our comparator treats every NaN as the same key.
var membershipSeed = maphash.MakeSeed()

func membershipHash[K cmp.Ordered](k K) uint64 {
	if k != k {
		return 0x9e3779b97f4a7c15
	}
	return maphash.Comparable(membershipSeed, k)
}
func filterWords(capacity int) int {
	n := 1
	for n < (capacity-1)/16+1 {
		n *= 2
	}
	return n
}
func filterMask(h uint64) uint64 { return uint64(1)<<(h&63) | uint64(1)<<((h>>6)&63) }
func (p *bpa[K, V]) mayContain(h uint64) bool {
	mask := filterMask(h)
	return p.membership[(h>>12)&uint64(len(p.membership)-1)]&mask == mask
}
func (p *bpa[K, V]) remember(h uint64) {
	p.membership[(h>>12)&uint64(len(p.membership)-1)] |= filterMask(h)
}
