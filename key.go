package bufftree

import "cmp"

// Self-inequality is true only for NaNs; it also handles named float types.
// For integer/string instantiations the compiler removes those branches.
func equalKey[K cmp.Ordered](a, b K) bool { return a == b || (a != a && b != b) }
func lessKey[K cmp.Ordered](a, b K) bool  { return a < b || (a == a && b != b) }
func compareKey[K cmp.Ordered](a, b K) int {
	if equalKey(a, b) {
		return 0
	}
	if lessKey(a, b) {
		return -1
	}
	return 1
}
